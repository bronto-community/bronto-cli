package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

const localFixture = `{"level":"error","status":502,"path":"/api/v1/checkout","gateway":"stripe"}
{"level":"info","status":200,"path":"/health"}
plain text line with timeout error
{"level":"error","status":500,"seq":4367602734065516544}
`

func writeLocalFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dump.jsonl")
	if err := os.WriteFile(p, []byte(localFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runLocal(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	if stdin != "" {
		root.SetIn(strings.NewReader(stdin))
	}
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestLocalSearchFiltersNDJSON(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	p := writeLocalFixture(t)
	out, err := runLocal(t, "", "search", "--local", p, "status >= 500")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 matches, got %d:\n%s", len(lines), out)
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil || ev["gateway"] != "stripe" {
		t.Fatalf("line0 = %q", lines[0])
	}
	// no auth, no server: --local must work fully offline (no --api-key set)
}

func TestLocalSearchInt64Fidelity(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	p := writeLocalFixture(t)
	out, err := runLocal(t, "", "search", "--local", p, "status = 500", "-o", "jsonl", "--jq", ".seq")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "4367602734065516544" {
		t.Fatalf("int64 corrupted: %q", out)
	}
}

func TestLocalSearchPlainLinesAndRegex(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	p := writeLocalFixture(t)
	out, err := runLocal(t, "", "search", "--local", p, "@raw ~ 'timeout'")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "plain text line with timeout error") || strings.Contains(out, "stripe") {
		t.Fatalf("regex over @raw wrong:\n%s", out)
	}
}

func TestLocalSearchStdinAndEmptyQuery(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	out, err := runLocal(t, localFixture, "search", "--local", "-")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Split(strings.TrimSpace(out), "\n")); got != 4 {
		t.Fatalf("empty query must match all 4 lines, got %d:\n%s", got, out)
	}
}

func TestLocalSearchUsageErrors(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	p := writeLocalFixture(t)
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"search", "--local", p, "-d", "x", "q"}, "usage_invalid_flags"},
		{[]string{"search", "--local", p, "--since", "1h", "q"}, "usage_invalid_flags"},
		{[]string{"search", "--local", p, "--select", "count()", "q"}, "usage_invalid_flags"},
		{[]string{"search", "--local", "-", "-"}, "usage_invalid_flags"},
		// server-only views and ordering are rejected, not silently ignored
		{[]string{"search", "--local", p, "--histogram"}, "usage_invalid_flags"},
		{[]string{"search", "--local", p, "--patterns"}, "usage_invalid_flags"},
		{[]string{"search", "--local", p, "--url"}, "usage_invalid_flags"},
		{[]string{"search", "--local", p, "--open"}, "usage_invalid_flags"},
		{[]string{"search", "--local", p, "--order-by", "status DESC"}, "usage_invalid_flags"},
		{[]string{"search", "--local", p, "--oldest-first"}, "usage_invalid_flags"},
		{[]string{"search", "--local", p, "--saved", "s"}, "usage_invalid_flags"},
		{[]string{"search", "--local", p, "status >>= 5"}, "usage_invalid_query"},
		{[]string{"search", "--local", filepath.Join(t.TempDir(), "missing.jsonl"), "q = 1"}, "local_file_error"},
	} {
		_, err := runLocal(t, "", tc.args...)
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != tc.code {
			t.Fatalf("%v: want %s, got %v", tc.args, tc.code, err)
		}
	}
}

// A bronto search -o jsonl dump carries parsed fields as message_kvs.<f>;
// the bare name the server query uses must match them too, while an
// exact top-level key still wins.
func TestLocalSearchMessageKVsFallback(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	dump := `{"@time":"t1","message_kvs.status":"502","message_kvs.level":"error"}
{"@time":"t2","message_kvs.status":"200","message_kvs.level":"info"}
{"@time":"t3","status":"404","message_kvs.status":"500"}
`
	out, err := runLocal(t, dump, "search", "--local", "-", "status >= 500", "-o", "jsonl", "--jq", ".\"@time\"")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(out); len(got) != 1 || got[0] != `"t1"` {
		t.Fatalf("matches = %q, want only t1", out)
	}
}

func TestLocalSearchParseErrorHasCaret(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	p := writeLocalFixture(t)
	_, err := runLocal(t, "", "search", "--local", p, "status >")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_invalid_query" || !strings.Contains(ce.Hint, "^") {
		t.Fatalf("want caret hint, got %v (hint=%q)", err, func() string {
			if ce != nil {
				return ce.Hint
			}
			return ""
		}())
	}
}
