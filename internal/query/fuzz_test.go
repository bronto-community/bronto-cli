package query

import (
	"strings"
	"testing"
)

// FuzzParse fuzzes the client-side query parser. Parse must never panic on
// arbitrary input, and any query it accepts must yield fields and re-parse
// deterministically. (Go-native fuzzing also satisfies Scorecard's Fuzzing
// check; the seed corpus runs under normal `go test`.)
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"",
		"status = 500",
		"a = 'b' AND c != 2",
		"name = 'O''Brien'",
		"(x > 1 OR y < 2) AND NOT z ~ 'p'",
		"field.sub = \"v\"",
		"@time >= 1700000000",
		"'unterminated",
		"= = =",
		"a = ",
		strings.Repeat("(", 64) + "a=1" + strings.Repeat(")", 64),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		n, err := Parse(input)
		if err != nil {
			return // parse errors are expected for arbitrary input
		}
		_ = Fields(n) // must not panic on any accepted query
		if _, err2 := Parse(input); err2 != nil {
			t.Fatalf("Parse non-deterministic for %q: accepted, then %v", input, err2)
		}
	})
}
