package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestUsageLive asserts `bronto usage` returns clean JSON against the live
// /usage endpoint (previously zero live coverage; its response shape broke
// the human view once already — see the v0.1.1 output overhaul).
func TestUsageLive(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)
	res, err := r.Run(t.Context(), "", "usage", "--since", "1h", "-o", "json")
	if err != nil {
		t.Fatalf("running usage: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("usage exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	var payload any
	if err := json.Unmarshal([]byte(res.Stdout), &payload); err != nil {
		t.Fatalf("usage -o json did not parse: %v\noutput: %s", err, res.Stdout)
	}
}

// TestParsersListTolerant covers the parsers read path live. /parsers is in
// the specLiveButUndocumented set (dropped from the published spec while
// still served) — tolerate a live 404 with a skip, mirroring traces.
func TestParsersListTolerant(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)
	res, err := r.Run(t.Context(), "", "parsers", "list", "-o", "json")
	if err != nil {
		t.Fatalf("running parsers list: %v", err)
	}
	if res.ExitCode == 4 && strings.Contains(res.Stderr, "resource_not_found") {
		t.Skipf("live API no longer serves /parsers (404) — drop the command and this skip: %s", strings.TrimSpace(res.Stderr))
	}
	if res.ExitCode != 0 {
		t.Fatalf("parsers list exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("parsers list -o json did not parse as array: %v\noutput: %s", err, res.Stdout)
	}
}

// TestFieldsLimit asserts -n caps the number of keys returned.
func TestFieldsLimit(t *testing.T) {
	key := skipIfNoCreds(t)
	dataset, _ := seededData(t)
	r := NewRunner(t, key)
	logID := logIDForDataset(t, r, dataset)

	res := mustExitZero(t, r, "fields", "-d", logID, "--since", "1h", "-n", "3", "-o", "json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("fields -o json did not parse: %v\noutput: %s", err, res.Stdout)
	}
	if len(rows) > 3 {
		t.Fatalf("fields -n 3 returned %d rows", len(rows))
	}
}

// TestTailNoFollowSeeded proves tail actually streams seeded data: one
// non-following poll over a window that includes the seed batch must
// surface the marker (previously only tail's SIGINT handling was live-
// tested, never its data path).
func TestTailNoFollowSeeded(t *testing.T) {
	key := skipIfNoCreds(t)
	dataset, marker := seededData(t)
	r := NewRunner(t, key)
	logID := logIDForDataset(t, r, dataset)

	res, err := r.Run(t.Context(), "",
		"tail", fmt.Sprintf("ci_marker = '%s'", marker),
		"-d", logID, "--window", "1h", "--no-follow", "-o", "jsonl")
	if err != nil {
		t.Fatalf("running tail: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("tail exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, marker) {
		t.Fatalf("tail --no-follow output does not contain the seeded marker\nstdout: %s\nstderr: %s", res.Stdout, res.Stderr)
	}
}

// TestMonitorsTestTolerant covers `monitors test` live. Its endpoint
// (/monitors/send-test-notifications) is live-but-undocumented after the
// 2026-07-17 spec reorg — tolerate a 404 with a skip. The test account is
// throwaway; test notifications go to the transient monitors' example.com
// email actions at worst.
func TestMonitorsTestTolerant(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)
	res, err := r.Run(t.Context(), "", "monitors", "test")
	if err != nil {
		t.Fatalf("running monitors test: %v", err)
	}
	if res.ExitCode == 4 && strings.Contains(res.Stderr, "resource_not_found") {
		t.Skipf("live API no longer serves send-test-notifications (404) — drop the command and this skip: %s", strings.TrimSpace(res.Stderr))
	}
	if res.ExitCode != 0 {
		t.Fatalf("monitors test exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
}

// TestAuthLoginKeyStdin exercises the full credential round trip with NO
// env key: login --key-stdin stores the key (file fallback on CI runners
// without a keychain), and auth status must then resolve it from the store.
func TestAuthLoginKeyStdin(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)
	r.OmitEnvKey = true

	res, err := r.Run(t.Context(), key+"\n",
		"auth", "login", "--key-stdin", "--region", regionOrDefault())
	if err != nil {
		t.Fatalf("running auth login: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("auth login exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "Logged in") {
		t.Fatalf("login confirmation missing: %q", res.Stderr)
	}

	status := mustRunJSONArray(t, r, "auth", "status", "-o", "json")
	if len(status) == 0 {
		t.Fatal("auth status returned no rows after login")
	}
	if s, _ := status[0]["status"].(string); s != "ok" {
		t.Fatalf("auth status after login = %+v", status[0])
	}
}

// TestPluginDispatch is hermetic (no creds): an executable bronto-<name>
// on PATH must be dispatched with the remaining args, and its exit code
// must pass through verbatim.
func TestPluginDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("plugin script uses a POSIX shebang")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "bronto-hello")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"plugin-ran $*\"\nexit 7\n"), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}

	r := NewRunner(t, "")
	r.ExtraEnv = []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")}
	res, err := r.Run(t.Context(), "", "hello", "a", "b")
	if err != nil {
		t.Fatalf("running plugin dispatch: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("plugin exit code = %d, want 7\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "plugin-ran a b") {
		t.Fatalf("plugin stdout = %q", res.Stdout)
	}
}
