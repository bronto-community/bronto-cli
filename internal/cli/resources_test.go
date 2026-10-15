package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

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
	}, "", "datasets", "update", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000d1", "-f", "collection=prod", "-f", "dataset=http2")
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
		// The live shape (spec GetMonitorEventsResponse): events plus a
		// groups_history array, which must not turn the output into one
		// row of two JSON blobs.
		_, _ = w.Write([]byte(`{"monitor_events":[{"monitor_id":"m","time":1784452890000,"monitor_status":"ALERT"},` +
			`{"monitor_id":"m","time":1784453160000,"monitor_status":"OK"}],` +
			`"groups_history":[{"monitor_id":"m","group_id":"g","history":[]}]}`))
	}, "", "monitors", "events", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 2 || rows[0]["monitor_status"] != "ALERT" {
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

// groups update is a PATCH that blanks whichever of name/description the
// body omits (live-verified), so the CLI refuses a partial body.
func TestGroupsUpdateRequiresNameAndDescription(t *testing.T) {
	const id = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f1"
	for _, fields := range [][]string{{"-f", "description=x"}, {"-f", "name=x"}} {
		_, _, err := runResource(t, func(_ http.ResponseWriter, r *http.Request) {
			t.Errorf("partial update must not call the API: %s %s", r.Method, r.URL.Path)
		}, "", append([]string{"groups", "update", id}, fields...)...)
		if codeOf(err) != "usage_missing_fields" {
			t.Fatalf("%v: err = %v", fields, err)
		}
		var ce *clierr.Error
		if !errors.As(err, &ce) || !strings.Contains(ce.Hint, "omitted field is cleared") {
			t.Fatalf("%v: hint = %+v", fields, ce)
		}
	}
	var gotMethod, gotBody string
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotMethod, gotBody = r.Method, string(b)
		_, _ = w.Write([]byte(`{"group_id":"` + id + `"}`))
	}, "", "groups", "update", id, "-f", "name=n", "-f", "description=d")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPatch || gotBody != `{"description":"d","name":"n"}` {
		t.Fatalf("%s %s", gotMethod, gotBody)
	}
}

func TestDatasetsUpdateRequiresCollectionAndDataset(t *testing.T) {
	_, _, err := runResource(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("partial update must not call the API: %s %s", r.Method, r.URL.Path)
	}, "", "datasets", "update", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000d1", "-f", "dataset=x")
	if codeOf(err) != "usage_missing_fields" || !strings.Contains(err.Error(), "collection") {
		t.Fatalf("err = %v", err)
	}
}

// Downtime rows carry downtime_id, not id: without IDKey a name matched
// a row with no usable id and reported not-found.
func TestDowntimesResolveByName(t *testing.T) {
	const id = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000b7"
	var deleted string
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/monitors/downtimes":
			_, _ = w.Write([]byte(`[{"downtime_id":"` + id + `","name":"weekly"}]`))
		case r.Method == http.MethodDelete:
			deleted = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}, "", "monitors", "downtimes", "delete", "weekly", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != "/monitors/downtimes/"+id {
		t.Fatalf("deleted %q", deleted)
	}
}

