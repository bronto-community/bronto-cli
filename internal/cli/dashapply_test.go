package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testManifest = `name: Checkout API health
description: Requests, latency and errors
tags: {team: checkout}
widgets:
  - name: Request rate
    type: line
    queries:
      - name: requests
        dataset: .traces/checkout-api
        select: count(*)
        where: $span.kind = 'SPAN_KIND_SERVER'
  - name: p95 latency
    unit: nanoseconds
    queries:
      - {name: p95, dataset: checkout-api, select: $span.duration_nano, agg: p95,
         where: "$span.kind = 'SPAN_KIND_SERVER'", group_by: [$span.name]}
  - name: Error rate %
    queries:
      - {name: errors, dataset: .traces/checkout-api, select: count(*), where: "$span.status_code = 'STATUS_CODE_ERROR'"}
      - {name: total, dataset: .traces/checkout-api, select: count(*)}
    formula: errors / total * 100
`

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dashboard.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustParse(t *testing.T, src string) *manifest {
	t.Helper()
	m, err := parseManifest("d.yaml", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m
}

func TestParseManifestErrors(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"no name", "widgets: []", "d.yaml:1:1: the dashboard needs a name"},
		{"unknown top-level key", "name: x\nwidget: []", `d.yaml:2:1: unknown field "widget" in the dashboard`},
		{"not yaml", "name: [x", "not valid YAML"},
		{"two documents", "name: a\n---\nname: b", "d.yaml:3: a manifest holds one dashboard"},
		{"empty", "", "the file is empty"},
		{"unknown query key", "name: x\nwidgets:\n  - name: w\n    queries:\n      - {dataset: a/b, select: count(*), wher: x}",
			`d.yaml:5:42: unknown field "wher" in a query`},
		{"no dataset", "name: x\nwidgets:\n  - name: w\n    queries:\n      - {select: count(*)}",
			"d.yaml:5:9: a query needs a dataset (or from_expr, or from)"},
		{"two sources", "name: x\nwidgets:\n  - name: w\n    queries:\n      - {dataset: a/b, from_expr: y, select: count(*)}",
			"use one of dataset, from_expr or from"},
		{"bare field without agg", "name: x\nwidgets:\n  - name: w\n    queries:\n      - {dataset: a/b, select: $span.duration_nano}",
			`select "$span.duration_nano" is a field, so set agg`},
		{"unknown agg", "name: x\nwidgets:\n  - name: w\n    queries:\n      - {dataset: a/b, select: x, agg: p42}",
			`unknown agg "p42"`},
		{"unknown unit", "name: x\nwidgets:\n  - name: w\n    unit: hours\n    queries:\n      - {dataset: a/b, select: count(*)}",
			`d.yaml:4:11: unknown unit "hours"`},
		{"unknown type", "name: x\nwidgets:\n  - name: w\n    type: lines\n    queries:\n      - {dataset: a/b, select: count(*)}",
			`d.yaml:3:5: widget "w": unknown type "lines"`},
		{"duplicate widget", "name: x\nwidgets:\n  - {name: w, queries: [{dataset: a/b, select: count(*)}]}\n  - {name: w, queries: [{dataset: a/b, select: count(*)}]}",
			`d.yaml:4:5: two widgets are named "w"`},
		{"formula refers to missing query", "name: x\nwidgets:\n  - name: w\n    queries:\n      - {name: a, dataset: a/b, select: count(*)}\n    formula: a / b",
			`d.yaml:6:14: widget "w": formula refers to "b"`},
		{"empty formula", "name: x\nwidgets:\n  - name: w\n    queries:\n      - {name: a, dataset: a/b, select: count(*)}\n    formula: \"\"",
			`d.yaml:6:14: widget "w": the formula is empty`},
		{"no queries", "name: x\nwidgets:\n  - {name: w}", `widget "w" needs at least one query`},
		{"too many queries", "name: x\nwidgets:\n  - name: w\n    queries: [" + strings.Repeat("{dataset: a/b, select: count(*)},", 7) + "]",
			"has 7 queries; a widget takes at most 6"},
		{"layout names unknown widget", "name: x\nwidgets:\n  - {name: w, queries: [{dataset: a/b, select: count(*)}]}\nlayout:\n  - {widget: v, x: 0, y: 0, w: 6, h: 4}",
			`d.yaml:5:5: layout names the widget "v"`},
		{"managed tag", "name: x\ntags: {managed-by: me}\nwidgets: []", `the tag "managed-by" is set by bronto dashboards apply`},
		{"space in query name with formula", "name: x\nwidgets:\n  - name: w\n    queries:\n      - {name: my q, dataset: a/b, select: count(*)}\n    formula: x",
			`query name "my q" has a space`},
		{"ref with extra key", "name: x\nwidgets:\n  - {ref: Shared, type: line}", `unknown field "type" in a ref widget`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseManifest("d.yaml", []byte(c.src))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v\nwant it to contain %q", err, c.want)
			}
		})
	}
}

