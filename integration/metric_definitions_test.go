package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestMetricDefinitionsLive covers bronto metrics definitions and
// widgets query against the live API, which is the only check on
// /metrics/definitions: it isn't in the published spec. It builds the
// chain a dashboard uses: seeded spans -> definition (from_expr) -> query
// returns non-zero -> widget -> widgets query, then pins the behaviors
// live-verified on 2026-10-21: deleting a widget leaves its definition,
// and a GET of a deleted definition (a 500 upstream) exits 4.
func TestMetricDefinitionsLive(t *testing.T) {
	key := skipIfNoCreds(t)
	serviceA, serviceB := seededTraces(t)
	r := NewRunner(t, key)

	// Live (2026-10-21) Bronto keys a saved query by its content (the same
	// select/where/from_expr/aggregation get the same query id, whatever
	// the query is named), and a query first evaluated before fresh data
	// was visible to it kept answering zero for 7+ minutes when polled,
	// while a query that differed only in its where saw the data at once.
	// So instead of polling one definition, each attempt creates a new one
	// whose where carries the attempt number, and deletes it on a miss.
	const traces = `"collection" = '.traces'`
	count := []any{map[string]any{"time": "count", "reduce_to": "avg"}}
	var id, name string
	attempt := 0
	PollUntil(t, seedPollBudget(), 10*time.Second, func() (bool, error) {
		attempt++
		nonce := fmt.Sprintf(" AND $span.name != 'attempt-%d'", attempt)
		name = resourceName(fmt.Sprintf("definition-%d", attempt))
		created := mustRunJSONObject(t, r, "metrics", "definitions", "create", "--input", writeBodyFile(t, map[string]any{
			"name": name,
			"queries": []any{
				map[string]any{"name": "requests", "select": []string{"count(*)"}, "from_expr": traces,
					"where": fmt.Sprintf("$service.name = '%s'", serviceA) + nonce, "aggregation": count},
				map[string]any{"name": "errors", "select": []string{"count(*)"}, "from_expr": traces,
					"where":       fmt.Sprintf("$service.name = '%s' AND $span.status_code = 'STATUS_CODE_ERROR'", serviceB) + nonce,
					"aggregation": count},
			},
		}))
		id, _ = created["id"].(string)
		if id == "" {
			t.Fatalf("metrics definitions create response missing id: %+v", created)
		}
		res := mustExitZero(t, r, "metrics", "definitions", "query", id, "--since", "1h", "--slices", "6", "-o", "json")
		totals, err := seriesTotals(res.Stdout)
		if err != nil {
			return false, err
		}
		if totals["requests"] == 0 || totals["errors"] == 0 {
			bestEffortDelete(r, "metrics definitions", id)
			return false, fmt.Errorf("attempt %d: totals %v", attempt, totals)
		}
		if totals["requests"] != 2 || totals["errors"] != 1 {
			t.Errorf("totals = %v, want requests 2 (the server and client span of %s) and errors 1", totals, serviceA)
		}
		return true, nil
	})
	t.Cleanup(func() { bestEffortDelete(r, "metrics definitions", id) })

	// The human view: one row per series, by name.
	res := mustExitZero(t, r, "metrics", "definitions", "query", name, "--since", "1h", "-o", "table")
	if !strings.Contains(res.Stdout, "requests") || !strings.Contains(res.Stdout, "errors") {
		t.Fatalf("query table:\n%s", res.Stdout)
	}
	assertListContainsName(t, r, "metrics definitions", name)

	// A widget plots it; widgets query evaluates every metric id.
	widget := mustRunJSONObject(t, r, "widgets", "create", "--input",
		writeBodyFile(t, map[string]any{"name": resourceName("widget-def"), "type": "line", "metric_ids": []string{id}}))
	widgetID, _ := widget["id"].(string)
	if widgetID == "" {
		t.Fatalf("widgets create response missing id: %+v", widget)
	}
	t.Cleanup(func() { bestEffortDelete(r, "widgets", widgetID) })
	res = mustExitZero(t, r, "widgets", "query", widgetID, "--since", "1h", "-o", "json")
	var byDef map[string]json.RawMessage
	if err := json.Unmarshal([]byte(res.Stdout), &byDef); err != nil || byDef[id] == nil {
		t.Fatalf("widgets query -o json should key the definition %s: %v\n%s", id, err, res.Stdout)
	}

	// Deleting the widget leaves the definition.
	mustExitZero(t, r, "widgets", "delete", widgetID, "--yes")
	got := mustRunJSONObject(t, r, "metrics", "definitions", "get", id)
	if got["id"] != id {
		t.Fatalf("definition after widget delete = %+v", got)
	}

	// Deleted: the API's 500 becomes resource_not_found, exit 4.
	mustExitZero(t, r, "metrics", "definitions", "delete", id, "--yes")
	res, err := r.Run(t.Context(), "", "metrics", "definitions", "get", id)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 4 || ParseErrorEnvelope(t, res.Stderr).Error.Code != "resource_not_found" {
		t.Fatalf("get after delete: exit %d, stderr %s", res.ExitCode, res.Stderr)
	}
}

// seriesTotals maps each series in a /timeseries response to its range
// value: the last aggregate, which is the one the query's stat names.
func seriesTotals(stdout string) (map[string]float64, error) {
	var resp map[string]struct {
		Aggregates []struct {
			Value float64 `json:"value"`
		} `json:"aggregates"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		return nil, fmt.Errorf("query -o json: %w\n%s", err, stdout)
	}
	out := map[string]float64{}
	for name, s := range resp {
		if n := len(s.Aggregates); n > 0 {
			out[name] = s.Aggregates[n-1].Value
		}
	}
	return out, nil
}
