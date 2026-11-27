package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "docs", "testdata", "mock")
}

type logRecorder struct{ lines []string }

func (l *logRecorder) logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func newTestServer(t *testing.T) (*Server, *logRecorder) {
	t.Helper()
	rec := &logRecorder{}
	s, err := New(fixtureDir(t), WithLogf(rec.logf))
	if err != nil {
		t.Fatal(err)
	}
	return s, rec
}

func do(t *testing.T, h http.Handler, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("X-BRONTO-API-KEY", APIKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// TestMockWorld pins the fixtures to "The docs mock world" in
// docs/AGENTS.md, so a fixture edit cannot silently drift from what the
// docs promise.
func TestMockWorld(t *testing.T) {
	s, _ := newTestServer(t)
	var names []string
	for _, d := range s.datasets {
		names = append(names, fmt.Sprintf("%s/%s", d["collection"], d["log"]))
	}
	sort.Strings(names)
	want := "prod/checkout-service prod/payments-api prod/web-frontend staging/checkout-service"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("datasets = %s, want %s", got, want)
	}
	lo := time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC).UnixMilli()
	hi := time.Date(2026, 7, 19, 9, 30, 0, 0, time.UTC).UnixMilli()
	fields := map[string]bool{"message": true, "service": true, "host": true, "status": true, "duration_ms": true, "path": true, "trace_id": true}
	for id, evs := range s.events {
		if len(evs) == 0 {
			t.Errorf("dataset %s has no events", id)
		}
		for _, ev := range evs {
			if ms := eventMillis(ev); ms < lo || ms >= hi {
				t.Errorf("event outside 09:00-09:30: %v", ev["@time"])
			}
			switch ev["@status"] {
			case "info", "warn", "error":
			default:
				t.Errorf("bad @status %v", ev["@status"])
			}
			kvs, _ := ev["message_kvs"].(map[string]any)
			for k := range kvs {
				if !fields[k] {
					t.Errorf("unexpected log field %q", k)
				}
			}
			if h, _ := kvs["host"].(string); h != "web-1" && h != "web-2" && h != "web-3" {
				t.Errorf("host %q outside web-1..web-3", h)
			}
		}
	}
	services := map[string]bool{}
	example := false
	for _, sp := range s.spans {
		services[fmt.Sprint(sp["Resource:service.name"])] = true
		if sp["Span:trace_id"] == "4bf92f3577b34da6a3ce929d0e0e4736" {
			example = true
		}
		if ms := eventMillis(sp); ms < lo || ms >= hi {
			t.Errorf("span outside 09:00-09:30: %v", sp["@time"])
		}
	}
	if len(services) != 3 || !services["web-frontend"] || !services["checkout-service"] || !services["payments-api"] {
		t.Errorf(".traces services = %v", services)
	}
	if !example {
		t.Error("example trace 4bf92f3577b34da6a3ce929d0e0e4736 missing")
	}
	for path, name := range map[string]string{
		"/monitors": "High 5xx rate on checkout", "/dashboards": "Checkout overview",
		"/users": "ada@example.com", "/groups": "oncall",
		"/integrations/webhooks": "oncall-pager", "/integrations/slack": "checkout-alerts",
		"/integrations/slack/workspaces": "example-eng", "/integrations/providers": "PagerDuty",
	} {
		code, body := do(t, s, http.MethodGet, path, nil)
		b, _ := json.Marshal(body)
		if code != http.StatusOK || !strings.Contains(string(b), name) {
			t.Errorf("GET %s = %d %s, want it to contain %q", path, code, b, name)
		}
	}
}

