package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

// Metric definitions (bronto metrics definitions) are the saved queries a
// widget plots through its metric_ids; `query` evaluates one through
// GET /timeseries/{id} so a user or agent can check a widget will show
// data without opening the web UI. See the registry entry in resources.go
// for what was live-verified about the endpoint.

const definitionsLong = `Manage metric definitions: the saved queries and formulas that dashboard
widgets plot (a widget's metric_ids). These are not the ingested metrics that
'bronto metrics list' shows.

A definition has up to 6 queries and optional formulas over them. Each query
needs a name, a select, a where (may be ""), and from_expr (or from, a list of
dataset ids). Prefer from_expr, e.g. "collection" = '.traces' AND "dataset" =
'checkout-service'. Formulas refer to queries by name: "errors / total * 100".

Definitions can't be updated: the API has no update endpoint. Create a new
definition, point the widget at it with 'bronto widgets update', and delete
the old one.

Run 'bronto metrics definitions query <definition>' to check that a definition
returns data. The /metrics/definitions endpoint isn't in Bronto's published
OpenAPI spec; the bronto CLI uses it as the web UI does.`

const definitionCreateExample = `  # Request count over a trace dataset
  bronto metrics definitions create --input - <<'EOF'
  {"name": "checkout-service requests",
   "queries": [{"name": "requests", "select": ["count(*)"],
     "from_expr": "\"collection\" = '.traces' AND \"dataset\" = 'checkout-service'",
     "where": "$span.kind = 'SPAN_KIND_SERVER'",
     "aggregation": [{"time": "count", "reduce_to": "avg"}]}]}
  EOF

  # p95 latency of server spans, grouped by operation, shown in ms/s
  bronto metrics definitions create --input - <<'EOF'
  {"name": "checkout-service p95 latency",
   "queries": [{"name": "p95_latency", "select": ["p95($span.duration_nano)"],
     "from_expr": "\"collection\" = '.traces' AND \"dataset\" = 'checkout-service'",
     "where": "$span.kind = 'SPAN_KIND_SERVER'", "groups": ["$span.name"],
     "aggregation": [{"time": "p95", "reduce_to": "avg"}],
     "unit_config": {"type": "time", "input": "nanoseconds", "output": "auto"}}]}
  EOF

  # Error rate in percent per service: a formula over two queries
  bronto metrics definitions create --input - <<'EOF'
  {"name": "error rate by service",
   "queries": [
     {"name": "errors", "select": ["count(*)"], "from_expr": "\"collection\" = '.traces'",
      "where": "$span.status_code = 'STATUS_CODE_ERROR'", "groups": ["$service.name"],
      "aggregation": [{"time": "count", "reduce_to": "avg"}]},
     {"name": "total", "select": ["count(*)"], "from_expr": "\"collection\" = '.traces'",
      "where": "", "groups": ["$service.name"],
      "aggregation": [{"time": "count", "reduce_to": "avg"}]}],
   "formulas": [{"name": "error_rate", "expression": "errors / total * 100"}]}
  EOF

  bronto metrics definitions create --input definition.json`

// maxDefinitionQueries is the API's cap on queries per definition.
const maxDefinitionQueries = 6

// checkDefinitionBody catches the create bodies the API rejects with a bare
// "400: No schema matches". Live (2026-10-21) a query without where (even
// "") or without from_expr/from fails that way; groups, aggregation and
// formulas are optional.
func checkDefinitionBody(verb string, def map[string]any) error {
	if verb != "create" || def == nil {
		return nil // not a JSON object: let the API report it
	}
	invalid := func(msg string) error {
		return clierr.New("usage_invalid_body", msg).
			WithHint("See 'bronto metrics definitions create --help' for complete examples.")
	}
	queries, _ := def["queries"].([]any)
	if len(queries) == 0 {
		return invalid("a metric definition needs at least one query in \"queries\"")
	}
	if len(queries) > maxDefinitionQueries {
		return invalid(fmt.Sprintf("a metric definition takes at most %d queries, got %d", maxDefinitionQueries, len(queries)))
	}
	for i, q := range queries {
		m, ok := q.(map[string]any)
		if !ok {
			return invalid(fmt.Sprintf("queries[%d] is not an object", i))
		}
		label := fmt.Sprintf("queries[%d]", i)
		if n, _ := m["name"].(string); n != "" {
			label = fmt.Sprintf("query %q", n)
		}
		var missing []string
		if _, ok := m["where"]; !ok {
			missing = append(missing, `where (use "" for no filter)`)
		}
		if m["from_expr"] == nil && m["from"] == nil {
			missing = append(missing, "from_expr (or from)")
		}
		if len(missing) > 0 {
			return invalid(fmt.Sprintf("%s is missing %s", label, strings.Join(missing, " and ")))
		}
	}
	return nil
}

