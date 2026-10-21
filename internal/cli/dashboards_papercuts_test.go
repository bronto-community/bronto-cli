package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
)

func hintOf(err error) string {
	var ce *clierr.Error
	if errors.As(err, &ce) {
		return ce.Hint
	}
	return ""
}

// The API silently drops widget_ids on dashboard create, so the CLI
// refuses it (exit 2) before any request and points to attach-widgets.
func TestDashboardsCreateRefusesWidgetIDs(t *testing.T) {
	noCall := func(w http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the API")
		w.WriteHeader(http.StatusNoContent)
	}
	for _, args := range [][]string{
		{"dashboards", "create", "-f", "name=x", "-f", `widget_ids=["` + wWidget + `"]`},
		{"dashboards", "create", "--input", "-"},
	} {
		_, _, err := runResource(t, noCall, `{"name":"x","widget_ids":[]}`, args...)
		if code := codeOf(err); code != "usage_unsupported_field" {
			t.Fatalf("%v: code = %q (err %v)", args, code, err)
		}
		if !strings.Contains(hintOf(err), "attach-widgets") {
			t.Errorf("%v: hint should point to attach-widgets: %q", args, hintOf(err))
		}
	}
}

// PATCH /dashboards/{id} is a oneOf of name and layout: both answer
// "400: More than one schema valid", neither changes nothing.
func TestDashboardsUpdateOneOfNameOrLayout(t *testing.T) {
	noCall := func(w http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the API")
		w.WriteHeader(http.StatusNoContent)
	}
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"-f", "name=x", "-f", `layout={"widget_layouts":[]}`}, "usage_conflicting_fields"},
		{[]string{"-f", "description=x"}, "usage_missing_fields"},
	} {
		args := append([]string{"dashboards", "update", wDash}, tc.args...)
		_, _, err := runResource(t, noCall, "", args...)
		if code := codeOf(err); code != tc.code {
			t.Fatalf("%v: code = %q, want %q (err %v)", tc.args, code, tc.code, err)
		}
	}
	// Each one alone goes through.
	for _, f := range []string{"name=x", `layout={"widget_layouts":[]}`} {
		var gotMethod string
		_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			w.WriteHeader(http.StatusNoContent)
		}, "", "dashboards", "update", wDash, "-f", f)
		if err != nil || gotMethod != http.MethodPatch {
			t.Fatalf("-f %s: method %q err %v", f, gotMethod, err)
		}
	}
}

// dashboardStub answers the 204 mutations and serves the dashboard on GET,
// recording the requests in order.
func dashboardStub(reqs *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*reqs = append(*reqs, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":"` + wDash + `","name":"renamed","widget_ids":["` + wWidget + `"]}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// update, attach-widgets and remove-widget answer 204: with -o json or
// --jq they print the resulting object instead of a stderr sentence.
func TestDashboardMutationsPrintJSON(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		mutation string
	}{
		{[]string{"dashboards", "update", wDash, "-f", "name=renamed"}, "PATCH /dashboards/" + wDash},
		{[]string{"dashboards", "attach-widgets", wDash, "--widget-ids", wWidget}, "POST /dashboards/" + wDash + "/widgets"},
		{[]string{"dashboards", "remove-widget", wDash, wWidget}, "DELETE /dashboards/" + wDash + "/widgets/" + wWidget},
	} {
		var reqs []string
		out, stderr, err := runResource(t, dashboardStub(&reqs), "", append(tc.args, "-o", "json")...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		want := []string{tc.mutation, "GET /dashboards/" + wDash}
		if strings.Join(reqs, "|") != strings.Join(want, "|") {
			t.Errorf("%v: requests = %v, want %v", tc.args, reqs, want)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil || doc["name"] != "renamed" {
			t.Errorf("%v: stdout = %q (err %v)", tc.args, out, err)
		}
		if stderr != "" {
			t.Errorf("%v: stderr = %q, want empty", tc.args, stderr)
		}

		reqs = nil
		out, _, err = runResource(t, dashboardStub(&reqs), "", append(tc.args, "-o", "json", "--jq", ".widget_ids[0]", "-r")...)
		if err != nil || strings.TrimSpace(out) != wWidget {
			t.Errorf("%v --jq: stdout = %q (err %v)", tc.args, out, err)
		}

		// Without an explicit machine format: no extra GET, stderr note.
		reqs = nil
		out, stderr, err = runResource(t, dashboardStub(&reqs), "", tc.args...)
		if err != nil || out != "" || stderr == "" || len(reqs) != 1 {
			t.Errorf("%v (no -o): out %q stderr %q reqs %v err %v", tc.args, out, stderr, reqs, err)
		}
	}
}

func TestWidgetAttachWidgetsHelpSaysGroup(t *testing.T) {
	cmd := newAttachWidgetsCmd("widgets")
	if !strings.Contains(cmd.Short, "group widget") || !strings.Contains(cmd.Long, "type group") {
		t.Fatalf("short %q long %q", cmd.Short, cmd.Long)
	}
	if d := newAttachWidgetsCmd("dashboards"); d.Short != "Attach widgets to a dashboard" {
		t.Fatalf("dashboards short = %q", d.Short)
	}
}

// --jq '.[] | …' on list output runs per item, so the CLI hints once on
// stderr; a whole-document PrintJSON (get) never hints.
func TestJQLeadingIterateHintsOnLists(t *testing.T) {
	list := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/monitors" {
			_, _ = w.Write([]byte(`{"monitors":[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1","name":"cpu"},{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000a2","name":"mem"}]}`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1","name":"cpu"}]`))
	}
	_, stderr, err := runResource(t, list, "", "monitors", "list", "-o", "json", "--jq", "  .[] | .name")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(stderr, output.JQListHint) != 1 {
		t.Errorf("list stderr = %q, want the hint once", stderr)
	}
	for _, tc := range []struct {
		args []string
	}{
		{[]string{"monitors", "list", "-o", "json", "--jq", ".name"}},
		{[]string{"monitors", "list", "-o", "json", "--jq", ".[] | .name", "--quiet"}},
		{[]string{"monitors", "get", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1", "-o", "json", "--jq", ".[] | .name"}},
	} {
		_, stderr, err := runResource(t, list, "", tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stderr, "hint:") {
			t.Errorf("%v: unexpected hint %q", tc.args, stderr)
		}
	}
}
