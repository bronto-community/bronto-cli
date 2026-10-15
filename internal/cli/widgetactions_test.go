package cli

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

const wDash = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f1"
const wWidget = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f2"

func TestDashboardAttachWidgets(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	_, stderr, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}, "", "dashboards", "attach-widgets", wDash, "--widget-ids", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f3,aaaaaaaa-aaaa-aaaa-aaaa-0000000000f4")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/dashboards/"+wDash+"/widgets" {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
	if gotBody != `{"widget_ids":["aaaaaaaa-aaaa-aaaa-aaaa-0000000000f3","aaaaaaaa-aaaa-aaaa-aaaa-0000000000f4"]}` {
		t.Fatalf("body = %q", gotBody)
	}
	if !strings.Contains(stderr, "Attached 2 widget(s)") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestWidgetAttachWidgetsRepeatedFlag(t *testing.T) {
	var gotPath, gotBody string
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}, "", "widgets", "attach-widgets", wWidget, "--widget-ids", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f3", "--widget-ids", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f4")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/widgets/"+wWidget+"/widgets" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotBody != `{"widget_ids":["aaaaaaaa-aaaa-aaaa-aaaa-0000000000f3","aaaaaaaa-aaaa-aaaa-aaaa-0000000000f4"]}` {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestAttachWidgetsRequiresWidgetIDs(t *testing.T) {
	// No --widget-ids: usage error (exit 2), no HTTP call.
	_, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the API without --widget-ids")
		w.WriteHeader(http.StatusNoContent)
	}, "", "dashboards", "attach-widgets", wDash)
	if code := codeOf(err); code != "usage_invalid_flags" {
		t.Fatalf("code = %q (err %v)", code, err)
	}
}

func TestDashboardRemoveWidget(t *testing.T) {
	var gotMethod, gotPath string
	_, stderr, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}, "", "dashboards", "remove-widget", wDash, "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f5")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/dashboards/"+wDash+"/widgets/aaaaaaaa-aaaa-aaaa-aaaa-0000000000f5" {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
	if !strings.Contains(stderr, "Removed widget aaaaaaaa-aaaa-aaaa-aaaa-0000000000f5") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestDashboardDetachFromTemplate(t *testing.T) {
	var gotMethod, gotPath string
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{"id":"` + wDash + `","name":"detached","template":null}`))
	}, "", "dashboards", "detach-from-template", wDash, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/dashboards/"+wDash+"/detach-from-template" {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
	// The updated Dashboard is printed back.
	if !strings.Contains(out, `"name": "detached"`) && !strings.Contains(out, `"name":"detached"`) {
		t.Fatalf("out = %q", out)
	}
}

func TestWidgetActionsDryRun(t *testing.T) {
	// --dry-run must not hit the API and must report the intent.
	out, stderr, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("dry-run must not call the API")
		w.WriteHeader(http.StatusNoContent)
	}, "", "widgets", "attach-widgets", wWidget, "--widget-ids", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f3", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	decodePlan(t, out, stderr, "POST", "/widgets/"+wWidget+"/widgets")
}

// TestWidgetActionsQuiet: --quiet suppresses the non-data confirmations
// (root.go documents it as "suppress non-data messages on stderr").
func TestWidgetActionsQuiet(t *testing.T) {
	noContent := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	for _, args := range [][]string{
		{"dashboards", "attach-widgets", wDash, "--widget-ids", wWidget, "--quiet"},
		{"dashboards", "remove-widget", wDash, wWidget, "--quiet"},
		{"widgets", "update", wWidget, "-f", "name=x", "-f", "type=line", "--quiet"},
	} {
		_, stderr, err := runResource(t, noContent, "", args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if stderr != "" {
			t.Errorf("%v --quiet: stderr = %q, want empty", args, stderr)
		}
	}
}

// Completion offers widget names, so remove-widget and attach-widgets
// resolve a widget by name as well as by id.
func TestWidgetActionsResolveWidgetNames(t *testing.T) {
	const wA, wB = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f3", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f4"
	var gotReq, gotBody string
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/widgets" {
			_, _ = w.Write([]byte(`{"widgets":[{"id":"` + wA + `","name":"errors"},{"id":"` + wB + `","name":"latency"}]}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		gotReq, gotBody = r.Method+" "+r.URL.Path, string(b)
		w.WriteHeader(http.StatusNoContent)
	}
	_, stderr, err := runResource(t, handler, "", "dashboards", "remove-widget", wDash, "errors")
	if err != nil {
		t.Fatal(err)
	}
	if gotReq != "DELETE /dashboards/"+wDash+"/widgets/"+wA {
		t.Fatalf("request = %s", gotReq)
	}
	if !strings.Contains(stderr, "Removed widget errors") {
		t.Fatalf("stderr = %q", stderr)
	}
	if _, _, err := runResource(t, handler, "", "dashboards", "attach-widgets", wDash, "--widget-ids", "errors,"+wB); err != nil {
		t.Fatal(err)
	}
	if gotBody != `{"widget_ids":["`+wA+`","`+wB+`"]}` {
		t.Fatalf("body = %q", gotBody)
	}
}

// TestWidgetActionsComplete: the parent positional completes the parent
// kind's names, remove-widget's second positional and --widget-ids
// complete widgets.
func TestWidgetActionsComplete(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dashboards":
			_, _ = w.Write([]byte(`{"dashboards":[{"id":"` + wDash + `","name":"ops"}]}`))
		case "/widgets":
			_, _ = w.Write([]byte(`{"widgets":[{"id":"` + wWidget + `","name":"latency"}]}`))
		default:
			t.Errorf("unexpected API call: %s", r.URL.Path)
		}
	}
	cases := []struct {
		line []string
		want string
	}{
		{[]string{"dashboards", "attach-widgets", ""}, "ops"},
		{[]string{"dashboards", "detach-from-template", ""}, "ops"},
		{[]string{"widgets", "attach-widgets", ""}, "latency"},
		{[]string{"dashboards", "remove-widget", ""}, "ops"},
		{[]string{"dashboards", "remove-widget", "ops", ""}, "latency"},
		{[]string{"dashboards", "attach-widgets", "ops", "--widget-ids", ""}, "latency"},
	}
	for _, c := range cases {
		cands, dir := runComplete(t, handler, c.line...)
		if dir != ":4" { // NoFileComp
			t.Errorf("%v: directive = %q, want :4", c.line, dir)
		}
		if !strings.Contains(strings.Join(cands, "\n"), c.want) {
			t.Errorf("%v: candidates %v missing %q", c.line, cands, c.want)
		}
	}
}