// definitionListRows replaces the queries and formulas arrays with their
// counts in the human list view.
func definitionListRows(rows []map[string]any, _ output.Format) []map[string]any {
	for _, row := range rows {
		for _, k := range []string{"queries", "formulas"} {
			arr, _ := row[k].([]any)
			row[k] = len(arr)
		}
	}
	return rows
}

// newDefinitionGetCmd is the generic get with one fix: live (2026-10-21) a
// GET of a missing definition id, deleted or never created, answers 500
// instead of 404. On a 500 for an id the list doesn't have, it reports
// resource_not_found (exit 4) instead of a server error.
func newDefinitionGetCmd(desc resourceDesc) *cobra.Command {
	cmd := newResourceGetCmd(desc)
	inner := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		err := inner(cmd, args)
		var ce *clierr.Error
		if err == nil || !errors.As(err, &ce) || ce.Code != "api_server_error" || !uuidRe.MatchString(args[0]) {
			return err
		}
		app, appErr := NewApp(cmd)
		if appErr != nil {
			return err
		}
		payload, listErr := doJSONRequest(cmd.Context(), app, http.MethodGet, desc.listPath(), nil)
		if listErr != nil {
			return err
		}
		for _, row := range rowsFromPayload(payload, desc.ListRowKeys...) {
			if desc.rowID(row) == args[0] {
				return err // it exists: the 500 is a real server error
			}
		}
		return clierr.New("resource_not_found", fmt.Sprintf("no %s with id %s", desc.singular(), args[0])).
			WithHint(fmt.Sprintf("Run 'bronto %s list' to see ids and names.", desc.display()))
	}
	return cmd
}

// timeseriesFlags are the time and resolution flags of the two query
// commands, mapped onto GET /timeseries/{id}'s query parameters.
type timeseriesFlags struct {
	since, from, to, timeRange string
	slices, limit              int
}

func (f *timeseriesFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.since, "since", "", "relative lookback: 30m, 24h, 7d, 1h30m (default 24h)")
	fl.StringVar(&f.from, "from", "", "absolute start (RFC3339)")
	fl.StringVar(&f.to, "to", "", "absolute end (RFC3339), requires --from")
	fl.StringVar(&f.timeRange, "time-range", "", `relative range in the API's words, e.g. "Last 24 hours" (instead of --since)`)
	fl.IntVar(&f.slices, "slices", 24, "time buckets per series (the TREND sparkline's resolution)")
	fl.IntVar(&f.limit, "limit", 0, "maximum groups per grouped query, 1-1000 (default: the API's 100)")
}

// params builds the /timeseries query string: time_range or from_ts/to_ts
// (the API takes one or the other), num_of_slices, and limit when set.
func (f timeseriesFlags) params() (url.Values, error) {
	v := url.Values{}
	if f.timeRange != "" {
		if f.since != "" || f.from != "" || f.to != "" {
			return nil, clierr.New("usage_conflicting_time_flags",
				"--time-range cannot be combined with --since/--from/--to")
		}
		v.Set("time_range", f.timeRange)
	} else {
		spec, err := timerange.Resolve(f.since, f.from, f.to, nil)
		if err != nil {
			return nil, err
		}
		if spec.IsZero() {
			spec.TimeRange = "Last 24 hours"
		}
		if spec.TimeRange != "" {
			v.Set("time_range", spec.TimeRange)
		} else {
			v.Set("from_ts", strconv.FormatInt(spec.FromTs, 10))
			v.Set("to_ts", strconv.FormatInt(spec.ToTs, 10))
		}
	}
	if f.slices < 1 {
		return nil, clierr.New("usage_invalid_flags", fmt.Sprintf("--slices must be at least 1, got %d", f.slices))
	}
	v.Set("num_of_slices", strconv.Itoa(f.slices))
	if f.limit != 0 {
		if f.limit < 1 || f.limit > 1000 {
			return nil, clierr.New("usage_invalid_limit", fmt.Sprintf("--limit must be between 1 and 1000, got %d", f.limit))
		}
		v.Set("limit", strconv.Itoa(f.limit))
	}
	return v, nil
}

