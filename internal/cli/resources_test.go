package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// runResource runs the root command with the given args against a stub
// server, returning captured stdout/stderr and the error (if any).
func runResource(t *testing.T, handler http.HandlerFunc, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(stdin))
	full := append(append([]string{}, args...), "--base-url", srv.URL, "--api-key", "k")
	root.SetArgs(full)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestResourcesListRowsFromNamedKey(t *testing.T) {
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/monitors" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"monitors":[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1","name":"cpu"},{"id":"m2","name":"mem"}]}`))
	}, "", "monitors", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("stdout not a JSON array: %v (%q)", err, out)
	}
	if len(rows) != 2 || rows[0]["id"] != "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1" {
		t.Fatalf("rows = %v", rows)
	}
}

func TestResourcesListRowsFromBareArray(t *testing.T) {
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1"},{"id":"m2"}]`))
	}, "", "monitors", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("stdout not a JSON array: %v (%q)", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
}

func TestResourcesGet(t *testing.T) {
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/monitors/aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1","name":"cpu"}`))
	}, "", "monitors", "get", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout not JSON: %v (%q)", err, out)
	}
	if doc["id"] != "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1" {
		t.Fatalf("doc = %v", doc)
	}
}

// TestResourcesGetEscapesID pins that a RESOLVED id (which comes from the
// API's own list response, so it may contain anything) is path-escaped:
// the name "weird" resolves to id "a/b", and the per-ID request must hit
// /monitors/a%2Fb.
func TestResourcesGetEscapesID(t *testing.T) {
	var gotPath string
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/monitors" {
			_, _ = w.Write([]byte(`[{"id":"a/b","name":"weird"}]`))
			return
		}
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"id":"a/b"}`))
	}, "", "monitors", "get", "weird")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/monitors/a%2Fb" {
		t.Fatalf("escaped path = %q, want /monitors/a%%2Fb", gotPath)
	}
}

// TestResourcesGetByName pins name resolution on the generic verbs: a
// non-UUID ref lists the resource, matches by name, and fetches by id.
// Ambiguous and unknown names produce teaching errors.
func TestResourcesGetByName(t *testing.T) {
	list := `[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-000000000001","name":"cpu"},` +
		`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-000000000002","name":"dup"},` +
		`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-000000000003","name":"dup"}]`
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/monitors" {
			_, _ = w.Write([]byte(list))
			return
		}
		if r.URL.Path != "/monitors/aaaaaaaa-aaaa-aaaa-aaaa-000000000001" {
			t.Fatalf("unexpected per-ID path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-000000000001","name":"cpu"}`))
	}
	out, _, err := runResource(t, handler, "", "monitors", "get", "cpu", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cpu") {
		t.Fatalf("out = %q", out)
	}

	_, _, err = runResource(t, handler, "", "monitors", "get", "dup")
	var ce *clierr.Error
	if !errors.As(err, &ce) || !strings.Contains(ce.Code, "ambiguous") {
		t.Fatalf("want ambiguous error, got %v", err)
	}

	_, _, err = runResource(t, handler, "", "monitors", "get", "ghost")
	if !errors.As(err, &ce) || ce.Code != "resource_not_found" || !strings.Contains(ce.Hint, "cpu") {
		t.Fatalf("want resource_not_found listing names, got %v", err)
	}
}

func TestResourcesCreateViaFields(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing content type")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1"}`))
	}, "", "monitors", "create", "-f", "name=cpu", "-f", "limit=10")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/monitors" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotBody["name"] != "cpu" || gotBody["limit"] != float64(10) {
		t.Fatalf("body = %v", gotBody)
	}
}

// TestResourcesConnectionFailureIsNetworkError pins that doJSONRequest maps
// connection failures (server unreachable) the same way bronto.Client does:
// a retryable network_error, not api_unreachable.
func TestResourcesConnectionFailureIsNetworkError(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"monitors", "get", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1", "--base-url", "http://127.0.0.1:1", "--api-key", "k"})
	err := root.Execute()
	if err == nil {
		t.Fatal("want error")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "network_error" || !ce.Retryable {
		t.Fatalf("want retryable network_error, got %v", err)
	}
}

func TestResourcesCreateRequiresBodySource(t *testing.T) {
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be contacted")
	}, "", "monitors", "create")
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage error, got %v", err)
	}
}

func TestResourcesUpdateUsesPut(t *testing.T) {
	var gotMethod, gotPath string
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1","name":"cpu2"}`))
	}, "", "monitors", "update", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1", "-f", "name=cpu2")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/monitors/aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1" {
		t.Fatalf("method/path = %s %s, want PUT /monitors/aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1", gotMethod, gotPath)
	}
}

