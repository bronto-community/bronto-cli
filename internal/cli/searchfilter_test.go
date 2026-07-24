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

// codeOf extracts the clierr code from an error, or "" if it isn't one.
func codeOf(err error) string {
	var ce *clierr.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

func TestFilterFlagSetSplitsOnFirstEquals(t *testing.T) {
	var got []filterClause
	f := &filterFlag{op: "=", dst: &got}
	if err := f.Set("field=a=b"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].field != "field" || got[0].value != "a=b" || got[0].op != "=" {
		t.Fatalf("clause = %+v", got)
	}
	if err := f.Set("noequals"); err == nil {
		t.Fatal("missing = must error")
	}
	if err := f.Set("=v"); err == nil {
		t.Fatal("empty field must error")
	}
}

func TestQuoteFilterValue(t *testing.T) {
	cases := []struct{ op, val, want string }{
		{"=", "claude-fable-5", "'claude-fable-5'"},
		{"=", "1000", "1000"},
		{">", "1000", "1000"},
		{">", "12.5", "12.5"},
		{"=", "true", "true"},
		{"=", "false", "false"},
		{"~", "err.*", "'err.*'"},      // regex always quoted
		{"~", "500", "'500'"},          // regex quoted even when numeric-looking
		{"=", "O'Brien", "'O''Brien'"}, // SQL-style escaping
	}
	for _, c := range cases {
		if got := quoteFilterValue(c.op, c.val); got != c.want {
			t.Errorf("quoteFilterValue(%q,%q) = %q, want %q", c.op, c.val, got, c.want)
		}
	}
}

func TestResolveFilterField(t *testing.T) {
	index := []string{"$model", "$final_model", "$duration_ms", "$model_swapped"}

	// exact match
	if got, err := resolveFilterField(index, "$model", false); err != nil || got != "$model" {
		t.Fatalf("exact: %q %v", got, err)
	}
	// normalized: drop $, case-insensitive; "model" -> only "$model"
	if got, err := resolveFilterField(index, "MODEL", false); err != nil || got != "$model" {
		t.Fatalf("normalized: %q %v", got, err)
	}
	// unknown -> usage_unknown_field
	_, err := resolveFilterField(index, "mdl", false)
	if code := codeOf(err); code != "usage_unknown_field" {
		t.Fatalf("unknown code = %q", code)
	}
	// verbatim (no index) returns name unchanged
	if got, err := resolveFilterField(nil, "whatever", true); err != nil || got != "whatever" {
		t.Fatalf("verbatim: %q %v", got, err)
	}
}

func TestResolveFilterFieldAmbiguous(t *testing.T) {
	// two fields sharing a normalized form, with no exact match for the
	// query -> hard error (exact match would otherwise win deterministically)
	index := []string{"$model", "model"}
	_, err := resolveFilterField(index, "MODEL", false)
	if code := codeOf(err); code != "usage_ambiguous_field" {
		t.Fatalf("ambiguous code = %q (err %v)", code, err)
	}
}

func TestUnknownFieldErrorSuggests(t *testing.T) {
	index := []string{"$model", "$duration_ms", "$session.id"}
	// typo caught by Damerau-Levenshtein on normalized names ($ stripped)
	err := unknownFieldError("mdl", index)
	var ce *clierr.Error
	if !errors.As(err, &ce) || !strings.Contains(ce.Hint, "$model") {
		t.Fatalf("hint = %q", ce.Hint)
	}
	// substring match preferred: "session" -> $session.id
	err = unknownFieldError("session", index)
	_ = errors.As(err, &ce)
	if !strings.Contains(ce.Hint, "$session.id") {
		t.Fatalf("substring hint = %q", ce.Hint)
	}
}

func TestAndWhere(t *testing.T) {
	if got := andWhere("", "$a = 1"); got != "$a = 1" {
		t.Errorf("empty positional: %q", got)
	}
	if got := andWhere("status >= 500", ""); got != "status >= 500" {
		t.Errorf("empty filters: %q", got)
	}
	if got := andWhere("status >= 500", "$a = 1"); got != "(status >= 500) AND $a = 1" {
		t.Errorf("both: %q", got)
	}
}

// filterSearchServer serves both /top-keys (field index) and /search
// (captures the request body), for end-to-end filter-flag tests.
func filterSearchServer(t *testing.T, topKeys, searchResp string, capture *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/top-keys":
			_, _ = w.Write([]byte(topKeys))
		case "/search":
			b, _ := io.ReadAll(r.Body)
			if capture != nil {
				_ = json.Unmarshal(b, capture)
			}
			_, _ = w.Write([]byte(searchResp))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
}

const filterTopKeys = `{"log-a":{
	"$model":{"type":"STRING","field_type":"ATTRIBUTE"},
	"$duration_ms":{"type":"STRING","field_type":"ATTRIBUTE"},
	"$final_model":{"type":"STRING","field_type":"ATTRIBUTE"}}}`

func TestSearchFilterFlagsBuildWhere(t *testing.T) {
	var body map[string]any
	srv := filterSearchServer(t, filterTopKeys, `{"events":[{"@raw":"e1","@time":"t1"}]}`, &body)
	defer srv.Close()

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "-d", "11111111-1111-1111-1111-111111111111",
		"--eq", "model=claude-fable-5", "--gt", "duration_ms=1000",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	// friendly names resolved ($-prefixed), string quoted, number bare, ANDed
	// in flag order.
	if body["where"] != "$model = 'claude-fable-5' AND $duration_ms > 1000" {
		t.Fatalf("where = %q", body["where"])
	}
}

func TestSearchFilterComposesWithPositional(t *testing.T) {
	var body map[string]any
	srv := filterSearchServer(t, filterTopKeys, `{"events":[]}`, &body)
	defer srv.Close()

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "status >= 500", "-d", "11111111-1111-1111-1111-111111111111",
		"--eq", "model=claude-fable-5",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if body["where"] != "(status >= 500) AND $model = 'claude-fable-5'" {
		t.Fatalf("where = %q", body["where"])
	}
}

func TestSearchShowQueryPrintsAndExits(t *testing.T) {
	// /search must NOT be hit: --show-query exits after compiling.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			t.Errorf("search must not run under --show-query")
		}
		if r.URL.Path == "/top-keys" {
			_, _ = w.Write([]byte(filterTopKeys))
		}
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "-d", "11111111-1111-1111-1111-111111111111",
		"--eq", "model=claude-fable-5", "--show-query",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "$model = 'claude-fable-5'" {
		t.Fatalf("show-query out = %q", out.String())
	}
}

