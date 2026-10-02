package cli

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
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

// runSearchArgs runs search against a stub returning respond and returns
// stdout and the error.
func runSearchArgs(t *testing.T, respond string, args ...string) (string, error) {
	t.Helper()
	srv := searchServer(t, respond, nil)
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"search", "status >= 500", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"}, args...))
	err := root.Execute()
	return out.String(), err
}

// TestSearchFieldsBareNameResolvesMessageKVs pins: search events come back
// flattened as message_kvs.<field>, so --fields status must find
// message_kvs.status and print it under the name the user asked for
// (matching tail --fields, whose keys are bare).
func TestSearchFieldsBareNameResolvesMessageKVs(t *testing.T) {
	const resp = `{"events":[{"@raw":"e1","@time":"t1","message_kvs":{"status":502,"host":"web-1"}}]}`
	out, err := runSearchArgs(t, resp, "--fields", "@time,status,host", "-o", "jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out), `{"@time":"t1","host":"web-1","status":502}`; got != want {
		t.Fatalf("jsonl = %q, want %q", got, want)
	}

	// The full flattened name keeps working, under its own name.
	out, err = runSearchArgs(t, resp, "--fields", "message_kvs.status", "-o", "jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out), `{"message_kvs.status":502}`; got != want {
		t.Fatalf("jsonl = %q, want %q", got, want)
	}

	// Table, csv and -x resolve the same way.
	out, err = runSearchArgs(t, resp, "--fields", "status,host", "-o", "csv")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out), "status,host\n502,web-1"; got != want {
		t.Fatalf("csv = %q, want %q", got, want)
	}
	out, err = runSearchArgs(t, resp, "--fields", "status", "-o", "table")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out), "STATUS\n502"; got != want {
		t.Fatalf("table = %q, want %q", got, want)
	}
	out, err = runSearchArgs(t, resp, "--fields", "status", "-o", "table", "-x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "status  502") {
		t.Fatalf("expanded = %q", out)
	}
}

// TestSearchFieldsExactKeyWins pins: when an event carries both a
// top-level key and message_kvs.<key>, the exact key is used.
func TestSearchFieldsExactKeyWins(t *testing.T) {
	const resp = `{"events":[{"@raw":"e1","@time":"t1","host":"top","message_kvs":{"host":"kv"}}]}`
	out, err := runSearchArgs(t, resp, "--fields", "host", "-o", "jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out), `{"host":"top"}`; got != want {
		t.Fatalf("jsonl = %q, want %q", got, want)
	}
}

// TestSearchFieldFallbackOnlyForRequestedFields pins: the message_kvs.
// fallback applies only to names the user passed via --fields. Unfiltered
// table/csv output looks up columns by exact key, so a sparse row that
// lacks top-level host shows an empty host cell (as JSON shows it absent)
// rather than borrowing message_kvs.host.
func TestSearchFieldFallbackOnlyForRequestedFields(t *testing.T) {
	const resp = `{"events":[` +
		`{"@raw":"e1","@time":"t1","host":"top"},` +
		`{"@raw":"e2","@time":"t2","message_kvs":{"host":"kv"}}]}`

	out, err := runSearchArgs(t, resp, "-o", "csv")
	if err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v\n%s", err, out)
	}
	hostCol, kvCol := -1, -1
	for i, c := range recs[0] {
		switch c {
		case "host":
			hostCol = i
		case "message_kvs.host":
			kvCol = i
		}
	}
	if hostCol < 0 || kvCol < 0 || len(recs) != 3 {
		t.Fatalf("csv = %q, want host and message_kvs.host columns, 2 rows", out)
	}
	if got := recs[1][hostCol]; got != "top" {
		t.Fatalf("row 1 host = %q, want top", got)
	}
	if got := recs[2][hostCol]; got != "" {
		t.Fatalf("csv row 2 host = %q, want empty (no top-level host): %q", got, out)
	}
	if got := recs[2][kvCol]; got != "kv" {
		t.Fatalf("row 2 message_kvs.host = %q, want kv", got)
	}

	out, err = runSearchArgs(t, resp, "-o", "table")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("table = %q, want header + 2 rows", out)
	}
	if n := strings.Count(lines[2], "kv"); n != 1 {
		t.Fatalf("table row 2 shows kv %d times, want once (message_kvs.host only): %q", n, out)
	}

	// With --fields host the fallback is what the user asked for.
	out, err = runSearchArgs(t, resp, "--fields", "host", "-o", "csv")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out), "host\ntop\nkv"; got != want {
		t.Fatalf("--fields csv = %q, want %q", got, want)
	}
}

// TestSearchJQSeesFieldsProjection pins: --jq runs on the row AFTER
// --fields projection, so it sees the output keys the user requested
// (.host), not the full flattened event (."message_kvs.host" is gone).
// Without --fields, --jq sees the full flattened keys.
func TestSearchJQSeesFieldsProjection(t *testing.T) {
	const resp = `{"events":[{"@raw":"e1","@time":"t1","message_kvs":{"host":"web-1"}}]}`
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--jq", `."message_kvs.host"`}, `"web-1"`},
		{[]string{"--jq", `.host`}, `null`},
		{[]string{"--fields", "host", "--jq", `.host`}, `"web-1"`},
		{[]string{"--fields", "host", "--jq", `."message_kvs.host"`}, `null`},
	} {
		out, err := runSearchArgs(t, resp, tc.args...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if got := strings.TrimSpace(out); got != tc.want {
			t.Fatalf("%v: out = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// TestRawOutputPrintsStringsUnquoted pins --raw-output / -r (jq -r):
// string results print without quotes, other values as compact JSON, one
// per line.
func TestRawOutputPrintsStringsUnquoted(t *testing.T) {
	const resp = `{"events":[{"@raw":"e1","@time":"t1","message_kvs":{"status":502}},{"@raw":"e2","@time":"t2","message_kvs":{"status":200}}]}`
	for _, flag := range []string{"--raw-output", "-r"} {
		out, err := runSearchArgs(t, resp, "--jq", `.["@raw"], .["message_kvs.status"], {a: 1}`, flag)
		if err != nil {
			t.Fatalf("%s: %v", flag, err)
		}
		if got, want := out, "e1\n502\n{\"a\":1}\ne2\n200\n{\"a\":1}\n"; got != want {
			t.Fatalf("%s: out = %q, want %q", flag, got, want)
		}
	}
}

// TestRawOutputOnPrintJSON pins -r on a non-streaming command (PrintJSON
// path): an id comes out bare for $(...) capture.
func TestRawOutputOnPrintJSON(t *testing.T) {
	out, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1","name":"cpu"}`))
	}, "", "monitors", "get", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1", "--jq", ".id", "-r")
	if err != nil {
		t.Fatal(err)
	}
	if out != "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1\n" {
		t.Fatalf("out = %q", out)
	}
}

// TestRawOutputWithoutJQIsUsageError pins: --raw-output only changes how
// --jq results print, so on its own it is rejected rather than ignored.
func TestRawOutputWithoutJQIsUsageError(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "x", "-d", "11111111-1111-1111-1111-111111111111",
		"--api-key", "k", "-r"})
	err := root.Execute()
	if got := clierr.ExitCode(err); err == nil || got != 2 || !strings.Contains(err.Error(), "--raw-output requires --jq") {
		t.Fatalf("want usage error, got %v (exit %d)", err, got)
	}
}
