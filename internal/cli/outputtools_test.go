package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// TestJQInvalidExpressionIsUsageErrorBeforeNetwork pins: a bad --jq
// expression must fail as a usage error (exit 2) at flag-parsing time,
// before any request is attempted — no httptest server is started, so a
// leaked network call would fail the test with a connection error instead
// of the expected usage error.
func TestJQInvalidExpressionIsUsageErrorBeforeNetwork(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "x", "-d", "11111111-1111-1111-1111-111111111111",
		"--api-key", "k", "--jq", "this is not { valid jq"})
	err := root.Execute()
	if err == nil {
		t.Fatal("want error for invalid jq expression")
	}
	if got := clierr.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode = %d, want 2: %v", got, err)
	}
}

// TestJQWithTableFormatIsUsageError pins: --jq combined with an explicit
// non-machine format is rejected before any request is attempted.
func TestJQWithTableFormatIsUsageError(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "x", "-d", "11111111-1111-1111-1111-111111111111",
		"--api-key", "k", "--jq", ".", "-o", "table"})
	err := root.Execute()
	if err == nil {
		t.Fatal("want error for --jq with -o table")
	}
	if got := clierr.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode = %d, want 2: %v", got, err)
	}
}

// TestSearchJQOnRawField pins: --jq applied to search's jsonl/streaming
// output extracts a field from every event, printing each result as its own
// JSON value (a bare string here, since "@raw" holds a string).
func TestSearchJQOnRawField(t *testing.T) {
	srv := searchServer(t, `{"events":[{"@raw":"e1","@time":"t1"},{"@raw":"e2","@time":"t2"}]}`, nil)
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "status >= 500", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k", "--jq", `.["@raw"]`})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), out.String())
	}
	for i, want := range []string{`"e1"`, `"e2"`} {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}
}

// TestSearchFieldsQuestionMarkListsFieldNames pins: --fields ? lists the
// available field names instead of data rows.
func TestSearchFieldsQuestionMarkListsFieldNames(t *testing.T) {
	srv := searchServer(t, `{"events":[{"@raw":"e1","@time":"t1","host":"web-1"},{"@raw":"e2","@time":"t2"}]}`, nil)
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "status >= 500", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k", "--fields", "?"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(out.String())
	want := "@raw\n@time\nhost"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// Guard against accidentally matching JSON/data output.
	var probe any
	if json.Unmarshal(out.Bytes(), &probe) == nil {
		t.Fatalf("output looks like JSON data, want plain field names: %q", out.String())
	}
}

// TestSearchFieldFilterSelectsColumns pins: --fields <a,b> restricts json
// output to those keys.
func TestSearchFieldFilterSelectsColumns(t *testing.T) {
	srv := searchServer(t, `{"events":[{"@raw":"e1","@time":"t1","host":"web-1"}]}`, nil)
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "status >= 500", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k", "--fields", "host", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, out.String())
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if _, ok := rows[0]["@raw"]; ok {
		t.Fatalf("@raw should be filtered out: %+v", rows[0])
	}
	if rows[0]["host"] != "web-1" {
		t.Fatalf("host = %v", rows[0]["host"])
	}
}
