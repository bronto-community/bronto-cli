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
	}, "", "dashboards", "attach-widgets", wDash, "--widget-ids", "id-1,id-2")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/dashboards/"+wDash+"/widgets" {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
	if gotBody != `{"widget_ids":["id-1","id-2"]}` {
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
	}, "", "widgets", "attach-widgets", wWidget, "--widget-ids", "id-1", "--widget-ids", "id-2")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/widgets/"+wWidget+"/widgets" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotBody != `{"widget_ids":["id-1","id-2"]}` {
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
	}, "", "dashboards", "remove-widget", wDash, "w-9")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/dashboards/"+wDash+"/widgets/w-9" {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
	if !strings.Contains(stderr, "Removed widget w-9") {
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
	_, stderr, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("dry-run must not call the API")
		w.WriteHeader(http.StatusNoContent)
	}, "", "widgets", "attach-widgets", wWidget, "--widget-ids", "id-1", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "DRY RUN") || !strings.Contains(stderr, "attach 1 widget(s)") {
		t.Fatalf("stderr = %q", stderr)
	}
}
