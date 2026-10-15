package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	out, _, err := runQueryCheck(t, "", "status >= 500 AND level = 'error'", "-o", "table")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "✓ valid") || !strings.Contains(out, "level, status") {
		t.Fatalf("out = %q", out)
	}
}

func TestQueryCheckJSON(t *testing.T) {
	out, stderr, err := runQueryCheck(t, "", "status IN (500, 502) AND level = error", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Valid    bool
		Fields   []string
		Warnings []string
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("-o json output does not parse: %v\n%s", err, out)
	}
	if !got.Valid || !reflect.DeepEqual(got.Fields, []string{"error", "level", "status"}) ||
		len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], `attribute "error"`) {
		t.Fatalf("result = %+v", got)
	}
	if stderr != "" {
		t.Fatalf("json mode must keep warnings out of stderr: %q", stderr)
	}

	// No warnings is an empty array, not null.
	out, _, err = runQueryCheck(t, "", "slow", "-o", "json")
	if err != nil || strings.TrimSpace(out) != `{"fields":[],"valid":true,"warnings":[]}` {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestQueryCheckQuietSilencesWarnings(t *testing.T) {
	_, stderr, err := runQueryCheck(t, "", "level = error", "-o", "table")
	if err != nil || !strings.Contains(stderr, "warning: ") {
		t.Fatalf("without --quiet the warning must print: stderr = %q, err = %v", stderr, err)
	}
	out, stderr, err := runQueryCheck(t, "", "level = error", "-o", "table", "--quiet")
	if err != nil || stderr != "" || !strings.Contains(out, "✓ valid") {
		t.Fatalf("--quiet: out = %q, stderr = %q, err = %v", out, stderr, err)
	}
}

func TestQueryCheckJQ(t *testing.T) {
	out, _, err := runQueryCheck(t, "", "message LIKE '%fail%'", "-o", "json", "--jq", ".valid")
	if err != nil || strings.TrimSpace(out) != "true" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	out, _, err = runQueryCheck(t, "", "a = b", "-o", "json", "--jq", ".warnings | length")
	if err != nil || strings.TrimSpace(out) != "1" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestQueryCheckStdin(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader("\"slow to respond\" AND status NOT IN (200)\n"))
	root.SetArgs([]string{"query", "check", "-", "-o", "table"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "✓ valid — fields: status" {
		t.Fatalf("out = %q", got)
	}

	root = NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader("status >"))
	root.SetArgs([]string{"query", "check", "-"})
	var ce *clierr.Error
	if err := root.Execute(); !errors.As(err, &ce) || ce.Code != "usage_invalid_query" {
		t.Fatalf("stdin query must be parsed, not the literal '-': %v", err)
	}
}

// TestQueryCheckAcceptsServerSyntax runs the command over expressions the
// live API accepts (IN, LIKE, free text, …), which it used to reject.
func TestQueryCheckAcceptsServerSyntax(t *testing.T) {
	for _, q := range []string{
		"status IN (500, 502)",
		"message LIKE '%fail%'",
		`"slow to respond"`,
		"nosuchfield IS NULL",
		"LOWER(level) NOT IN ('info')",
	} {
		if _, _, err := runQueryCheck(t, "", q, "-o", "json"); err != nil {
			t.Errorf("query check %q: %v", q, err)
		}
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

	_, stderr, err := runQueryCheck(t, srv.URL, "stauts >= 500", "-d", "web", "-o", "table")
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