func TestCheckMuteFlags(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	future, past := now.UnixMilli()+60_000, now.UnixMilli()-60_000
	cases := []struct {
		name            string
		until           int64
		unmute, set     bool
		code, hintSubst string
	}{
		{"default mutes forever", -1, false, false, "", ""},
		{"explicit -1", -1, false, true, "", ""},
		{"future", future, false, true, "", ""},
		{"zero would unmute", 0, false, true, "usage_invalid_flags", "--unmute"},
		{"past", past, false, true, "usage_invalid_flags", "future"},
		{"negative", -5, false, true, "usage_invalid_flags", "-1 to mute indefinitely"},
		{"seconds", now.Unix() + 60, false, true, "usage_invalid_flags", "looks like seconds"},
		{"unmute", -1, true, false, "", ""},
		{"unmute with until", future, true, true, "usage_conflicting_flags", ""},
	}
	for _, c := range cases {
		err := checkMuteFlags(c.until, c.unmute, c.set, now)
		if codeOf(err) != c.code {
			t.Errorf("%s: err = %v, want code %q", c.name, err, c.code)
			continue
		}
		var ce *clierr.Error
		if c.hintSubst != "" && (!errors.As(err, &ce) || !strings.Contains(ce.Hint, c.hintSubst)) {
			t.Errorf("%s: hint = %+v, want it to mention %q", c.name, ce, c.hintSubst)
		}
	}
	// An expired millisecond timestamp has the right units: no seconds hint.
	var ce *clierr.Error
	if err := checkMuteFlags(past, false, true, now); !errors.As(err, &ce) || strings.Contains(ce.Hint, "looks like seconds") {
		t.Errorf("past ms timestamp: err = %v, hint = %+v; want no seconds hint", err, ce)
	}
}

// An invalid --until is refused before any request: --until 0 used to
// send the unmute value and print "Muted".
func TestMonitorsMuteRejectsBadUntil(t *testing.T) {
	for _, args := range [][]string{
		{"--until", "0"},
		{"--until", "1000"},
		{"--unmute", "--until", "-1"},
	} {
		_, _, err := runResource(t, func(_ http.ResponseWriter, r *http.Request) {
			t.Errorf("%v must not call the API: %s %s", args, r.Method, r.URL.Path)
		}, "", append([]string{"monitors", "mute", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1"}, args...)...)
		if clierr.ExitCode(err) != 2 {
			t.Fatalf("%v: err = %v, want a usage error", args, err)
		}
	}
}

// api-keys create's response is the only place the full key appears, so
// create tells the user to store it (stderr, honoring --quiet and silent
// under --dry-run).
func TestAPIKeysCreateNotice(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"k1","api_key":"uuid.secret"}`))
	}
	out, stderr, err := runResource(t, handler, "", "api-keys", "create", "-f", "name=n", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "uuid.secret") {
		t.Fatalf("create must print the full key: %q", out)
	}
	if !strings.Contains(stderr, "shown only once") {
		t.Fatalf("stderr = %q", stderr)
	}
	for _, extra := range []string{"--quiet", "--dry-run"} {
		_, stderr, err = runResource(t, handler, "", "api-keys", "create", "-f", "name=n", "-o", "json", extra)
		if err != nil {
			t.Fatal(err)
		}
		if stderr != "" {
			t.Fatalf("%s stderr = %q", extra, stderr)
		}
	}
}

// dashboards list rows carry an empty widget_ids live even when widgets
// are attached, so the list view has no widget count column.
func TestDashboardsListHasNoWidgetCount(t *testing.T) {
	out, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"d1","name":"ops","widget_ids":[]}]`))
	}, "", "dashboards", "list", "-o", "table")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "WIDGETS") || !strings.Contains(out, "ops") {
		t.Fatalf("out = %q", out)
	}
}

// TestResourcesGuideListsPutUpdates keeps the managing-resources guide's
// list of full-replacement (PUT) updates in step with the registry.
func TestResourcesGuideListsPutUpdates(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(docsRepoRoot(t), "docs", "cli", "guides", "resources.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(string(data)), " ")
	_, after, ok := strings.Cut(text, "Updates to ")
	sentence, _, ok2 := strings.Cut(after, " use HTTP PUT")
	if !ok || !ok2 {
		t.Fatal(`resources.mdx has no "Updates to … use HTTP PUT" sentence`)
	}
	var documented []string
	for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(sentence, -1) {
		documented = append(documented, m[1])
	}
	var want []string
	for _, d := range resourceRegistry {
		if !d.NoUpdate && d.updateMethod() == http.MethodPut {
			want = append(want, d.display())
		}
	}
	slices.Sort(documented)
	slices.Sort(want)
	if !slices.Equal(documented, want) {
		t.Fatalf("resources.mdx lists PUT updates %v, the registry has %v", documented, want)
	}
}
