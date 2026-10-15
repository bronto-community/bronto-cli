package query

import (
	"strconv"
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"status >= 500 AND user.email = 'a@b.c'", "status >= ? AND user.email = ?", true},
		{`msg ~ "timeout" or not (level = error)`, "msg ~ ? OR NOT (level = ?)", true},
		{"error", "?", true}, // free-text term
		{"checkout failed", "? ?", true},
		{"@raw ~ 'x' AND $service != checkout-api", "@raw ~ ? AND $service != ?", true},
		{"name = 'O''Brien'", "name = ?", true},
		{"((a = 1))", "((a = ?))", true},
		{"", "", true},
		{"a = 'unterminated", "", false},
		{"service IN ('a', 'b')", "service IN (?, ?)", true},
		{"service NOT IN (1,2) AND msg not like '%x%'", "service NOT IN (?, ?) AND msg NOT LIKE ?", true},
		{"LOWER(level) = 'e' OR a is not null", "LOWER(level) = ? OR a IS NOT NULL", true},
		{"NOT EXISTS $span.parent_span_id", "NOT EXISTS $span.parent_span_id", true},
		{"in", "?", true},            // a lone keyword-word is a free-text term
		{"msg ~ 'a\\'b'", "", false}, // backslash is no escape: unterminated
		{"x = 1 | stats count()", "", false},
	}
	for _, c := range cases {
		got, ok := Sanitize(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("Sanitize(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func FuzzSanitize(f *testing.F) {
	f.Add("status >= 500 AND msg = 'x'")
	f.Add(`a = "q\"x" or b = 1.5e3`)
	f.Fuzz(func(t *testing.T, in string) {
		out, ok := Sanitize(in)
		if !ok {
			return
		}
		// No literal survives: no quoted strings, no numbers.
		if strings.ContainsAny(out, `'"`) {
			t.Fatalf("Sanitize(%q) = %q keeps a quoted string", in, out)
		}
		for _, tok := range strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(out)) {
			if _, err := strconv.ParseFloat(tok, 64); err == nil {
				t.Fatalf("Sanitize(%q) = %q keeps the number %q", in, out, tok)
			}
		}
	})
}