// Every problem in a file is reported, in file order.
func TestParseManifestReportsAllErrorsInOrder(t *testing.T) {
	_, err := parseManifest("d.yaml", []byte("name: x\nwidgets:\n  - name: w\n    type: nope\n    queries:\n      - {dataset: a/b, select: f}\n  - {ref: ''}\n"))
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	i, j, k := strings.Index(msg, ":3:5:"), strings.Index(msg, ":6:9:"), strings.Index(msg, ":7:5:")
	if i < 0 || j < 0 || k < 0 || i >= j || j >= k {
		t.Fatalf("want three errors in line order:\n%s", msg)
	}
}

func TestResolveDefaults(t *testing.T) {
	m := mustParse(t, testManifest)
	ds := []datasetInfo{{name: "checkout-api", collection: ".traces", id: "aaaaaaaa-aaaa-aaaa-aaaa-00000000d501"}}
	want, err := m.resolve(lookupIn(ds))
	if err != nil {
		t.Fatal(err)
	}
	req := want.Widgets[0].Def.Queries[0]
	wantFrom := `"collection" = '.traces' AND "dataset" = 'checkout-api'`
	if req.FromExpr != wantFrom || req.Aggregation[0].Time != "count" || req.Aggregation[0].ReduceTo != "avg" ||
		req.Select[0] != "count(*)" || req.Where != "$span.kind = 'SPAN_KIND_SERVER'" || req.Groups == nil {
		t.Fatalf("request query = %+v", req)
	}
	p95 := want.Widgets[1].Def.Queries[0]
	if p95.Aggregation[0].Time != "p95" || p95.FromExpr != wantFrom || p95.UnitConfig["input"] != "nanoseconds" ||
		p95.UnitConfig["type"] != "time" || p95.UnitConfig["output"] != "auto" || p95.Groups[0] != "$span.name" {
		t.Fatalf("p95 query = %+v", p95)
	}
	errs := want.Widgets[2].Def
	if len(errs.Formulas) != 1 || errs.Formulas[0].Name != "error_rate" || errs.Formulas[0].Expression != "errors / total * 100" ||
		errs.Formulas[0].Aggregation.ReduceTo != "avg" || errs.Queries[1].Where != "" {
		t.Fatalf("error rate = %+v", errs)
	}
	if *want.Description != "Requests, latency and errors" || want.Tags["team"] != "checkout" || want.Layout != nil {
		t.Fatalf("dashboard = %+v", want)
	}
}

func TestDefaultAgg(t *testing.T) {
	for sel, want := range map[string]string{
		"count(*)": "count", "p95($span.duration_nano)": "p95", "AVERAGE(x)": "avg", "SUM(bytes)": "sum",
		"$span.duration_nano": "", "frobnicate(x)": "",
	} {
		if got := defaultAgg(sel); got != want {
			t.Errorf("defaultAgg(%q) = %q, want %q", sel, got, want)
		}
	}
}

func TestFromExprRoundTrip(t *testing.T) {
	one := []datasetInfo{{collection: ".traces", name: "checkout-api"}}
	two := []datasetInfo{{collection: ".traces", name: "a"}, {collection: "prod", name: "b"}}
	e1, _ := fromExprFor(one)
	e2, _ := fromExprFor(two)
	if e1 != `"collection" = '.traces' AND "dataset" = 'checkout-api'` {
		t.Fatalf("one = %s", e1)
	}
	if e2 != `("collection" = '.traces' AND "dataset" = 'a') OR ("collection" = 'prod' AND "dataset" = 'b')` {
		t.Fatalf("two = %s", e2)
	}
	if refs, ok := parseFromExpr(e2); !ok || strings.Join(refs, ",") != ".traces/a,prod/b" {
		t.Fatalf("parse two = %v %v", refs, ok)
	}
	for _, other := range []string{`"collection" = '.traces'`, `metric_name IN ('x')`,
		`"collection" = 'a' AND "dataset" = 'b' OR x`, `("collection" = 'a' AND "dataset" = 'b')`} {
		if _, ok := parseFromExpr(other); ok {
			t.Errorf("parseFromExpr(%q) should not match", other)
		}
	}
	if _, err := fromExprFor([]datasetInfo{{collection: "c", name: "it's"}}); err == nil {
		t.Error("a quote in a dataset name should be refused")
	}
}

