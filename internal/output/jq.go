package output

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/itchyny/gojq"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

// CompileJQ parses and compiles a jq expression once. Callers should compile
// eagerly — before any network call — so a bad expression fails fast as a
// usage error (exit 2) rather than after a round trip to the API.
func CompileJQ(expr string) (*gojq.Code, error) {
	q, err := gojq.Parse(expr)
	if err != nil {
		return nil, clierr.New("usage_invalid_jq", fmt.Sprintf("invalid jq expression: %v", err)).
			WithHint("See https://jqlang.org/manual/ for jq syntax.")
	}
	code, err := gojq.Compile(q)
	if err != nil {
		return nil, clierr.New("usage_invalid_jq", fmt.Sprintf("invalid jq expression: %v", err)).
			WithHint("See https://jqlang.org/manual/ for jq syntax.")
	}
	return code, nil
}

// runJQ runs code against v and writes every emitted result as its own
// compact JSON line — jq semantics: a query can yield zero, one, or many
// results per input, and each result (object, array, string, number, ...)
// prints as one line. Runtime errors on a given input are skipped, matching
// jq's own behavior of continuing past per-value errors, so a query that
// errors on every input still completes and exits cleanly with no output.
func runJQ(w io.Writer, code *gojq.Code, v any) error {
	iter := code.Run(v)
	for {
		res, ok := iter.Next()
		if !ok {
			return nil
		}
		if _, isErr := res.(error); isErr {
			continue
		}
		b, err := json.Marshal(res)
		if err != nil {
			continue
		}
		b = append(b, '\n')
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
}