// fetchTimeseries evaluates one saved definition. Unlike GET
// /metrics/definitions/{id}, this endpoint answers 404 for a missing id.
func fetchTimeseries(ctx context.Context, app *App, id string, params url.Values) (any, error) {
	return doJSONRequest(ctx, app, http.MethodGet, "/timeseries/"+url.PathEscape(id)+"?"+params.Encode(), nil)
}

func newDefinitionQueryCmd() *cobra.Command {
	var tf timeseriesFlags
	cmd := &cobra.Command{
		Use:   "query <definition>",
		Short: "Evaluate a metric definition and show its series",
		Long: `Evaluates a saved metric definition over a time range (GET /timeseries/{id})
and prints one row per series: each query and formula, or each group of a
grouped one, with its value over the range and a sparkline of the buckets.
This is the data a widget built on the definition shows. -o json prints the
raw API response.

The definition can be an id or a unique name. A warning on stderr says when
every series is zero: usually the from_expr or where matches nothing.`,
		Example: "  bronto metrics definitions query \"checkout-service requests\"\n" +
			"  bronto metrics definitions query <id> --since 7d --slices 48\n" +
			"  bronto metrics definitions query <id> --time-range \"Last 24 hours\" -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKindRef("definitions"),
		RunE: func(cmd *cobra.Command, args []string) error {
			params, err := tf.params()
			if err != nil {
				return err
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			id, err := resolveKindRef(cmd.Context(), app, "definitions", args[0])
			if err != nil {
				return err
			}
			payload, err := fetchTimeseries(cmd.Context(), app, id, params)
			if err != nil {
				return err
			}
			return printTimeseries(app, params, payload, timeseriesRows(payload, ""), false)
		},
	}
	tf.register(cmd)
	return cmd
}

func newWidgetQueryCmd() *cobra.Command {
	var tf timeseriesFlags
	cmd := &cobra.Command{
		Use:   "query <widget>",
		Short: "Evaluate the metric definitions a widget plots",
		Long: `Evaluates every metric definition in a widget's metric_ids, like
'bronto metrics definitions query' does for one, and prints their series in
one table with a DEFINITION column. -o json prints an object mapping each
definition id to its raw /timeseries response.

The widget can be an id or a unique name.`,
		Example: "  bronto widgets query \"Checkout requests\"\n" +
			"  bronto widgets query <id> --since 7d -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKindRef("widgets"),
		RunE: func(cmd *cobra.Command, args []string) error {
			params, err := tf.params()
			if err != nil {
				return err
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			widgetID, err := resolveKindRef(ctx, app, "widgets", args[0])
			if err != nil {
				return err
			}
			widget, err := doJSONRequest(ctx, app, http.MethodGet, "/widgets/"+url.PathEscape(widgetID), nil)
			if err != nil {
				return err
			}
			ids := stringList(widget, "metric_ids")
			if len(ids) == 0 {
				return clierr.New("resource_not_found",
					fmt.Sprintf("widget %s has no metric definitions (its metric_ids is empty)", args[0])).
					WithHint("Create one with 'bronto metrics definitions create' and set the widget's metric_ids with 'bronto widgets update'.")
			}
			names, err := definitionNames(ctx, app)
			if err != nil {
				return err
			}
			raw := map[string]any{}
			var rows []map[string]any
			for _, id := range ids {
				payload, err := fetchTimeseries(ctx, app, id, params)
				if err != nil {
					var ce *clierr.Error
					if errors.As(err, &ce) && ce.Code == "resource_not_found" {
						return clierr.New("resource_not_found",
							fmt.Sprintf("widget %s references metric definition %s, which doesn't exist", args[0], id)).
							WithHint("Point the widget at an existing definition with 'bronto widgets update'.")
					}
					return err
				}
				raw[id] = payload
				label := names[id]
				if label == "" {
					label = id
				}
				rows = append(rows, timeseriesRows(payload, label)...)
			}
			return printTimeseries(app, params, raw, rows, true)
		},
	}
	tf.register(cmd)
	return cmd
}

// stringList reads a string array field from a decoded JSON object.
func stringList(doc any, key string) []string {
	m, _ := doc.(map[string]any)
	arr, _ := m[key].([]any)
	var out []string
	for _, v := range arr {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// definitionNames maps every definition id to its name, from one list call.
func definitionNames(ctx context.Context, app *App) (map[string]string, error) {
	desc, _ := descByName("definitions")
	payload, err := doJSONRequest(ctx, app, http.MethodGet, desc.listPath(), nil)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, row := range rowsFromPayload(payload, desc.ListRowKeys...) {
		if n, _ := row["name"].(string); n != "" {
			names[desc.rowID(row)] = n
		}
	}
	return names, nil
}

// printTimeseries prints the raw payload for json/jsonl and the series rows
// for table/csv, warning on stderr when every series is zero.
func printTimeseries(app *App, params url.Values, raw any, rows []map[string]any, withDefinition bool) error {
	if allZero(rows) {
		window := params.Get("time_range")
		if window == "" {
			window = "the requested range"
		}
		// The second sentence is live-verified 2026-10-21: Bronto keys a
		// saved query by its content, and one first evaluated before fresh
		// data was visible kept answering zero for minutes, while a query
		// differing only in its where saw the data at once.
		app.Notef("Warning: every series is zero over %s. Check the definition's from_expr and where "+
			"(bronto metrics definitions get <definition>), or widen the range with --since. "+
			"If the data was ingested in the last few minutes, Bronto may still be answering a cached zero "+
			"for these queries: a new definition whose query differs (even only in its where) is evaluated fresh.\n", window)
	}
	format, err := app.DetectFormat(false)
	if err != nil {
		return err
	}
	p, err := app.PrinterFor(format)
	if err != nil {
		return err
	}
	if format != output.FormatTable && format != output.FormatCSV {
		return p.PrintJSON(raw)
	}
	if len(rows) == 0 {
		app.Notef("No series.\n")
		return nil
	}
	for _, r := range rows {
		v, _ := r["value"].(float64)
		unit, _ := r["unit"].(map[string]any)
		if format == output.FormatTable {
			r["value"] = formatSeriesValue(v, unit)
		} else {
			r["value"] = formatAggValue(v)
		}
	}
	cols := []string{"series", "group", "stat", "value", "trend"}
	if withDefinition {
		cols = append([]string{"definition"}, cols...)
	}
	return p.PrintRows(cols, rows)
}

func allZero(rows []map[string]any) bool {
	if len(rows) == 0 {
		return false
	}
	for _, r := range rows {
		if v, _ := r["value"].(float64); v != 0 {
			return false
		}
		pts, _ := r["points"].([]float64)
		for _, x := range pts {
			if x != 0 {
				return false
			}
		}
	}
	return true
}

// timeseriesRows flattens a GET /timeseries/{id} response, a map from each
// query and formula name to its series, into one row per series: the
// groups of a grouped query each get a row; an ungrouped one gets a single
// row with its range aggregate. Rows follow the definition's order
// (queries, then formulas), which every entry repeats in its queries and
// formulas fields (live 2026-10-21); names missing from those sort last.
//
// The live shape (EU, 2026-10-21): aggregates is [{COUNT, n}, {<stat>,
// value}], so the last entry is the value the stat names; series values
// are strings ("106.0"); groups_series[] carry name (the group value),
// stat, value and a numeric timeseries.
func timeseriesRows(payload any, definition string) []map[string]any {
	entries, _ := payload.(map[string]any)
	var rows []map[string]any
	for _, name := range seriesOrder(entries) {
		e, _ := entries[name].(map[string]any)
		if e == nil {
			continue
		}
		unit := unitConfig(entries, name)
		row := func(group, stat string, value float64, points []float64) map[string]any {
			r := map[string]any{
				"series": name, "group": group, "stat": strings.ToLower(stat),
				"value": value, "points": points, "trend": sparkline(points),
			}
			if unit != nil {
				r["unit"] = unit
			}
			if definition != "" {
				r["definition"] = definition
			}
			return r
		}
		if groups, _ := e["groups_series"].([]any); len(groups) > 0 {
			for _, g := range groups {
				gm, _ := g.(map[string]any)
				if gm == nil {
					continue
				}
				stat, _ := gm["stat"].(string)
				rows = append(rows, row(fmt.Sprint(gm["name"]), stat, toNumber(gm["value"]), seriesPoints(gm["timeseries"])))
			}
			continue
		}
		stat, value := "", 0.0
		if aggs, _ := e["aggregates"].([]any); len(aggs) > 0 {
			last, _ := aggs[len(aggs)-1].(map[string]any)
			stat, _ = last["function"].(string)
			value = toNumber(last["value"])
		}
		rows = append(rows, row("", stat, value, seriesPoints(e["series"])))
	}
	return rows
}

// seriesOrder lists the response's series names in definition order.
func seriesOrder(entries map[string]any) []string {
	var order []string
	seen := map[string]bool{}
	add := func(list any) {
		arr, _ := list.([]any)
		for _, item := range arr {
			m, _ := item.(map[string]any)
			if n, _ := m["name"].(string); n != "" && !seen[n] {
				if _, ok := entries[n]; ok {
					seen[n] = true
					order = append(order, n)
				}
			}
		}
	}
	// Not every entry carries the definition's queries and formulas (live,
	// often only one does), so scan all of them, in sorted key order to
	// stay deterministic; add skips names it has already placed.
	keys := sortedKeys(entries)
	for _, key := range []string{"queries", "formulas"} {
		for _, k := range keys {
			if m, ok := entries[k].(map[string]any); ok {
				add(m[key])
			}
		}
	}
	var rest []string
	for n := range entries {
		if !seen[n] {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	return append(order, rest...)
}

// unitConfig returns the unit_config of the query or formula named name,
// from whichever entry carries the definition's queries and formulas.
func unitConfig(entries map[string]any, name string) map[string]any {
	for _, k := range sortedKeys(entries) {
		entry, _ := entries[k].(map[string]any)
		for _, key := range []string{"queries", "formulas"} {
			list, _ := entry[key].([]any)
			for _, item := range list {
				m, _ := item.(map[string]any)
				if n, _ := m["name"].(string); n == name {
					u, _ := m["unit_config"].(map[string]any)
					return u
				}
			}
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func seriesPoints(v any) []float64 {
	arr, _ := v.([]any)
	out := make([]float64, 0, len(arr))
	for _, p := range arr {
		m, _ := p.(map[string]any)
		out = append(out, toNumber(m["value"]))
	}
	return out
}

// timeUnitNanos are the unit_config inputs for type "time", in nanoseconds.
var timeUnitNanos = map[string]float64{
	"nanoseconds": 1, "microseconds": 1e3, "milliseconds": 1e6, "seconds": 1e9,
}

// formatSeriesValue renders a value for the table: a time-typed
// unit_config ({type: time, input: nanoseconds}) becomes "413 ms", the way
// the web UI shows it; anything else is a plain number.
func formatSeriesValue(v float64, unit map[string]any) string {
	if t, _ := unit["type"].(string); t == "time" {
		in, _ := unit["input"].(string)
		if f, ok := timeUnitNanos[in]; ok {
			return humanNanos(v * f)
		}
	}
	return formatAggValue(v)
}

// humanNanos renders a nanosecond count with the largest unit that keeps
// it at 1 or more, to three significant digits.
func humanNanos(ns float64) string {
	units := []struct {
		name string
		size float64
	}{{"s", 1e9}, {"ms", 1e6}, {"µs", 1e3}, {"ns", 1}}
	for _, u := range units {
		if x := ns / u.size; x >= 1 || u.size == 1 {
			prec := 2
			switch {
			case x >= 100:
				prec = 0
			case x >= 10:
				prec = 1
			case x == 0:
				prec = 0
			}
			return strconv.FormatFloat(x, 'f', prec, 64) + " " + u.name
		}
	}
	return formatAggValue(ns)
}
