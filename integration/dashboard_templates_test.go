package integration

import (
	"slices"
	"strings"
	"testing"
)

// TestDashboardTemplatesLive covers the template catalog end to end (#197):
// list, show through the preview endpoint, create a dashboard from a
// parameterized template, and clean up. The created dashboard takes the
// template's name (the API has no name override), so everything it
// creates is tracked by id. Deleting the dashboard doesn't delete the
// widgets and metric definitions Bronto copied for it (live-verified
// 2026-10-21), so the test deletes those explicitly; the widget deletes
// failing would mean the API now cascades and the docs need updating.
//
// About 20 requests: the template with the fewest widgets keeps the
// per-widget cleanup small.
func TestDashboardTemplatesLive(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)

	rows := mustRunJSONArray(t, r, "dashboards", "templates", "list", "-o", "json")
	if len(rows) == 0 {
		t.Fatal("dashboards templates list returned no templates")
	}
	var tmpl map[string]any
	for _, row := range rows {
		params, _ := row["parameters"].(map[string]any)
		widgets, _ := row["widgets"].([]any)
		if len(params) == 0 || len(widgets) == 0 {
			continue
		}
		if tmpl == nil || len(widgets) < len(tmpl["widgets"].([]any)) {
			tmpl = row
		}
	}
	if tmpl == nil {
		t.Skip("no parameterized dashboard template in this account")
	}
	tmplID, _ := tmpl["id"].(string)
	tmplName, _ := tmpl["name"].(string)
	var paramKeys []string
	for k := range tmpl["parameters"].(map[string]any) {
		paramKeys = append(paramKeys, k)
	}
	slices.Sort(paramKeys)
	var paramArgs []string
	const value = `"collection" = 'cli-test-templates'`
	for _, k := range paramKeys {
		paramArgs = append(paramArgs, "--param", k+"="+value)
	}

	// show by name renders through the preview, which saves nothing.
	shown := mustRunJSONObject(t, r, append([]string{"dashboards", "templates", "show", tmplName, "-o", "json"}, paramArgs...)...)
	if shown["id"] != tmplID {
		t.Fatalf("templates show id = %v, want the template id %s", shown["id"], tmplID)
	}
	if params, _ := shown["parameters"].(map[string]any); len(params) != len(paramKeys) {
		t.Fatalf("templates show parameters = %v, want %v", shown["parameters"], paramKeys)
	}
	if ws, _ := shown["widgets"].([]any); len(ws) == 0 {
		t.Fatalf("templates show rendered no widgets: %v", shown)
	}

	// An undeclared key is a usage error before any request.
	res, err := r.Run(t.Context(), "", "dashboards", "create", "--template", tmplID, "--param", "cli-test-bogus=x")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 2 || !strings.Contains(res.Stderr, "usage_unknown_param") {
		t.Fatalf("create with an unknown --param: exit %d, want 2\nstderr: %s", res.ExitCode, res.Stderr)
	}

	created := mustRunJSONObject(t, r, append([]string{"dashboards", "create", "--template", tmplID, "-o", "json"}, paramArgs...)...)
	dash := resourceID(created, "dashboard_id")
	if dash == "" {
		t.Fatalf("dashboards create --template response missing id: %+v", created)
	}
	widgetIDs, defIDs := templateInstanceIDs(created)
	t.Cleanup(func() {
		bestEffortDelete(r, "dashboards", dash)
		for _, w := range widgetIDs {
			bestEffortDelete(r, "widgets", w)
		}
		for _, d := range defIDs {
			_, _ = r.Run(t.Context(), "", "api", "DELETE", "/metrics/definitions/"+d)
		}
	})
	if len(widgetIDs) == 0 {
		t.Fatalf("dashboards create --template created no widgets: %+v", created)
	}

	got := mustRunJSONObject(t, r, "dashboards", "get", dash)
	tmplRef, _ := got["template"].(map[string]any)
	if tmplRef["id"] != tmplID {
		t.Fatalf("dashboards get template = %v, want id %s", got["template"], tmplID)
	}

	res = mustExitZero(t, r, "dashboards", "detach-from-template", dash)
	if strings.TrimSpace(res.Stdout) != "" || !strings.Contains(res.Stderr, "Detached dashboard") {
		t.Fatalf("detach-from-template: stdout %q stderr %q", res.Stdout, res.Stderr)
	}
	got = mustRunJSONObject(t, r, "dashboards", "get", dash)
	if got["template"] != nil {
		t.Fatalf("template after detach = %v, want null", got["template"])
	}

	mustExitZero(t, r, "dashboards", "delete", dash, "--yes")
	for _, w := range widgetIDs {
		mustExitZero(t, r, "widgets", "delete", w, "--yes")
	}
	for _, d := range defIDs {
		mustExitZero(t, r, "api", "DELETE", "/metrics/definitions/"+d)
	}
}

// templateInstanceIDs collects the widget and metric definition ids a
// template instance created, from the create response's inline widgets.
func templateInstanceIDs(dash map[string]any) (widgets, defs []string) {
	add := func(list []string, v any) []string {
		if s, _ := v.(string); s != "" && !slices.Contains(list, s) {
			return append(list, s)
		}
		return list
	}
	for _, v := range asSlice(dash["widget_ids"]) {
		widgets = add(widgets, v)
	}
	for _, w := range asSlice(dash["widgets"]) {
		widget, _ := w.(map[string]any)
		widgets = add(widgets, widget["id"])
		for _, m := range asSlice(widget["metric_ids"]) {
			defs = add(defs, m)
		}
	}
	return widgets, defs
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
