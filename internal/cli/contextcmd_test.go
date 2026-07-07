package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

func TestContextFetchesAndStreams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/context" {
			t.Errorf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("sequence") != "12345" || q.Get("from") != "ds-1" ||
			q.Get("timestamp") != "1700000000000" || q.Get("direction") != "both" || q.Get("limit") != "20" {
			t.Errorf("query = %v", q)
		}
		_, _ = w.Write([]byte(`{"events":[{"@raw":"before-line"},{"@raw":"anchor"},{"@raw":"after-line"}]}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"context", "--sequence", "12345", "-d", "ds-1",
		"--timestamp", "1700000000000", "-n", "20",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 jsonl lines, got %q", out.String())
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &ev); err != nil || ev["@raw"] != "anchor" {
		t.Fatalf("line1 = %q", lines[1])
	}
}

func TestContextRejectsBadDirection(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"context", "--sequence", "1", "-d", "x", "--timestamp", "1",
		"--direction", "sideways", "--api-key", "k"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v", err)
	}
}

func TestContextRejectsMissingRequiredFlag(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"context", "--api-key", "k"})
	err := root.Execute()
	err = wrapExecuteErrorForTest(err)
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2 for missing required flag, got %v (exit %d)", err, clierr.ExitCode(err))
	}
}

func wrapExecuteErrorForTest(err error) error {
	if err == nil {
		return nil
	}
	errMsg := err.Error()
	if strings.HasPrefix(errMsg, "required flag(s)") {
		return clierr.New("usage_missing_flag", errMsg).
			WithHint("Run 'bronto --help' for usage.")
	}
	return err
}
