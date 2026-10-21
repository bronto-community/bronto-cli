package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDashboardsApplyLive covers bronto dashboards apply/export/validate
// against the live API (#198): apply a manifest with two inline widgets
// (one with a formula) and a ref, check the widgets show the seeded spans,
// re-apply with zero writes, change a query (new definition, widget
// repointed, old definition gone), remove a widget and the ref (widget and
// definition deleted, ref only detached), --dry-run writes nothing, and
// export -> apply is a no-op. Everything is named bronto-ci-… (widget
// names become definition names), so the sweeper catches a crashed run.
//
// About 80 requests: every apply reads the dashboards, widgets,
// definitions and datasets lists once.
func TestDashboardsApplyLive(t *testing.T) {
	key := skipIfNoCreds(t)
	serviceA, serviceB := seededTraces(t)
	r := NewRunner(t, key)
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	dsA, dsB := ".traces/"+serviceA, ".traces/"+serviceB

	// Wait until all three seeded spans, and the one error, are searchable
	// in both datasets. Waiting for only some of them isn't enough: the
	// dashboard's queries would then be evaluated on partial data, and
	// Bronto caches a query's result by its content (#205), so they'd keep
	// answering the partial count. Each attempt's where differs so the
	// probe itself never reads a cached answer.
	attempt := 0
	PollUntil(t, seedPollBudget(), 10*time.Second, func() (bool, error) {
		attempt++
		res, err := r.Run(t.Context(), "", "search", fmt.Sprintf("$span.name != 'attempt-%d'", attempt),
			"-d", dsA, "-d", dsB, "--since", "1h", "--select", "count(*)", "-g", "$span.status_code", "-o", "json")
		if err != nil {
			return false, err
		}
		if res.ExitCode != 0 {
			return false, fmt.Errorf("attempt %d: exit %d: %s", attempt, res.ExitCode, res.Stderr)
		}
		var groups []struct {
			Group string  `json:"group"`
			Count float64 `json:"count"`
		}
		if err := json.Unmarshal([]byte(res.Stdout), &groups); err != nil {
			return false, fmt.Errorf("attempt %d: %w", attempt, err)
		}
		total, errs := 0.0, 0.0
		for _, g := range groups {
			total += g.Count
			if g.Group == "STATUS_CODE_ERROR" {
				errs += g.Count
			}
		}
		if total != 3 || errs != 1 {
			return false, fmt.Errorf("attempt %d: %v spans, %v errors searchable; want 3 and 1", attempt, total, errs)
		}
		return true, nil
	})

	dashName := resourceName("dash")
	requests, errRate := resourceName("requests"), resourceName("error-rate")
	shared := mustRunJSONObject(t, r, "widgets", "create", "-f", "name="+resourceName("shared"), "-f", "type=line", "-o", "json")
	sharedID, _ := shared["id"].(string)
	if sharedID == "" {
		t.Fatalf("widgets create: %+v", shared)
	}
	t.Cleanup(func() { bestEffortDelete(r, "widgets", sharedID) })

	requestsWidget := func(where string) string {
		return fmt.Sprintf("  - name: %s\n    type: line\n    queries:\n      - {name: requests, dataset: %s, select: count(*), where: \"%s\"}\n",
			requests, dsA, where)
	}
	errorWidget := fmt.Sprintf("  - name: %s\n    queries:\n"+
		"      - {name: errors, dataset: [%s, %s], select: count(*), where: \"$span.status_code = 'STATUS_CODE_ERROR'\"}\n"+
		"      - {name: total, dataset: [%s, %s], select: count(*)}\n"+
		"    formula: errors / total * 100\n", errRate, dsA, dsB, dsA, dsB)
	head := "name: " + dashName + "\ndescription: bronto-cli integration test\nwidgets:\n"
	server := "$span.kind = 'SPAN_KIND_SERVER'"
	full := write("v1.yaml", head+requestsWidget(server)+errorWidget+"  - ref: "+sharedID+"\n")

	type change struct {
		Action            string   `json:"action"`
		Name              string   `json:"name"`
		ID                string   `json:"id"`
		DeleteDefinitions []string `json:"delete_definitions"`
	}
	type plan struct {
		Dashboard struct {
			Action string `json:"action"`
			ID     string `json:"id"`
		} `json:"dashboard"`
		Widgets []change `json:"widgets"`
		Writes  int      `json:"writes"`
		DryRun  bool     `json:"dry_run"`
	}
	apply := func(path string, extra ...string) plan {
		t.Helper()
		res := mustExitZero(t, r, append([]string{"dashboards", "apply", "-f", path, "-o", "json"}, extra...)...)
		var p plan
		if err := json.Unmarshal([]byte(res.Stdout), &p); err != nil {
			t.Fatalf("apply -o json: %v\n%s", err, res.Stdout)
		}
		return p
	}
	byName := func(p plan) map[string]change {
		m := map[string]change{}
		for _, c := range p.Widgets {
			m[c.Name] = c
		}
		return m
	}

	// 1. Create: dashboard + its tag, 5 writes per inline widget, 1 attach.
	p := apply(full)
	dashID := p.Dashboard.ID
	if dashID == "" || p.Dashboard.Action != "create" {
		t.Fatalf("first apply: %+v", p)
	}
	t.Cleanup(func() {
		// apply itself cleans up: a manifest without widgets deletes every
		// widget and definition it owns. Then the dashboard.
		empty := filepath.Join(dir, "empty.yaml")
		_ = os.WriteFile(empty, []byte("name: "+dashName+"\nwidgets: []\n"), 0o600)
		// t.Context() is already cancelled when cleanups run.
		res, err := r.Run(context.Background(), "", "dashboards", "apply", "-f", empty)
		if err != nil || res.ExitCode != 0 {
			t.Errorf("cleanup apply: %v, exit %d: %s", err, res.ExitCode, res.Stderr)
		}
		bestEffortDelete(r, "dashboards", dashID)
	})
	if p.Writes != 13 {
		t.Errorf("first apply made %d writes, want 13: %+v", p.Writes, p.Widgets)
	}
	w := byName(p)
	reqID, errID := w[requests].ID, w[errRate].ID
	if reqID == "" || errID == "" || w[shared["name"].(string)].Action != "attach" {
		t.Fatalf("widgets: %+v", p.Widgets)
	}

	// 2. The widgets show the seeded spans: serviceA has one server span,
	// and one of the three spans is an error. The formula is named after
	// the widget (error-rate -> …_error_rate) and plots a percentage.
	totals := widgetTotals(t, r, reqID)
	if totals["requests"] != 1 {
		t.Errorf("requests widget totals = %v, want requests 1", totals)
	}
	totals = widgetTotals(t, r, errID)
	formula := 0.0
	for k, v := range totals {
		if strings.HasSuffix(k, "_error_rate") {
			formula = v
		}
	}
	if totals["errors"] != 1 || totals["total"] != 3 || formula <= 0 || formula > 100 {
		t.Errorf("error rate widget totals = %v, want errors 1, total 3 and a percentage from the formula", totals)
	}

	// 3. Re-apply the same file: no writes.
	if p := apply(full); p.Writes != 0 {
		t.Errorf("re-apply made %d writes: %+v", p.Writes, p.Widgets)
	}

	// 4. Change one query: new definition, repoint, delete the old one.
	oldDefs := widgetMetricIDs(t, r, reqID)
	changed := write("v2.yaml", head+requestsWidget(server+" AND $span.name = 'GET /checkout'")+errorWidget+"  - ref: "+sharedID+"\n")
	p = apply(changed)
	c := byName(p)[requests]
	if p.Writes != 3 || c.Action != "update" || len(c.DeleteDefinitions) != 1 || c.DeleteDefinitions[0] != oldDefs[0] {
		t.Errorf("change query: %d writes, %+v (old definition %v)", p.Writes, c, oldDefs)
	}
	if now := widgetMetricIDs(t, r, reqID); len(now) != 1 || now[0] == oldDefs[0] {
		t.Errorf("widget not repointed: metric_ids %v, old %v", now, oldDefs)
	}

	// 5. Remove the error-rate widget and the ref: the widget and its
	// definition are deleted, the ref only detached.
	errDefs := widgetMetricIDs(t, r, errID)
	trimmed := write("v3.yaml", head+requestsWidget(server+" AND $span.name = 'GET /checkout'"))
	p = apply(trimmed)
	if p.Writes != 3 {
		t.Errorf("remove widget + ref: %d writes, want 3: %+v", p.Writes, p.Widgets)
	}
	if res, _ := r.Run(t.Context(), "", "widgets", "get", errID); res.ExitCode != 4 {
		t.Errorf("deleted widget: widgets get exited %d", res.ExitCode)
	}
	mustExitZero(t, r, "widgets", "get", sharedID) // detached, not deleted
	res := mustExitZero(t, r, "metrics", "definitions", "list", "-o", "json", "--jq", ".id")
	for _, gone := range []string{oldDefs[0], errDefs[0]} {
		if strings.Contains(res.Stdout, gone) {
			t.Errorf("definition %s still exists", gone)
		}
	}

	// 6. --dry-run writes nothing: plan adding the error widget back, then
	// the export (below) must not have it.
	if p := apply(full, "--dry-run"); !p.DryRun || p.Writes == 0 {
		t.Errorf("dry run plan: %+v", p)
	}

	// 7. export -> apply is a no-op.
	res = mustExitZero(t, r, "dashboards", "export", dashID)
	if strings.Contains(res.Stdout, errRate) || !strings.Contains(res.Stdout, "dataset: "+dsA) {
		t.Fatalf("export (after a dry run that would add %s):\n%s", errRate, res.Stdout)
	}
	if p := apply(write("export.yaml", res.Stdout)); p.Writes != 0 {
		t.Errorf("export -> apply made %d writes: %+v\n%s", p.Writes, p.Widgets, res.Stdout)
	}
}

// widgetTotals runs widgets query and maps each series to its range value.
func widgetTotals(t *testing.T, r *Runner, widgetID string) map[string]float64 {
	t.Helper()
	res := mustExitZero(t, r, "widgets", "query", widgetID, "--since", "1h", "--slices", "6", "-o", "json")
	var byDef map[string]json.RawMessage
	if err := json.Unmarshal([]byte(res.Stdout), &byDef); err != nil {
		t.Fatalf("widgets query -o json: %v\n%s", err, res.Stdout)
	}
	out := map[string]float64{}
	for _, raw := range byDef {
		totals, err := seriesTotals(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range totals {
			out[k] = v
		}
	}
	return out
}

func widgetMetricIDs(t *testing.T, r *Runner, widgetID string) []string {
	t.Helper()
	w := mustRunJSONObject(t, r, "widgets", "get", widgetID, "-o", "json")
	var ids []string
	arr, _ := w["metric_ids"].([]any)
	for _, v := range arr {
		if s, ok := v.(string); ok {
			ids = append(ids, s)
		}
	}
	if len(ids) == 0 {
		t.Fatalf("widget %s has no metric_ids: %+v", widgetID, w)
	}
	return ids
}