func TestSearch(t *testing.T) {
	s, _ := newTestServer(t)
	code, body := do(t, s, http.MethodPost, "/search", map[string]any{
		"from": []string{"0b7c6f2e-1d4a-4c8e-9a51-3f2d8e6b7a10"}, "where": "status >= 500",
		"select": []string{"@time", "host"}, "limit": 5,
	})
	if code != http.StatusOK {
		t.Fatalf("search = %d %v", code, body)
	}
	res, _ := body["result"].([]any)
	if len(res) != 5 {
		t.Fatalf("result rows = %d, want 5", len(res))
	}
	first := res[0].(map[string]any)
	if first["host"] == nil || first["@time"] == nil {
		t.Errorf("projection lost fields: %v", first)
	}

	// Group-by aggregate in the live shape.
	_, body = do(t, s, http.MethodPost, "/search", map[string]any{
		"from_expr": "logset = '.traces'", "where": "NOT EXISTS Span:parent_span_id",
		"select": []string{"count(*)"}, "groups": []string{"Resource:service.name"},
	})
	groups, _ := body["groups"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["group"] != "[web-frontend]" {
		t.Errorf("root spans by service = %v, want only web-frontend", groups)
	}

	// Bad query and missing scope are 400s.
	if code, _ := do(t, s, http.MethodPost, "/search", map[string]any{"from_expr": "logset = '.traces'", "where": "status >="}); code != http.StatusBadRequest {
		t.Errorf("bad where = %d, want 400", code)
	}
	if code, _ := do(t, s, http.MethodPost, "/search", map[string]any{"where": "a = 1"}); code != http.StatusBadRequest {
		t.Errorf("no from = %d, want 400", code)
	}
}

func TestTailProgressesAndResets(t *testing.T) {
	s, _ := newTestServer(t)
	poll := func() int {
		_, body := do(t, s, http.MethodPost, "/search", map[string]any{
			"from":   []string{"0b7c6f2e-1d4a-4c8e-9a51-3f2d8e6b7a10"},
			"select": []string{"@time", "@raw", "@sequence", "@origin", "@status"}, "most_recent_first": false,
		})
		res, _ := body["result"].([]any)
		last := res[len(res)-1].(map[string]any)
		return int(last["@sequence"].(float64))
	}
	a, b := poll(), poll()
	if b <= a {
		t.Errorf("second poll did not advance: %d then %d", a, b)
	}
	s.Reset()
	if c := poll(); c != a {
		t.Errorf("after Reset first poll = %d, want %d", c, a)
	}
}

func TestUnhandledAndAuth(t *testing.T) {
	s, rec := newTestServer(t)
	if code, _ := do(t, s, http.MethodGet, "/no-such-thing", nil); code != http.StatusNotFound {
		t.Errorf("unknown route = %d, want 404", code)
	}
	if len(rec.lines) == 0 || !strings.Contains(rec.lines[0], "unhandled GET /no-such-thing") {
		t.Errorf("log = %v, want an unhandled line naming the route", rec.lines)
	}
	r := httptest.NewRequest(http.MethodGet, "/logs", nil)
	r.Header.Set("X-BRONTO-API-KEY", "wrong")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong key = %d, want 401", w.Code)
	}
	// Resource by id, and a relative-time placeholder resolved to a number.
	code, body := do(t, s, http.MethodGet, "/logs/0b7c6f2e-1d4a-4c8e-9a51-3f2d8e6b7a10", nil)
	meta, _ := body["metadata"].(map[string]any)
	if code != http.StatusOK || meta == nil {
		t.Fatalf("GET /logs/<id> = %d %v", code, body)
	}
	if _, ok := meta["last_heartbeat_at"].(float64); !ok {
		t.Errorf("last_heartbeat_at = %v, want epoch millis", meta["last_heartbeat_at"])
	}
}

// TestTemplatePreview pins the read-only POST route behind
// `bronto dashboards templates show`: the preview answers with its own
// fixture instead of echoing the body back as a create.
func TestTemplatePreview(t *testing.T) {
	s, _ := newTestServer(t)
	code, body := do(t, s, http.MethodPost, "/dashboards/templates/e7a1c3f5-2b4d-4e6f-8a0b-1c2d3e4f5a61/preview",
		map[string]any{"arguments": map[string]any{}})
	if code != http.StatusOK || body["name"] != "HTTP service overview" {
		t.Fatalf("preview = %d %v", code, body)
	}
	if ws, _ := body["widgets"].([]any); len(ws) != 2 {
		t.Fatalf("preview widgets = %v", body["widgets"])
	}
}