func TestDatasetLookup(t *testing.T) {
	ds := []datasetInfo{
		{name: "checkout-api", collection: ".traces", id: "aaaaaaaa-aaaa-aaaa-aaaa-00000000d501"},
		{name: "payments", collection: ".traces", id: "aaaaaaaa-aaaa-aaaa-aaaa-00000000d502"},
		{name: "payments", collection: "prod", id: "aaaaaaaa-aaaa-aaaa-aaaa-00000000d503"},
	}
	look := lookupIn(ds)
	for ref, want := range map[string]string{
		"checkout-api": ".traces/checkout-api", ".traces/payments": ".traces/payments",
		"aaaaaaaa-aaaa-aaaa-aaaa-00000000d503": "prod/payments",
	} {
		d, err := look(ref)
		if err != nil || d.qualified() != want {
			t.Errorf("lookup(%q) = %v, %v; want %s", ref, d.qualified(), err, want)
		}
	}
	if _, err := look("payments"); err == nil || !strings.Contains(err.Error(), ".traces/payments, prod/payments") {
		t.Errorf("ambiguous name: %v", err)
	}
	if _, err := look("nope"); err == nil {
		t.Error("missing dataset should fail")
	}
	// Several datasets OR-join into one from_expr.
	m := mustParse(t, "name: x\nwidgets:\n  - name: w\n    queries:\n      - {dataset: [checkout-api, prod/payments], select: count(*)}")
	want, err := m.resolve(look)
	if err != nil {
		t.Fatal(err)
	}
	if got := want.Widgets[0].Def.Queries[0].FromExpr; got != `("collection" = '.traces' AND "dataset" = 'checkout-api') OR ("collection" = 'prod' AND "dataset" = 'payments')` {
		t.Fatalf("from_expr = %s", got)
	}
}

// --- plan ---------------------------------------------------------------

const (
	pDash  = "dddddddd-0000-0000-0000-000000000001"
	pW1    = "dddddddd-0000-0000-0000-0000000000a1"
	pW2    = "dddddddd-0000-0000-0000-0000000000a2"
	pShare = "dddddddd-0000-0000-0000-0000000000a3"
	pD1    = "dddddddd-0000-0000-0000-0000000000b1"
	pD2    = "dddddddd-0000-0000-0000-0000000000b2"
)

func planFixture(t *testing.T, src string) *wantDashboard {
	t.Helper()
	m := mustParse(t, src)
	want, err := m.resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return want
}

const planManifestSrc = `name: D
widgets:
  - name: Requests
    queries: [{name: r, from_expr: x, select: count(*)}]
  - name: Errors
    queries: [{name: e, from_expr: x, select: count(*), where: "s = 'E'"}]
`

func ownedTags() map[string]string {
	return map[string]string{tagManagedBy: tagManagedByValue, tagOwner: pDash}
}

// liveFor is the live state an earlier apply of planManifestSrc left.
func liveFor(t *testing.T) *liveState {
	want := planFixture(t, planManifestSrc)
	d1, d2 := want.Widgets[0].Def, want.Widgets[1].Def
	d1.ID, d2.ID = pD1, pD2
	return &liveState{
		Dashboard: &liveDashboard{ID: pDash, Name: "D", Tags: map[string]string{tagManagedBy: tagManagedByValue},
			WidgetIDs: []string{pW1, pW2}},
		Widgets: []liveWidget{
			{ID: pW1, Name: "Requests", Type: "line", MetricIDs: []string{pD1}, Tags: ownedTags(), Aux: map[string]any{}},
			{ID: pW2, Name: "Errors", Type: "line", MetricIDs: []string{pD2}, Tags: ownedTags()},
			{ID: pShare, Name: "Shared", Type: "score", MetricIDs: []string{"dddddddd-0000-0000-0000-0000000000b9"}},
		},
		Defs: map[string]apiDef{pD1: d1, pD2: d2},
	}
}

func actions(p *dashPlan) string {
	var out []string
	for _, w := range p.Widgets {
		out = append(out, w.Name+":"+w.Action)
	}
	return strings.Join(out, " ")
}