func TestSearchFilterExactSkipsResolution(t *testing.T) {
	// --exact must not hit /top-keys and must use the name verbatim.
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/top-keys" {
			t.Errorf("--exact must not fetch the field index")
		}
		if r.URL.Path == "/search" {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			_, _ = w.Write([]byte(`{"events":[]}`))
		}
	}))
	defer srv.Close()

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "-d", "11111111-1111-1111-1111-111111111111",
		"--eq", "$model=claude-fable-5", "--exact",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if body["where"] != "$model = 'claude-fable-5'" {
		t.Fatalf("where = %q", body["where"])
	}
}

func TestSearchFilterUnknownFieldErrors(t *testing.T) {
	srv := filterSearchServer(t, filterTopKeys, `{"events":[]}`, nil)
	defer srv.Close()

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "-d", "11111111-1111-1111-1111-111111111111",
		"--eq", "nonexistent=x",
		"--base-url", srv.URL, "--api-key", "k"})
	err := root.Execute()
	if code := codeOf(err); code != "usage_unknown_field" {
		t.Fatalf("code = %q (err %v)", code, err)
	}
}

func TestSearchFilterRejectedWithLocal(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "--local", "-", "--eq", "model=x", "--api-key", "k"})
	err := root.Execute()
	if code := codeOf(err); code != "usage_invalid_flags" {
		t.Fatalf("code = %q (err %v)", code, err)
	}
}
