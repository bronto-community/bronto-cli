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
	_, stderr, err := runResource(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be contacted under --dry-run")
	}, "", "users", "deactivate", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "DRY RUN: would deactivate user aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1.") {
		t.Fatalf("dry-run stderr = %q", stderr)
	}
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