// TestIntegrations pins the routes behind `bronto webhooks`, `bronto slack`
// and `bronto providers`: the export endpoints answer HCL as text/plain,
// and a provider's secret is write-only.
func TestIntegrations(t *testing.T) {
	s, rec := newTestServer(t)
	get := func(path string) (*httptest.ResponseRecorder, string) {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-BRONTO-API-KEY", APIKey)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w, w.Body.String()
	}
	for base, resource := range map[string]string{
		"/integrations/webhooks":  "bronto_webhook_integration",
		"/integrations/slack":     "bronto_slack_integration",
		"/integrations/providers": "bronto_provider_integration",
	} {
		code, body := do(t, s, http.MethodGet, base, nil)
		rows, _ := body["integrations"].([]any)
		if code != http.StatusOK || len(rows) == 0 {
			t.Fatalf("GET %s = %d %v", base, code, body)
		}
		for _, row := range rows {
			id, _ := row.(map[string]any)["id"].(string)
			for _, format := range []string{"terraform", "opentofu"} {
				w, hcl := get(base + "/" + id + "/export?format=" + format)
				if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/plain" ||
					!strings.HasPrefix(hcl, `resource "`+resource+`" "`) {
					t.Errorf("GET %s/%s/export?format=%s = %d %q %q", base, id, format, w.Code, w.Header().Get("Content-Type"), hcl)
				}
				if strings.Contains(hcl, "secret =") {
					t.Errorf("export of %s/%s carries a secret: %s", base, id, hcl)
				}
			}
			if w, _ := get(base + "/" + id + "/export?format=yaml"); w.Code != http.StatusBadRequest {
				t.Errorf("export?format=yaml = %d, want 400", w.Code)
			}
		}
	}
	// An export without a fixture is unhandled, not an empty list printed as HCL.
	if w, _ := get("/monitors/a1f3c9d2-4b6e-4f81-9c2d-7e5b3a8f1c01/export?format=terraform"); w.Code != http.StatusNotFound {
		t.Errorf("export without a fixture = %d, want 404", w.Code)
	}
	if len(rec.lines) == 0 || !strings.Contains(rec.lines[len(rec.lines)-1], "unhandled GET /monitors/") {
		t.Errorf("log = %v, want an unhandled line", rec.lines)
	}

	_, ws := do(t, s, http.MethodGet, "/integrations/slack/workspaces", nil)
	wsRows, _ := ws["slack_workspaces"].([]any)
	if len(wsRows) != 1 {
		t.Fatalf("slack workspaces = %v", ws)
	}
	wsID, _ := wsRows[0].(map[string]any)["id"].(string)
	if code, _ := do(t, s, http.MethodDelete, "/integrations/slack/workspaces/"+wsID, nil); code != http.StatusNoContent {
		t.Errorf("DELETE workspace = %d, want 204", code)
	}
	_, slack := do(t, s, http.MethodGet, "/integrations/slack", nil)
	if got := slack["integrations"].([]any)[0].(map[string]any)["workspace_id"]; got != wsID {
		t.Errorf("slack integration workspace_id = %v, want the workspace %s", got, wsID)
	}

	// The secret is never returned: reads carry has_secret instead.
	_, providers := do(t, s, http.MethodGet, "/integrations/providers", nil)
	b, _ := json.Marshal(providers)
	if strings.Contains(string(b), `"secret"`) {
		t.Errorf("provider fixtures carry a secret: %s", b)
	}
	id, _ := providers["integrations"].([]any)[0].(map[string]any)["id"].(string)
	for _, req := range []struct{ method, path string }{
		{http.MethodPost, "/integrations/providers"},
		{http.MethodPut, "/integrations/providers/" + id},
	} {
		_, body := do(t, s, req.method, req.path, map[string]any{"url": "https://example.com/x", "secret": "s3"})
		if _, ok := body["secret"]; ok || body["has_secret"] != true || body["url"] != "https://example.com/x" {
			t.Errorf("%s %s = %v, want has_secret and no secret", req.method, req.path, body)
		}
	}
}