func TestResourcesDeleteWithYesSkipsPrompt(t *testing.T) {
	var gotMethod, gotPath string
	_, stderr, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}, "", "monitors", "delete", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/monitors/aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(stderr, "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1") {
		t.Fatalf("stderr missing confirmation: %q", stderr)
	}
}

func TestResourcesDeleteNonTTYWithoutYesIsUsageError(t *testing.T) {
	oldOut, oldIn := stdoutIsTTY, stdinIsTTY
	stdoutIsTTY = func() bool { return false }
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdoutIsTTY, stdinIsTTY = oldOut, oldIn })

	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be contacted")
	}, "", "monitors", "delete", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1")
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage error exit 2, got %v", err)
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_confirmation_required" {
		t.Fatalf("want usage_confirmation_required, got %v", err)
	}
}

func TestResourcesDeleteTTYPromptAbortOnNonY(t *testing.T) {
	oldOut, oldIn := stdoutIsTTY, stdinIsTTY
	stdoutIsTTY = func() bool { return true }
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY, stdinIsTTY = oldOut, oldIn })

	contacted := false
	_, stderr, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		contacted = true
	}, "n\n", "monitors", "delete", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1")
	if err != nil {
		t.Fatalf("abort should exit 0, got %v", err)
	}
	if contacted {
		t.Fatal("server should not be contacted on abort")
	}
	if !strings.Contains(stderr, "Aborted") {
		t.Fatalf("stderr missing Aborted: %q", stderr)
	}
}

func TestResourcesDeleteTTYPromptProceedsOnY(t *testing.T) {
	oldOut, oldIn := stdoutIsTTY, stdinIsTTY
	stdoutIsTTY = func() bool { return true }
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY, stdinIsTTY = oldOut, oldIn })

	var gotMethod string
	_, stderr, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	}, "y\n", "monitors", "delete", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %s", gotMethod)
	}
	if !strings.Contains(stderr, "[y/N]") {
		t.Fatalf("stderr missing prompt: %q", stderr)
	}
}

func TestDatasetsCreatePostsToDatasetsPathWhileListHitsLogs(t *testing.T) {
	var gotCreatePath string
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datasets" {
			gotCreatePath = r.URL.Path
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000d1"}`))
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}, "", "datasets", "create", "-f", "collection=demo", "-f", "dataset=http")
	if err != nil {
		t.Fatal(err)
	}
	if gotCreatePath != "/datasets" {
		t.Fatalf("create path = %q, want /datasets", gotCreatePath)
	}

	var gotListPath string
	_, _, err = runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotListPath = r.URL.Path
		_, _ = w.Write([]byte(`{"logs":[]}`))
	}, "", "datasets", "list")
	if err != nil {
		t.Fatal(err)
	}
	if gotListPath != "/logs" {
		t.Fatalf("list path = %q, want /logs", gotListPath)
	}
}

func TestDatasetsUpdateUsesPut(t *testing.T) {
	var gotMethod string
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_, _ = w.Write([]byte(`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000d1"}`))
	}, "", "datasets", "update", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000d1", "-f", "log=http2")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("method = %s, want PUT", gotMethod)
	}
}

func TestAPIKeysListAutoColumns(t *testing.T) {
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api-keys" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"api_keys":[{"id":"k1","name":"prod","roles":["SearchApi"]}]}`))
	}, "", "api-keys", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "k1") || !strings.Contains(strings.ToUpper(out), "ID") {
		t.Fatalf("table output missing expected columns: %q", out)
	}
}

func TestMonitorsEventsMute(t *testing.T) {
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/monitors/aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1/events" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"events":[{"id":"e1"}]}`))
	}, "", "monitors", "events", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("events output = %q, err %v", out, err)
	}

	// Mute goes through the live status endpoint: POST /monitors/{id}/status
	// with mute_until (-1 forever, 0 unmute, future epoch-millis until then).
	var gotPath, gotBody string
	_, stderr, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}, "", "monitors", "mute", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/monitors/aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1/status" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody != `{"mute_until":-1}` {
		t.Fatalf("body = %q", gotBody)
	}
	if !strings.Contains(stderr, "Muted monitor aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1") {
		t.Fatalf("stderr missing confirmation: %q", stderr)
	}

	_, stderr, err = runResource(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}, "", "monitors", "mute", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1", "--unmute")
	if err != nil {
		t.Fatal(err)
	}
	if gotBody != `{"mute_until":0}` {
		t.Fatalf("unmute body = %q", gotBody)
	}
	if !strings.Contains(stderr, "Unmuted monitor aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1") {
		t.Fatalf("stderr missing unmute confirmation: %q", stderr)
	}
}