func TestComputePlan(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		live     func(*testing.T) *liveState
		actions  string
		writes   int
		check    func(*testing.T, *dashPlan)
	}{
		{
			name: "create from scratch", manifest: planManifestSrc,
			live:    func(*testing.T) *liveState { return &liveState{Defs: map[string]apiDef{}} },
			actions: "Requests:create Errors:create",
			// dashboard + managed-by tag, then 5 per widget
			writes: 2 + 10,
			check: func(t *testing.T, p *dashPlan) {
				if p.Dashboard.Action != actCreate || len(p.Dashboard.Tags) != 1 || p.Dashboard.Tags[0].Name != tagManagedBy {
					t.Errorf("dashboard = %+v", p.Dashboard)
				}
			},
		},
		{
			name: "no-op", manifest: planManifestSrc, live: liveFor,
			actions: "Requests:unchanged Errors:unchanged", writes: 0,
		},
		{
			name:     "change query: new definition, repoint, delete old",
			manifest: strings.Replace(planManifestSrc, `where: "s = 'E'"`, `where: "s = 'ERROR'"`, 1),
			live:     liveFor, actions: "Requests:unchanged Errors:update", writes: 3,
			check: func(t *testing.T, p *dashPlan) {
				ch := p.Widgets[1]
				if !ch.NewDefinition || !ch.UpdateWidget || len(ch.DeleteDefinitions) != 1 || ch.DeleteDefinitions[0] != pD2 {
					t.Errorf("errors change = %+v", ch)
				}
				if !strings.Contains(strings.Join(ch.Details, "\n"), `query e: where: "s = 'E'" -> "s = 'ERROR'"`) {
					t.Errorf("details = %v", ch.Details)
				}
			},
		},
		{
			name:     "remove widget: delete widget and its definition",
			manifest: "name: D\nwidgets:\n  - name: Requests\n    queries: [{name: r, from_expr: x, select: count(*)}]\n",
			live:     liveFor, actions: "Requests:unchanged Errors:delete", writes: 2,
			check: func(t *testing.T, p *dashPlan) {
				if d := p.Widgets[1].DeleteDefinitions; len(d) != 1 || d[0] != pD2 {
					t.Errorf("delete definitions = %v", d)
				}
			},
		},
		{
			name:     "ref widget: attach, never owned",
			manifest: planManifestSrc + "  - ref: Shared\n",
			live:     liveFor, actions: "Requests:unchanged Errors:unchanged Shared:attach", writes: 1,
			check: func(t *testing.T, p *dashPlan) {
				if w := p.Widgets[2]; !w.Ref || w.Adopt || w.NewDefinition {
					t.Errorf("ref change = %+v", w)
				}
			},
		},
		{
			name: "ref widget removed from the file: detach only", manifest: planManifestSrc,
			live: func(t *testing.T) *liveState {
				l := liveFor(t)
				l.Dashboard.WidgetIDs = append(l.Dashboard.WidgetIDs, pShare)
				return l
			},
			actions: "Requests:unchanged Errors:unchanged Shared:detach", writes: 1,
			check: func(t *testing.T, p *dashPlan) {
				if d := p.Widgets[2]; len(d.DeleteDefinitions) != 0 {
					t.Errorf("a detach must not delete definitions: %+v", d)
				}
			},
		},
		{
			name: "unowned attached widget with the manifest's name is adopted", manifest: planManifestSrc,
			live: func(t *testing.T) *liveState {
				l := liveFor(t)
				l.Widgets[1].Tags = nil // created in the web UI
				return l
			},
			actions: "Requests:unchanged Errors:update", writes: 2,
			check: func(t *testing.T, p *dashPlan) {
				if ch := p.Widgets[1]; !ch.Adopt || ch.NewDefinition || ch.UpdateWidget {
					t.Errorf("adopt = %+v", ch)
				}
			},
		},
		{
			name: "a widget owned by another dashboard isn't adopted", manifest: planManifestSrc,
			live: func(t *testing.T) *liveState {
				l := liveFor(t)
				l.Widgets[1].Tags = map[string]string{tagManagedBy: tagManagedByValue, tagOwner: "other"}
				return l
			},
			actions: "Requests:unchanged Errors:create Errors:detach", writes: 6,
		},
		{
			name:     "a definition another widget also plots isn't deleted",
			manifest: "name: D\nwidgets:\n  - name: Requests\n    queries: [{name: r, from_expr: x, select: count(*)}]\n",
			live: func(t *testing.T) *liveState {
				l := liveFor(t)
				l.Widgets[2].MetricIDs = []string{pD2}
				return l
			},
			actions: "Requests:unchanged Errors:delete", writes: 1,
			check: func(t *testing.T, p *dashPlan) {
				if len(p.Widgets[1].DeleteDefinitions) != 0 {
					t.Errorf("shared definition scheduled for delete: %+v", p.Widgets[1])
				}
			},
		},
		{
			name: "an owned widget that was detached is re-attached", manifest: planManifestSrc,
			live: func(t *testing.T) *liveState {
				l := liveFor(t)
				l.Dashboard.WidgetIDs = []string{pW1}
				return l
			},
			actions: "Requests:unchanged Errors:update", writes: 1,
		},
		{
			name: "an orphaned definition from an interrupted run is deleted", manifest: planManifestSrc,
			live: func(t *testing.T) *liveState {
				l := liveFor(t)
				l.Defs["dddddddd-0000-0000-0000-0000000000bf"] = apiDef{ID: "x", Description: definitionMarker(pDash)}
				l.Defs["dddddddd-0000-0000-0000-0000000000be"] = apiDef{ID: "y", Description: "someone else's"}
				return l
			},
			actions: "Requests:unchanged Errors:unchanged", writes: 1,
			check: func(t *testing.T, p *dashPlan) {
				if len(p.Orphans) != 1 || p.Orphans[0] != "dddddddd-0000-0000-0000-0000000000bf" {
					t.Errorf("orphans = %v", p.Orphans)
				}
			},
		},
		{
			name: "tags: authoritative when present", manifest: "name: D\ntags: {team: a}\n" + strings.TrimPrefix(planManifestSrc, "name: D\n"),
			live: func(t *testing.T) *liveState {
				l := liveFor(t)
				l.Dashboard.Tags = map[string]string{tagManagedBy: tagManagedByValue, "team": "b", "old": "x"}
				return l
			},
			actions: "Requests:unchanged Errors:unchanged", writes: 2,
			check: func(t *testing.T, p *dashPlan) {
				got := compactJSON(p.Dashboard.Tags)
				if got != `[{"name":"team","value":"a","op":"update"},{"name":"old","op":"delete"}]` {
					t.Errorf("tags = %s", got)
				}
			},
		},
		{
			name:     "description change is a warning, not a write",
			manifest: "name: D\ndescription: new\n" + strings.TrimPrefix(planManifestSrc, "name: D\n"),
			live:     liveFor, actions: "Requests:unchanged Errors:unchanged", writes: 0,
			check: func(t *testing.T, p *dashPlan) {
				if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "can't change a dashboard's description") {
					t.Errorf("warnings = %v", p.Warnings)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := computePlan(planFixture(t, c.manifest), c.live(t))
			if err != nil {
				t.Fatal(err)
			}
			if got := actions(p); got != c.actions {
				t.Errorf("actions = %s\nwant      %s", got, c.actions)
			}
			if p.Writes != c.writes {
				t.Errorf("writes = %d, want %d", p.Writes, c.writes)
			}
			if c.check != nil {
				c.check(t, p)
			}
		})
	}
}

