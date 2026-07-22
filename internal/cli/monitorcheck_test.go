package cli

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func TestLintMonitor(t *testing.T) {
	known := map[string]bool{"11111111-1111-1111-1111-111111111111": true}
	good := map[string]any{
		"name": "m", "comparison_operator": "ABOVE", "threshold": 1.0,
		"window":  "Last 10 minutes",
		"actions": []any{map[string]any{"type": "EMAIL", "email": "a@b.c"}},
		"queries": []any{map[string]any{
			"name": "q", "select": []any{"count(*)"}, "where": "status >= 500",
			"from": []any{"11111111-1111-1111-1111-111111111111"},
		}},
	}
	if probs := lintMonitor(good, known, true); len(probs) != 0 {
		t.Fatalf("good monitor flagged: %v", probs)
	}

	bad := map[string]any{
		"comparison_operator": "NOPE",
		"window":              "Last 2 minutes",
		"actions":             []any{map[string]any{"type": "EMAIL"}},
		"queries": []any{map[string]any{
			"select": []any{}, "where": "status >",
			"from": []any{"99999999-9999-9999-9999-999999999999"},
		}},
	}
	probs := lintMonitor(bad, known, true)
	joined := strings.Join(probs, "\n")
	for _, want := range []string{
		"missing required field \"name\"",
		"missing required field \"threshold\"",
		"comparison_operator \"NOPE\"",
		"below the 5-minute minimum",
		"EMAIL action needs an email",
		"missing name",
		"select must be a non-empty array",
		"expected a value",
		"not found in this account",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems missing %q:\n%s", want, joined)
		}
	}

	// offline: dataset checks skipped, everything else still lints
	offline := lintMonitor(bad, nil, false)
	if strings.Contains(strings.Join(offline, "\n"), "not found in this account") {
		t.Fatal("dataset checks must be skipped when the account is unreachable")
	}
}

func TestLintMonitorWindowBounds(t *testing.T) {
	if p := lintMonitorWindow("Last 1 days"); len(p) != 0 {
		t.Fatalf("1 day must pass: %v", p)
	}
	if p := lintMonitorWindow("Last 2 days"); len(p) == 0 {
		t.Fatal("2 days must exceed the max")
	}
	if p := lintMonitorWindow("10 minutes"); len(p) == 0 {
		t.Fatal("missing 'Last' prefix must fail")
	}
}

// TestMonitorsCheckCommand runs the full command path: file inputs, a
// stub account for dataset checks, mixed pass/fail, exit code.
func TestMonitorsCheckCommand(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/logs" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"logs":[{"log":"web","collection":"prod","log_id":"11111111-1111-1111-1111-111111111111"}]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	_ = os.WriteFile(good, []byte(`{"name":"g","comparison_operator":"ABOVE","threshold":1,"window":"Last 10 minutes","actions":[{"type":"EMAIL","email":"a@b.c"}],"queries":[{"name":"q","select":["count(*)"],"where":"status >= 500","from":["11111111-1111-1111-1111-111111111111"]}]}`), 0o600)
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"name":"b","comparison_operator":"NOPE","threshold":1,"window":"Last 10 minutes","actions":[{"type":"EMAIL","email":"a@b.c"}],"queries":[{"name":"q","select":["count(*)"],"where":"status >= 500","from":["11111111-1111-1111-1111-111111111111"]}]}`), 0o600)

	root := NewRootCmd()
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	root.SetArgs([]string{"monitors", "check", "--input", good, "--input", bad,
		"--api-key", "k", "--base-url", srv.URL})
	err := root.Execute()
	var ce *clierr.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "monitor_check_failed" {
		t.Fatalf("want monitor_check_failed, got %v", err)
	}
	out := errBuf.String()
	if !strings.Contains(out, `✓ monitor "g"`) || !strings.Contains(out, `✗ monitor "b"`) ||
		!strings.Contains(out, "2 monitor(s) checked, 1 problem(s).") {
		t.Fatalf("output = %q", out)
	}

	// all-good input exits zero; stdin form works
	root = NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader(`{"name":"s","comparison_operator":"ABOVE","threshold":1,"window":"Last 10 minutes","actions":[{"type":"INTEGRATION"}],"queries":[{"name":"q","select":["count(*)"],"where":"","from_expr":"logset = 'x'"}]}`))
	root.SetArgs([]string{"monitors", "check", "--input", "-", "--api-key", "k", "--base-url", srv.URL})
	if err := root.Execute(); err != nil {
		t.Fatalf("clean monitor must pass: %v", err)
	}

	// invalid JSON input
	broken := filepath.Join(dir, "broken.json")
	_ = os.WriteFile(broken, []byte("{nope"), 0o600)
	root = NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"monitors", "check", "--input", broken, "--api-key", "k", "--base-url", srv.URL})
	if err := root.Execute(); err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("broken JSON must be a usage error, got %v", err)
	}

	// no --input at all
	root = NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"monitors", "check", "--api-key", "k", "--base-url", srv.URL})
	if err := root.Execute(); err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("missing input must be a usage error, got %v", err)
	}
}