func TestUserActionCommands(t *testing.T) {
	for _, action := range []string{"deactivate", "reactivate", "resend-invite"} {
		var gotMethod, gotPath string
		_, stderr, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath = r.Method, r.URL.Path
			w.WriteHeader(http.StatusOK)
		}, "", "users", action, "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1")
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if gotMethod != http.MethodPost || gotPath != "/users/aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1/"+action {
			t.Fatalf("%s: %s %s", action, gotMethod, gotPath)
		}
		if !strings.Contains(stderr, "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1") {
			t.Fatalf("%s stderr = %q", action, stderr)
		}
	}

	// dry-run: no contact, honest message
	out, stderr, err := runResource(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be contacted under --dry-run")
	}, "", "users", "deactivate", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	decodePlan(t, out, stderr, "POST", "/users/aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1/deactivate")
}

func TestGroupMembersCommand(t *testing.T) {
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/groups/aaaaaaaa-aaaa-aaaa-aaaa-0000000000f1/members" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"members":[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1","email":"a@b.c"}]}`))
	}, "", "groups", "members", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f1", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0]["id"] != "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1" {
		t.Fatalf("members out = %q err=%v", out, err)
	}
}

// TestEpochColumnsRenderReadableInTables pins: the event-history tables
// (monitors events/notifications) show absolute UTC times and groups
// members shows its modified_at like other *_at list columns, while
// json keeps the raw epoch milliseconds.
func TestEpochColumnsRenderReadableInTables(t *testing.T) {
	const mon = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1"
	const grp = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f1"
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/monitors/" + mon + "/events":
			_, _ = w.Write([]byte(`{"monitor_events":[{"monitor_id":"m","time":1784452890000,"monitor_status":"ALERT"}]}`))
		case "/monitors/" + mon + "/notifications":
			_, _ = w.Write([]byte(`{"monitor_notifications":[{"time":1784452891204,"status":"SENT","type":"EMAIL"}]}`))
		case "/groups/" + grp + "/members":
			_, _ = w.Write([]byte(`{"members":[{"member_id":"u1","member_type":"USER","modified_at":1777890000000}]}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}
	cases := []struct {
		args       []string
		table, csv string // expected substrings
		raw        string // raw epoch value expected in json
	}{
		{[]string{"monitors", "events", mon}, "2026-07-19 09:21:30 UTC", "2026-07-19T09:21:30Z", "1784452890000"},
		{[]string{"monitors", "notifications", mon}, "2026-07-19 09:21:31 UTC", "2026-07-19T09:21:31Z", "1784452891204"},
		{[]string{"groups", "members", grp}, "2026-05-04", "2026-05-04T", "1777890000000"},
	}
	for _, c := range cases {
		name := strings.Join(c.args[:2], " ")
		out, _, err := runResource(t, handler, "", append(append([]string{}, c.args...), "-o", "table")...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(out, c.table) || strings.Contains(out, c.raw) {
			t.Fatalf("%s table = %q, want %q and no raw %s", name, out, c.table, c.raw)
		}
		out, _, err = runResource(t, handler, "", append(append([]string{}, c.args...), "-o", "csv")...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(out, c.csv) {
			t.Fatalf("%s csv = %q, want %q", name, out, c.csv)
		}
		out, _, err = runResource(t, handler, "", append(append([]string{}, c.args...), "-o", "json")...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(out, c.raw) {
			t.Fatalf("%s json = %q, want raw %s", name, out, c.raw)
		}
	}
}

// TestExpansionResourcesRouteCorrectly spot-checks the new registry rows'
// generated verbs against their spec paths (nested attach + PUT vs PATCH).
func TestExpansionResourcesRouteCorrectly(t *testing.T) {
	var gotMethod, gotPath string
	record := func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}
	cases := []struct {
		args                 []string
		wantMethod, wantPath string
	}{
		{[]string{"monitors", "templates", "list"}, http.MethodGet, "/monitors/templates"},
		{[]string{"monitors", "downtimes", "update", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000d1", "-f", "name=x"}, http.MethodPut, "/monitors/downtimes/aaaaaaaa-aaaa-aaaa-aaaa-0000000000d1"},
		{[]string{"limits", "update", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000b1", "-f", "name=x"}, http.MethodPatch, "/limits/aaaaaaaa-aaaa-aaaa-aaaa-0000000000b1"},
		{[]string{"webhooks", "create", "-f", "name=x", "-f", "url=https://example.com/h"}, http.MethodPost, "/integrations/webhooks"},
		{[]string{"slack", "list"}, http.MethodGet, "/integrations/slack"},
		{[]string{"log-views", "list"}, http.MethodGet, "/logs/views"},
		{[]string{"widgets", "list"}, http.MethodGet, "/widgets"},
		{[]string{"widgets", "create", "-f", "name=x", "-f", "type=line"}, http.MethodPost, "/widgets"},
		{[]string{"widgets", "update", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1", "-f", "name=x", "-f", "type=line"}, http.MethodPut, "/widgets/aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1"},
	}
	for _, c := range cases {
		if _, _, err := runResource(t, record, "", c.args...); err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if gotMethod != c.wantMethod || gotPath != c.wantPath {
			t.Fatalf("%v -> %s %s, want %s %s", c.args, gotMethod, gotPath, c.wantMethod, c.wantPath)
		}
	}
}

// TestResourceUpdateNoContentConfirmsOnStderr: updateWidget answers 204 with
// no body. update must confirm on stderr and keep stdout empty, like delete,
// instead of printing a bare "null".
func TestResourceUpdateNoContentConfirmsOnStderr(t *testing.T) {
	const id = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e2"
	out, errOut, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/widgets/"+id {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}, "", "widgets", "update", id, "-f", "name=x", "-f", "type=line", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want empty on a 204 update", out)
	}
	if want := "Updated widget " + id + "."; !strings.Contains(errOut, want) {
		t.Fatalf("stderr = %q, want it to contain %q", errOut, want)
	}
}

// TestAmbiguousNameErrorForMultiWordKinds pins the usage_ambiguous_<kind>
// code as lower_snake and the message's human plural for kinds whose
// singular has spaces or capitals ("Slack integration", "API key").
func TestAmbiguousNameErrorForMultiWordKinds(t *testing.T) {
	rows := []map[string]any{
		{"id": "id-1", "name": "dup"}, {"id": "id-2", "name": "dup"},
	}
	for kind, want := range map[string]string{
		"slack":    "usage_ambiguous_slack_integration",
		"api-keys": "usage_ambiguous_api_key",
		"monitors": "usage_ambiguous_monitor",
	} {
		desc, _ := descByName(kind)
		_, err := matchResourceRef(desc, rows, "dup")
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != want || clierr.ExitCode(err) != 2 {
			t.Errorf("%s: want %s exit 2, got %v", kind, want, err)
			continue
		}
		if wantMsg := "2 " + desc.plural() + " are named"; !strings.Contains(ce.Message, wantMsg) {
			t.Errorf("%s: message %q, want it to contain %q", kind, ce.Message, wantMsg)
		}
	}
}

// TestNotFoundHintDedupesNames pins that the not-found hint lists each
// available name once, even when several resources share it.
func TestNotFoundHintDedupesNames(t *testing.T) {
	desc, _ := descByName("monitors")
	rows := []map[string]any{
		{"id": "id-1", "name": "dup"}, {"id": "id-2", "name": "dup"}, {"id": "id-3", "name": "cpu"},
	}
	_, err := matchResourceRef(desc, rows, "ghost")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "resource_not_found" {
		t.Fatalf("want resource_not_found, got %v", err)
	}
	if strings.Count(ce.Hint, "dup") != 1 {
		t.Fatalf("hint lists a duplicate name more than once: %q", ce.Hint)
	}
}

// TestUserEmailMatchIsCaseInsensitive pins that users resolve by email
// regardless of case, while other kinds keep exact name matching.
func TestUserEmailMatchIsCaseInsensitive(t *testing.T) {
	users, _ := descByName("users")
	id, err := matchResourceRef(users, []map[string]any{{"id": "u-1", "email": "ada@example.com"}}, "ADA@Example.com")
	if err != nil || id != "u-1" {
		t.Fatalf("email match: id=%q err=%v", id, err)
	}
	monitors, _ := descByName("monitors")
	if _, err := matchResourceRef(monitors, []map[string]any{{"id": "m-1", "name": "cpu"}}, "CPU"); err == nil {
		t.Fatal("monitor names must match exactly")
	}
}

// TestResourcesDeleteResolvesBeforeConfirming pins the delete order: an
// unknown name fails as not-found (exit 4) before any confirmation, even
// without a terminal, and the prompt names the id a name resolved to.
func TestResourcesDeleteResolvesBeforeConfirming(t *testing.T) {
	oldOut, oldIn := stdoutIsTTY, stdinIsTTY
	t.Cleanup(func() { stdoutIsTTY, stdinIsTTY = oldOut, oldIn })
	list := `[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-000000000001","name":"cpu"}]`
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/monitors" {
			_, _ = w.Write([]byte(list))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}

	stdoutIsTTY = func() bool { return false }
	stdinIsTTY = func() bool { return false }
	_, _, err := runResource(t, handler, "", "monitors", "delete", "ghost")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "resource_not_found" || clierr.ExitCode(err) != 4 {
		t.Fatalf("want resource_not_found exit 4, got %v", err)
	}

	stdoutIsTTY = func() bool { return true }
	stdinIsTTY = func() bool { return true }
	_, stderr, err := runResource(t, handler, "n\n", "monitors", "delete", "cpu")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Delete monitor cpu (aaaaaaaa-aaaa-aaaa-aaaa-000000000001)?") {
		t.Fatalf("prompt should show the resolved id: %q", stderr)
	}
}

// TestResourcesCreateRejectsInvalidJSONInput pins that create/update
// --input must be a JSON object or array, checked before any request.
func TestResourcesCreateRejectsInvalidJSONInput(t *testing.T) {
	for _, body := range []string{"", "{oops", "[1,"} {
		_, _, err := runResource(t, func(http.ResponseWriter, *http.Request) {
			t.Error("server contacted")
		}, body, "monitors", "create", "--input", "-")
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != "usage_invalid_body" || clierr.ExitCode(err) != 2 {
			t.Errorf("body %q: want usage_invalid_body exit 2, got %v", body, err)
		}
	}
}

// TestResourceListDerivedFieldsInJSON pins that --fields and --fields '?'
// agree across formats for derived list columns: json/jsonl can select
// last_activity (and list it under '?'), while plain -o json stays raw.
func TestResourceListDerivedFieldsInJSON(t *testing.T) {
	list := `[{"collection":"prod","dataset":"web","log_id":"l-1","metadata":{"last_heartbeat_at":1784452890000}}]`
	handler := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(list)) }

	out, _, err := runResource(t, handler, "", "datasets", "list", "-o", "json", "--fields", "dataset,last_activity")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"last_activity": "2026-07-19T`) || !strings.Contains(out, `"dataset": "web"`) {
		t.Fatalf("json --fields with a derived column = %q", out)
	}

	out, _, err = runResource(t, handler, "", "datasets", "list", "-o", "jsonl", "--fields", "?")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"last_activity", "metadata", "log_id"} {
		if !strings.Contains(out, name+"\n") {
			t.Errorf("--fields '?' -o jsonl missing %q: %q", name, out)
		}
	}

	out, _, err = runResource(t, handler, "", "datasets", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "last_activity") || !strings.Contains(out, "1784452890000") {
		t.Fatalf("plain -o json must stay raw: %q", out)
	}
}