func TestComputePlanConflicts(t *testing.T) {
	two := func(t *testing.T) *liveState {
		l := liveFor(t)
		l.Widgets = append(l.Widgets, liveWidget{ID: "dddddddd-0000-0000-0000-0000000000a9", Name: "Shared"})
		return l
	}
	if _, err := computePlan(planFixture(t, planManifestSrc+"  - ref: Shared\n"), two(t)); err == nil ||
		!strings.Contains(err.Error(), `2 widgets are named "Shared"`) {
		t.Fatalf("ambiguous ref: %v", err)
	}
	if _, err := computePlan(planFixture(t, planManifestSrc+"  - ref: Nope\n"), liveFor(t)); err == nil ||
		!strings.Contains(err.Error(), `no widget named "Nope"`) {
		t.Fatalf("missing ref: %v", err)
	}
	dup := liveFor(t)
	dup.Widgets = append(dup.Widgets, liveWidget{ID: "dddddddd-0000-0000-0000-0000000000a8", Name: "Errors", Tags: ownedTags()})
	if _, err := computePlan(planFixture(t, planManifestSrc), dup); err == nil ||
		!strings.Contains(err.Error(), `2 widgets named "Errors" belong to this dashboard`) {
		t.Fatalf("duplicate owned: %v", err)
	}
}

// --- end to end against the fake org --------------------------------------

func applyJSON(t *testing.T, f *fakeOrg, path string, extra ...string) map[string]any {
	t.Helper()
	args := append([]string{"dashboards", "apply", "-f", path, "-o", "json"}, extra...)
	out, stderr, err := runResource(t, f.handler, "", args...)
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("apply -o json: %v\n%s", err, out)
	}
	return doc
}

