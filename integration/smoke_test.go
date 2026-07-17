package integration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// --- Hermetic checks: no credentials needed, always run. These give this
// package a real self-check in plain `go test ./...` / lint CI, per the
// plan's "no //go:build tag" gating design (skipIfNoCreds skips
// individual tests at runtime, never the whole package at compile time).

func TestSmoke_HermeticBinaryBuilds(t *testing.T) {
	if binPath == "" {
		t.Fatal("binPath was not resolved by TestMain")
	}
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("resolved binary is missing: %v", err)
	}
}

func TestSmoke_HermeticHelp(t *testing.T) {
	r := NewRunner(t, "")
	res, err := r.Run(t.Context(), "", "--help")
	if err != nil {
		t.Fatalf("running --help: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("--help exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "bronto") {
		t.Fatalf("--help output doesn't mention bronto:\n%s", res.Stdout)
	}
}

// --- Live checks: need BRONTO_IT_MGMT_KEY.

func TestSmoke_Ping(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)
	res, err := r.Run(t.Context(), "", "ping")
	if err != nil {
		t.Fatalf("running ping: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ping exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
}

func TestSmoke_AuthStatus(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)
	res, err := r.Run(t.Context(), "", "auth", "status", "-o", "json")
	if err != nil {
		t.Fatalf("running auth status: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("auth status exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("auth status -o json did not parse: %v\noutput: %s", err, res.Stdout)
	}
	if len(rows) != 1 {
		t.Fatalf("auth status -o json: want 1 row, got %d: %s", len(rows), res.Stdout)
	}
	if status, _ := rows[0]["status"].(string); status != "ok" {
		t.Fatalf("auth status = %q, want %q (row: %+v)", status, "ok", rows[0])
	}
}

func TestSmoke_Version(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)
	res, err := r.Run(t.Context(), "", "--version")
	if err != nil {
		t.Fatalf("running --version: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("--version exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) == "" {
		t.Fatal("--version printed nothing")
	}
}
