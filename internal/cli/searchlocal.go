package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/query"
)

// localScanCap bounds a single input line (raw log lines can be huge;
// bufio's default 64KiB is too small for fat JSON events).
const localScanCap = 4 * 1024 * 1024

// runLocalSearch evaluates the query client-side over NDJSON or plain
// lines from path ("-" = stdin): the offline counterpart of a server
// search (issue #36). JSON lines flatten to dotted keys (with @raw set
// to the original line); non-JSON lines become {"@raw": line}. Matching
// events flow through the normal printers, so -o/--fields/--jq compose.
func runLocalSearch(app *App, in io.Reader, path, where string, limit int) error {
	r := in
	if path != "-" {
		f, err := os.Open(path) // #nosec G304 -- the user names their own input file
		if err != nil {
			return clierr.New("local_file_error", fmt.Sprintf("cannot open %s: %v", path, err))
		}
		defer func() { _ = f.Close() }()
		r = f
	}

	match := func(map[string]any) bool { return true }
	if strings.TrimSpace(where) != "" {
		node, err := query.Parse(where)
		if err != nil {
			return usageQueryError(err, where)
		}
		m, err := query.NewMatcher(node)
		if err != nil {
			return clierr.New("usage_invalid_query", err.Error())
		}
		match = m.Match
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), localScanCap)
	events := make([]map[string]any, 0, 64)
	for sc.Scan() && len(events) < limit {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		ev := parseLocalLine(line)
		if match(ev) {
			events = append(events, ev)
		}
	}
	if err := sc.Err(); err != nil {
		return clierr.New("local_file_error", fmt.Sprintf("reading %s: %v", path, err))
	}
	return printEvents(app, events)
}

// parseLocalLine turns one input line into a flattened event. JSON
// objects decode with UseNumber (64-bit values survive a jsonl
// round-trip) and keep the original line under @raw; anything else is a
// plain log line.
func parseLocalLine(line string) map[string]any {
	if strings.HasPrefix(line, "{") {
		dec := json.NewDecoder(bytes.NewReader([]byte(line)))
		dec.UseNumber()
		var obj map[string]any
		if err := dec.Decode(&obj); err == nil {
			ev := bronto.Flatten(obj)
			if _, ok := ev["@raw"]; !ok {
				ev["@raw"] = line
			}
			return ev
		}
	}
	return map[string]any{"@raw": line}
}

// usageQueryError renders a local parse failure with the same caret
// diagnostics `bronto query check` uses.
func usageQueryError(err error, where string) error {
	var pe *query.ParseError
	errors.As(err, &pe)
	ce := clierr.New("usage_invalid_query", err.Error())
	if pe != nil {
		ce = ce.WithHint(pe.Caret(where))
	}
	return ce
}