func TestApplyEndToEnd(t *testing.T) {
	f := newFakeOrg(t)
	path := writeManifest(t, testManifest)

	// Dry run first: nothing written.
	doc := applyJSON(t, f, path, "--dry-run")
	if f.writeCount() != 0 || doc["dry_run"] != true || doc["writes"].(float64) != 2+1+15 {
		t.Fatalf("dry run: %d writes made, plan %v", f.writeCount(), doc["writes"])
	}

	doc = applyJSON(t, f, path)
	// dashboard, managed-by + team tags, 3 widgets x 5
	if got := f.writeCount(); got != 18 || doc["writes"].(float64) != 18 {
		t.Fatalf("first apply: %d writes (%v): %v", got, doc["writes"], f.writes)
	}
	dash := f.dashboardByName("Checkout API health")
	if dash == nil || len(strs(dash["widget_ids"])) != 3 || dash["tags"].(map[string]any)["team"] != "checkout" ||
		dash["description"] != "Requests, latency and errors" {
		t.Fatalf("dashboard = %v", dash)
	}
	dashID := dash["id"].(string)
	errW := f.widgetByName("Error rate %")
	tags := errW["tags"].(map[string]any)
	if tags[tagManagedBy] != tagManagedByValue || tags[tagOwner] != dashID {
		t.Fatalf("widget tags = %v", tags)
	}
	def := f.defs[strs(errW["metric_ids"])[0]]
	if def["description"] != definitionMarker(dashID) || len(def["formulas"].([]any)) != 1 {
		t.Fatalf("definition = %v", def)
	}

	// Re-apply: zero writes.
	f.resetWrites()
	applyJSON(t, f, path)
	if f.writeCount() != 0 {
		t.Fatalf("re-apply wrote: %v", f.writes)
	}

	// Change one query: a new definition, the widget repointed, the old
	// definition deleted, in that order.
	p95 := f.widgetByName("p95 latency")
	oldDef := strs(p95["metric_ids"])[0]
	f.resetWrites()
	changed := writeManifest(t, strings.Replace(testManifest, "agg: p95", "agg: p99", 1))
	applyJSON(t, f, changed)
	if got := strings.Join(f.writes, ", "); got != "POST /metrics/definitions, PUT /widgets/"+p95["id"].(string)+", DELETE /metrics/definitions/"+oldDef {
		t.Fatalf("change query writes = %s", got)
	}
	if _, still := f.defs[oldDef]; still {
		t.Fatal("old definition not deleted")
	}
	newDef := strs(f.widgetByName("p95 latency")["metric_ids"])[0]
	if newDef == oldDef || f.defs[newDef]["queries"].([]any)[0].(map[string]any)["aggregation"].([]any)[0].(map[string]any)["time"] != "p99" {
		t.Fatalf("widget not repointed to a p99 definition: %v", f.defs[newDef])
	}

	// Remove a widget: it and its definition go.
	errDef := strs(errW["metric_ids"])[0]
	f.resetWrites()
	removed := writeManifest(t, strings.SplitN(strings.Replace(testManifest, "agg: p95", "agg: p99", 1), "  - name: Error rate %", 2)[0])
	applyJSON(t, f, removed)
	if f.widgetByName("Error rate %") != nil || f.defs[errDef] != nil || f.writeCount() != 2 {
		t.Fatalf("remove widget: writes %v", f.writes)
	}
	if len(strs(f.dashboardByName("Checkout API health")["widget_ids"])) != 2 {
		t.Fatal("removed widget still attached")
	}

	// Export, then apply the export: no writes.
	out, stderr, err := runResource(t, f.handler, "", "dashboards", "export", "Checkout API health")
	if err != nil {
		t.Fatalf("export: %v\n%s", err, stderr)
	}
	if strings.Contains(stderr, "Note:") {
		t.Errorf("a managed dashboard's export shouldn't print notes: %s", stderr)
	}
	for _, want := range []string{"name: Checkout API health", "dataset: .traces/checkout-api", "unit: nanoseconds",
		"agg: p99", "team: checkout", "group_by:"} {
		if !strings.Contains(out, want) {
			t.Errorf("export missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "agg: count") || strings.Contains(out, "reduce_to") || strings.Contains(out, "layout") {
		t.Errorf("export should leave defaults and the empty layout out:\n%s", out)
	}
	f.resetWrites()
	applyJSON(t, f, writeManifest(t, out))
	if f.writeCount() != 0 {
		t.Fatalf("export -> apply wrote: %v\n%s", f.writes, out)
	}
}

// A dashboard built elsewhere: export writes every widget inline with a
// note, and applying the export adopts them (tags only), after which a
// re-apply is a no-op.
func TestExportUnmanagedThenApplyAdopts(t *testing.T) {
	f := newFakeOrg(t)
	f.defs["aaaaaaaa-aaaa-aaaa-aaaa-0000000000f1"] = map[string]any{"id": "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f1", "name": "x",
		"queries": []any{map[string]any{"name": "errors", "select": []any{"count(*)"}, "where": "",
			"from_expr": `"collection" = '.traces' AND "dataset" = 'payments'`, "groups": []any{"$service.name"},
			"aggregation": []any{map[string]any{"time": "count", "reduce_to": "sum"}}},
			map[string]any{"name": "total", "select": []any{"count(*)"}, "where": "", "from_expr": `"collection" = '.traces'`,
				"aggregation": []any{map[string]any{"time": "count", "reduce_to": "avg"}}}},
		"formulas": []any{map[string]any{"name": "pct", "expression": "errors / total * 100", "aggregation": map[string]any{"reduce_to": "avg"}}}}
	f.widgets["aaaaaaaa-aaaa-aaaa-aaaa-0000000000f2"] = map[string]any{"id": "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f2", "name": "UI widget",
		"type": "bar", "description": "made by hand", "aux": map[string]any{},
		"metric_ids": []any{"aaaaaaaa-aaaa-aaaa-aaaa-0000000000f1"}, "widget_ids": []any{}, "tags": map[string]any{}}
	f.widgets["aaaaaaaa-aaaa-aaaa-aaaa-0000000000f3"] = map[string]any{"id": "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f3", "name": "Group",
		"type": "group", "metric_ids": []any{}, "widget_ids": []any{"aaaaaaaa-aaaa-aaaa-aaaa-0000000000f2"}, "tags": map[string]any{}}
	f.dashboards["aaaaaaaa-aaaa-aaaa-aaaa-0000000000f4"] = map[string]any{"id": "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f4", "name": "UI dash",
		"description": "", "tags": map[string]any{"env": "prod"},
		"widget_ids": []any{"aaaaaaaa-aaaa-aaaa-aaaa-0000000000f2", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f3"},
		"layout":     map[string]any{"widget_layouts": []any{map[string]any{"id": "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f2", "x": 0, "y": 0, "w": 6, "h": 4}}}}

	out, stderr, err := runResource(t, f.handler, "", "dashboards", "export", "UI dash")
	if err != nil {
		t.Fatalf("export: %v\n%s", err, stderr)
	}
	for _, want := range []string{"ref: Group", "name: UI widget", "type: bar", "reduce_to: sum", `from_expr: '"collection" = ''.traces'''`,
		"dataset: .traces/payments", "formulas:", "name: pct", "layout:", "widget: UI widget", "env: prod"} {
		if !strings.Contains(out, want) {
			t.Errorf("export missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(stderr, `widget "Group" is exported as ref: it is a group widget`) ||
		!strings.Contains(stderr, `1 widget weren't created by bronto dashboards apply ("UI widget")`) {
		t.Errorf("notes:\n%s", stderr)
	}

	path := writeManifest(t, out)
	f.resetWrites()
	applyJSON(t, f, path)
	// managed-by on the dashboard, two ownership tags on the widget.
	if got := strings.Join(f.writes, ", "); got != "POST /tags, POST /tags, POST /tags" {
		t.Fatalf("adopt writes = %s", got)
	}
	f.resetWrites()
	applyJSON(t, f, path)
	if f.writeCount() != 0 {
		t.Fatalf("second apply wrote: %v", f.writes)
	}
}

func TestApplyAmbiguousDashboard(t *testing.T) {
	f := newFakeOrg(t)
	for _, id := range []string{"aaaaaaaa-aaaa-aaaa-aaaa-0000000000c1", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000c2"} {
		f.dashboards[id] = map[string]any{"id": id, "name": "Checkout API health", "tags": map[string]any{}}
	}
	_, stderr, err := runResource(t, f.handler, "", "dashboards", "apply", "-f", writeManifest(t, testManifest))
	if err == nil || !strings.Contains(err.Error(), `2 dashboards are named "Checkout API health"`) ||
		!strings.Contains(err.Error(), "aaaaaaaa-aaaa-aaaa-aaaa-0000000000c1") {
		t.Fatalf("err = %v\n%s", err, stderr)
	}
	if f.writeCount() != 0 {
		t.Fatalf("wrote: %v", f.writes)
	}
}

func TestApplyHumanOutput(t *testing.T) {
	f := newFakeOrg(t)
	out, stderr, err := runResource(t, f.handler, "", "dashboards", "apply", "-f", writeManifest(t, testManifest), "--dry-run", "-o", "table")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`dashboard "Checkout API health": create`, `+ widget "Request rate": create`,
		"line, 1 query", "2 queries, 1 formula", "tag team: add checkout"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(stderr, "Dry run: apply would make 18 API writes; nothing was changed.") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestApplyDirectoryAndStdin(t *testing.T) {
	f := newFakeOrg(t)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("name: A\nwidgets: []\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "b.yml"), []byte("name: B\nwidgets: []\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o600)
	if _, stderr, err := runResource(t, f.handler, "", "dashboards", "apply", "-f", dir, "-o", "json"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if f.dashboardByName("A") == nil || f.dashboardByName("B") == nil {
		t.Fatal("directory apply missed a file")
	}
	if _, stderr, err := runResource(t, f.handler, "name: C\nwidgets: []\n", "dashboards", "apply", "-f", "-", "-o", "json"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if f.dashboardByName("C") == nil {
		t.Fatal("stdin apply")
	}
	// The same dashboard in two files is refused before any request.
	_ = os.WriteFile(filepath.Join(dir, "c.yaml"), []byte("name: A\nwidgets: []\n"), 0o600)
	f.resetWrites()
	if _, _, err := runResource(t, f.handler, "", "dashboards", "apply", "-f", dir); err == nil ||
		!strings.Contains(err.Error(), `the dashboard "A" is also declared in`) {
		t.Fatalf("duplicate dashboard: %v", err)
	}
}

func TestValidate(t *testing.T) {
	f := newFakeOrg(t)
	path := writeManifest(t, testManifest)
	_, stderr, err := runResource(t, f.handler, "", "dashboards", "validate", "-f", path)
	if err != nil || !strings.Contains(stderr, `✓ `+path+`: dashboard "Checkout API health", 3 widgets`) {
		t.Fatalf("offline: %v\n%s", err, stderr)
	}
	if len(f.writes) != 0 {
		t.Fatal("offline validate made requests")
	}

	_, stderr, err = runResource(t, f.handler, "", "dashboards", "validate", "-f", path, "--live")
	if err != nil || !strings.Contains(stderr, `✓ widget "Request rate": 1 series`) || !strings.Contains(stderr, "0 problems, 0 warnings") {
		t.Fatalf("live: %v\n%s", err, stderr)
	}
	// validate --live saves nothing: its searches are the only POSTs.
	for _, w := range f.writes {
		if w != "POST /timeseries/search" {
			t.Fatalf("validate --live wrote %s", w)
		}
	}

	f.series = 0
	_, stderr, err = runResource(t, f.handler, "", "dashboards", "validate", "-f", path, "--live")
	if err != nil || !strings.Contains(stderr, "every series is zero over 24h") || !strings.Contains(stderr, "3 warnings") {
		t.Fatalf("zero: %v\n%s", err, stderr)
	}

	f.searchErr = true
	_, stderr, err = runResource(t, f.handler, "", "dashboards", "validate", "-f", path, "--live")
	if err == nil || !strings.Contains(err.Error(), "problems in the dashboard manifests") || !strings.Contains(stderr, "the API rejected its queries") {
		t.Fatalf("rejected: %v\n%s", err, stderr)
	}

	bad := writeManifest(t, "name: x\nwidgets:\n  - {name: w, queries: [{dataset: nope/nope, select: count(*)}]}\n")
	f.searchErr = false
	_, stderr, err = runResource(t, f.handler, "", "dashboards", "validate", "-f", bad, "--live")
	if err == nil || !strings.Contains(stderr, `no dataset "nope/nope"`) {
		t.Fatalf("missing dataset: %v\n%s", err, stderr)
	}
}

func TestExportOutputFormats(t *testing.T) {
	f := newFakeOrg(t)
	applyJSON(t, f, writeManifest(t, testManifest))
	out, _, err := runResource(t, f.handler, "", "dashboards", "export", "Checkout API health", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil || m["name"] != "Checkout API health" {
		t.Fatalf("json export: %v\n%s", err, out)
	}
	// The JSON export applies too (JSON is YAML).
	f.resetWrites()
	applyJSON(t, f, writeManifest(t, out))
	if f.writeCount() != 0 {
		t.Fatalf("json export -> apply wrote %v", f.writes)
	}
	if _, _, err := runResource(t, f.handler, "", "dashboards", "export", "Checkout API health", "-o", "csv"); err == nil {
		t.Fatal("export -o csv should fail")
	}
}
