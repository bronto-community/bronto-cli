package cli

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func runQueryCheck(t *testing.T, srvURL string, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	full := append([]string{"query", "check"}, args...)
	if srvURL != "" {
		full = append(full, "--api-key", "k", "--base-url", srvURL)
	}
	root.SetArgs(full)
	err := root.Execute()
	return out.String(), errBuf.String(), err
}

func TestQueryCheckValid(t *testing.T) {
	out, _, err := runQueryCheck(t, "", "status >= 500 AND level = 'error'")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "✓ valid") || !strings.Contains(out, "level, status") {
		t.Fatalf("out = %q", out)
	}
}

func TestQueryCheckSyntaxErrorCaret(t *testing.T) {
	_, _, err := runQueryCheck(t, "", "status >")
	var ce *clierr.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "usage_invalid_query" || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage_invalid_query exit 2, got %v", err)
	}
	if !strings.Contains(ce.Hint, "^") {
		t.Fatalf("hint must carry the caret: %q", ce.Hint)
	}
}

func TestQueryCheckUnknownFieldSuggests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/logs":
			_, _ = w.Write([]byte(`{"logs":[{"log":"web","collection":"prod","log_id":"11111111-1111-1111-1111-111111111111"}]}`))
		case "/top-keys":
			_, _ = w.Write([]byte(`{"11111111-1111-1111-1111-111111111111":{"status":{"rank":1,"type":"NUMBER"},"level":{"rank":1,"type":"STRING"}}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	_, stderr, err := runQueryCheck(t, srv.URL, "stauts >= 500", "-d", "web")
	if err != nil {
		t.Fatalf("non-strict unknown field must warn, not fail: %v", err)
	}
	if !strings.Contains(stderr, `did you mean "status"`) {
		t.Fatalf("stderr = %q", stderr)
	}

	_, _, err = runQueryCheck(t, srv.URL, "stauts >= 500", "-d", "web", "--strict")
	var ce *clierr.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "query_unknown_field" {
		t.Fatalf("strict mode must fail: %v", err)
	}
}

// TestSearchEnrichesServer400 pins the advisory integration: a server 400
// on a query our parser also rejects gains the caret diagnosis; parsing
// never blocks the request.
func TestSearchEnrichesServer400(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"details":"Invalid query."}`))
		}
	}))
	defer srv.Close()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "status >", "-d", "11111111-1111-1111-1111-111111111111",
		"--api-key", "k", "--base-url", srv.URL})
	err := root.Execute()
	var ce *clierr.Error
	if err == nil || !errors.As(err, &ce) {
		t.Fatal(err)
	}
	if !strings.Contains(ce.Hint, "Local query check") || !strings.Contains(ce.Hint, "^") {
		t.Fatalf("hint = %q, want local diagnosis with caret", ce.Hint)
	}
}