// TestSingleRecordCSV pins that get renders -o csv as a one-row CSV rather
// than silently printing JSON.
func TestSingleRecordCSV(t *testing.T) {
	out, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-000000000001","name":"cpu"}`))
	}, "", "monitors", "get", "aaaaaaaa-aaaa-aaaa-aaaa-000000000001", "-o", "csv")
	if err != nil {
		t.Fatal(err)
	}
	if out != "id,name\naaaaaaaa-aaaa-aaaa-aaaa-000000000001,cpu\n" {
		t.Fatalf("get -o csv = %q", out)
	}
}

// TestResourceListRawAndDerivedFieldsTogether pins that selecting a
// derived field keeps the other requested raw fields, including for
// transforms that expand one raw row into several (permissions: one group
// into one row per permission).
func TestResourceListRawAndDerivedFieldsTogether(t *testing.T) {
	catalog := `{"resources":[{"group_name":"Monitors","permissions":[{"name":"monitors:read"},{"name":"monitors:write"}]}]}`
	out, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(catalog))
	}, "", "permissions", "list", "-o", "jsonl", "--fields", "group_name,group,name")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"group":"Monitors","group_name":"Monitors","name":"monitors:read"}` + "\n" +
		`{"group":"Monitors","group_name":"Monitors","name":"monitors:write"}` + "\n"
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}

	list := `[{"dataset":"web","updated_at":1777890000000,"metadata":{"last_heartbeat_at":1784452890000}}]`
	out, _, err = runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(list))
	}, "", "datasets", "list", "-o", "jsonl", "--fields", "dataset,updated_at,last_activity")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"updated_at":1777890000000`) || !strings.Contains(out, `"last_activity":"2026-`) {
		t.Fatalf("raw value lost next to its derived column: %q", out)
	}
}
