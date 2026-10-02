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
		services[fmt.Sprint(sp["$service.name"])] = true
		if sp["$span.trace_id"] == "4bf92f3577b34da6a3ce929d0e0e4736" {
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
		"from_expr": "logset = '.traces'", "where": "NOT EXISTS $span.parent_span_id",
		"select": []string{"count(*)"}, "groups": []string{"$service.name"},
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
