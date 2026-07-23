package query_test

import (
	"testing"

	"github.com/bronto-community/bronto-cli/internal/query"
	"github.com/bronto-community/bronto-cli/internal/traces"
)

// TestParseAcceptsWhatQuoteProduces welds the client-side validator to the
// quoting helper the CLI itself uses to BUILD queries: every literal
// traces.Quote can emit must parse. Quote documents SQL-style escaping
// (doubling embedded single quotes, so O'Brien becomes 'O”Brien'), and
// monitors check — a CI gate — treats parse failures as lint problems, so
// a validator that rejects Quote's own output fails CI on valid monitors.
func TestParseAcceptsWhatQuoteProduces(t *testing.T) {
	literals := []string{
		"simple",
		"O'Brien", // the exact example Quote's doc comment promises works
		"it's got 'several' quotes",
		"'",
		"''",
		"leading'",
		"'trailing",
		"",
		"unicode Ω'π",
	}
	for _, lit := range literals {
		q := "name = " + traces.Quote(lit)
		if _, err := query.Parse(q); err != nil {
			t.Errorf("Parse rejects a query built by the CLI's own quoting:\n  literal: %q\n  query:   %s\n  err:     %v", lit, q, err)
		}
	}
}

// TestParseStillRejectsUnterminatedStrings pins the negative side of the
// weld: supporting doubled-quote escaping must not make the lexer accept
// genuinely unterminated strings.
func TestParseStillRejectsUnterminatedStrings(t *testing.T) {
	for _, q := range []string{
		"name = 'abc",
		"name = '",
		"name = 'a''", // 'a' followed by an unterminated escape-open
	} {
		if _, err := query.Parse(q); err == nil {
			t.Errorf("Parse accepted invalid query %q", q)
		}
	}
}
