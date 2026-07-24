package cli

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/query"
)

// filterClause is one structured --eq/--gt/… occurrence: a friendly field
// name, the query operator it maps to, and the raw value. Clauses are
// collected in command-line order and ANDed together into a WHERE.
type filterClause struct {
	op    string // query operator: = != > >= < <= ~ !~
	field string
	value string
}

// filterFlag is a pflag.Value shared by every operator flag: each flag
// carries its operator and appends occurrences to one ordered slice, so
// clauses keep the order the user typed them across different flags.
type filterFlag struct {
	op  string
	dst *[]filterClause
}

func (f *filterFlag) String() string { return "" }
func (f *filterFlag) Type() string   { return "field=value" }

func (f *filterFlag) Set(s string) error {
	// Split on the FIRST '=' so values may contain '=' (field=a=b).
	field, value, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("expected field=value, got %q", s)
	}
	if strings.TrimSpace(field) == "" {
		return fmt.Errorf("empty field name in %q", s)
	}
	*f.dst = append(*f.dst, filterClause{op: f.op, field: strings.TrimSpace(field), value: value})
	return nil
}

// registerFilterFlags wires the operator-named filter flags onto f, all
// feeding the shared ordered clause slice.
func registerFilterFlags(f *pflag.FlagSet, dst *[]filterClause) {
	specs := []struct{ name, op, desc string }{
		{"eq", "=", "keep events where field equals value (repeatable, ANDed)"},
		{"ne", "!=", "keep events where field does not equal value"},
		{"gt", ">", "keep events where field is greater than value"},
		{"ge", ">=", "keep events where field is greater than or equal to value"},
		{"lt", "<", "keep events where field is less than value"},
		{"le", "<=", "keep events where field is less than or equal to value"},
		{"match", "~", "keep events where field matches the regex value"},
		{"nmatch", "!~", "keep events where field does not match the regex value"},
	}
	for _, s := range specs {
		f.Var(&filterFlag{op: s.op, dst: dst}, s.name, s.desc)
	}
}

// compileFilters turns friendly filter clauses into an official WHERE
// fragment. When logID is set and exact is false it fetches the dataset's
// field index (/top-keys) to resolve friendly names to their exact spelling;
// on any index miss it falls back to using names verbatim and returns a note
// explaining the degraded mode. Value quoting is a value-shape heuristic
// (numbers/bools bare, everything else single-quoted) — deliberately NOT the
// index's `type`, which reports "string" even for numeric attributes.
func compileFilters(ctx context.Context, app *App, clauses []filterClause, exact bool, logID, timeRange string) (where, note string, err error) {
	if len(clauses) == 0 {
		return "", "", nil
	}
	var index []string // exact field names available in the dataset
	if !exact && logID != "" {
		names, ferr := fetchFieldNames(ctx, app, logID, timeRange)
		if ferr != nil {
			note = fmt.Sprintf("field index unavailable (%v); using field names verbatim", ferr)
		} else {
			index = names
		}
	}
	parts := make([]string, 0, len(clauses))
	for _, c := range clauses {
		name, rerr := resolveFilterField(index, c.field, exact || index == nil)
		if rerr != nil {
			return "", "", rerr
		}
		parts = append(parts, name+" "+c.op+" "+quoteFilterValue(c.op, c.value))
	}
	return strings.Join(parts, " AND "), note, nil
}

// fetchFieldNames returns the exact field names present in a dataset over the
// given relative time range, via the /top-keys index.
func fetchFieldNames(ctx context.Context, app *App, logID, timeRange string) ([]string, error) {
	params := url.Values{"time_range": []string{timeRange}, "log_id": []string{logID}}
	var payload map[string]any
	client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
	if err := client.GetJSON(ctx, "/top-keys", params, &payload); err != nil {
		return nil, err
	}
	rows := normalizeTopKeys(payload)
	if len(rows) == 0 {
		return nil, fmt.Errorf("no fields found in the last window")
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		if k, ok := r["key"].(string); ok {
			names = append(names, k)
		}
	}
	return names, nil
}

// normalizeFieldName lowercases and drops a leading '$' so friendly names
// ("model") resolve to their exact form ("$model").
func normalizeFieldName(s string) string {
	return strings.ToLower(strings.TrimPrefix(s, "$"))
}

// resolveFilterField maps a friendly field name to its exact spelling using
// the dataset field index. Resolution is deterministic: an exact match wins;
// otherwise a single case-insensitive/optional-$ match is used; zero or
// multiple matches are hard errors (exit 2) with candidates. When verbatim
// is true (no index, or --exact) the name is returned unchanged.
func resolveFilterField(index []string, name string, verbatim bool) (string, error) {
	if verbatim {
		return name, nil
	}
	for _, f := range index {
		if f == name {
			return f, nil
		}
	}
	target := normalizeFieldName(name)
	var matches []string
	for _, f := range index {
		if normalizeFieldName(f) == target {
			matches = append(matches, f)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", unknownFieldError(name, index)
	default:
		sort.Strings(matches)
		return "", clierr.New("usage_ambiguous_field",
			fmt.Sprintf("%q matches %d fields — %s", name, len(matches), strings.Join(matches, ", "))).
			WithHint(fmt.Sprintf("Use the exact one, e.g. --eq '%s'=<value>.", matches[0]))
	}
}

// unknownFieldError builds an exit-2 error naming the closest field(s),
// pointing at `bronto fields` for the full list plus values. Candidates are
// found by substring first (precise), then by Damerau-Levenshtein on the
// normalized names (catches typos like "mdl" -> "$model"); the $ prefix is
// stripped on both sides so it doesn't inflate the edit distance.
func unknownFieldError(name string, index []string) error {
	needle := normalizeFieldName(name)
	var near []string
	normToOrig := make(map[string]string, len(index))
	norms := make([]string, 0, len(index))
	for _, f := range index {
		n := normalizeFieldName(f)
		normToOrig[n] = f
		norms = append(norms, n)
		if strings.Contains(n, needle) {
			near = append(near, f)
		}
	}
	sort.Strings(near)
	if len(near) > 3 {
		near = near[:3]
	}
	if len(near) == 0 {
		if s := query.Suggest(needle, norms); s != "" {
			near = []string{normToOrig[s]}
		}
	}
	e := clierr.New("usage_unknown_field", fmt.Sprintf("no field matching %q in the dataset", name))
	if len(near) > 0 {
		return e.WithHint(fmt.Sprintf("closest: %s. Run 'bronto fields -d <dataset> %s' to see fields and values.",
			strings.Join(near, ", "), name))
	}
	return e.WithHint(fmt.Sprintf("Run 'bronto fields -d <dataset>' to list fields, or --exact to use %q verbatim.", name))
}

// quoteFilterValue renders a value for the query language. Regex operators
// (~ !~) always take a quoted string; otherwise a value that parses as a
// number or bool is left bare and everything else is single-quoted (SQL-style
// doubled-quote escaping). This value-shape heuristic beats trusting the
// field's declared type, which is "string" even for numeric attributes.
func quoteFilterValue(op, value string) string {
	if op == "~" || op == "!~" {
		return quoteString(value)
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return value
	}
	if value == "true" || value == "false" {
		return value
	}
	return quoteString(value)
}

func quoteString(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

// andWhere combines a positional (official) WHERE with a compiled filter
// fragment, parenthesizing the positional part to preserve its precedence.
func andWhere(positional, filters string) string {
	switch {
	case positional == "":
		return filters
	case filters == "":
		return positional
	default:
		return "(" + positional + ") AND " + filters
	}
}
