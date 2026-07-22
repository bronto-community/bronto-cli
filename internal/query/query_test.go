package query

import (
	"errors"
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	for _, q := range []string{
		"status >= 500",
		"ci_marker = 'abc-123'",
		`level = "error"`,
		"status >= 500 AND level = 'error'",
		"a = 1 OR b = 2 AND NOT c = 3",
		"(a = 1 OR b = 2) AND c != 'x'",
		"$span.trace_id = 'd14b'",
		"@status = 'info'",
		"message ~ 'timeout'",
		"path !~ 'health'",
		"gateway = stripe", // bareword value
		"nested.field.name >= 1.5",
		"NOT (a = 1)",
	} {
		if _, err := Parse(q); err != nil {
			t.Errorf("Parse(%q) = %v, want ok", q, err)
		}
	}
}

func TestParseErrorsWithPosition(t *testing.T) {
	cases := []struct {
		q       string
		wantMsg string
	}{
		{"status >", "expected a value"},
		{"status", "expected a comparison operator"},
		{"status >= 500 AND", "incomplete expression"},
		{"(a = 1", "missing closing parenthesis"},
		{"= 5", "unexpected \"=\""},
		{"a = 'unterminated", "unterminated string"},
		{"", "empty query"},
		{"a = 1 b = 2", "expected AND, OR, or end"},
	}
	for _, c := range cases {
		_, err := Parse(c.q)
		var pe *ParseError
		if err == nil || !errors.As(err, &pe) {
			t.Errorf("Parse(%q): want ParseError, got %v", c.q, err)
			continue
		}
		if !strings.Contains(pe.Msg, c.wantMsg) {
			t.Errorf("Parse(%q) msg = %q, want contains %q", c.q, pe.Msg, c.wantMsg)
		}
	}
}

func TestCaretPointsAtError(t *testing.T) {
	_, err := Parse("status >= 500 AND lev !")
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatal(err)
	}
	caret := pe.Caret("status >= 500 AND lev !")
	lines := strings.Split(caret, "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "^") {
		t.Fatalf("caret = %q", caret)
	}
	if idx := strings.Index(lines[1], "^"); idx != pe.Pos {
		t.Fatalf("caret at %d, error pos %d", idx, pe.Pos)
	}
}

func TestFields(t *testing.T) {
	n, err := Parse("status >= 500 AND (level = 'e' OR NOT status = 200) AND svc.name = 'x'")
	if err != nil {
		t.Fatal(err)
	}
	got := Fields(n)
	want := []string{"level", "status", "svc.name"}
	if len(got) != len(want) {
		t.Fatalf("fields = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fields = %v, want %v", got, want)
		}
	}
}

func TestSuggest(t *testing.T) {
	known := []string{"status", "level", "gateway", "message"}
	if s := Suggest("stauts", known); s != "status" {
		t.Fatalf("Suggest(stauts) = %q", s)
	}
	if s := Suggest("levl", known); s != "level" {
		t.Fatalf("Suggest(levl) = %q", s)
	}
	if s := Suggest("zzzzzz", known); s != "" {
		t.Fatalf("Suggest(zzzzzz) = %q, want no match", s)
	}
}
