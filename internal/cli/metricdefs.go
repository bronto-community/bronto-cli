package cli

import (
	"context"
	"encoding/json"
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
     "where": "Span:kind = 'SPAN_KIND_SERVER'",
     "aggregation": [{"time": "count", "reduce_to": "avg"}]}]}
  EOF

  # p95 latency of server spans, grouped by operation, shown in ms/s
  bronto metrics definitions create --input - <<'EOF'
  {"name": "checkout-service p95 latency",
   "queries": [{"name": "p95_latency", "select": ["p95(Span:duration_nano)"],
     "from_expr": "\"collection\" = '.traces' AND \"dataset\" = 'checkout-service'",
     "where": "Span:kind = 'SPAN_KIND_SERVER'", "groups": ["Span:name"],
     "aggregation": [{"time": "p95", "reduce_to": "avg"}],
     "unit_config": {"type": "time", "input": "nanoseconds", "output": "auto"}}]}
  EOF

  # Error rate in percent per service: a formula over two queries
  bronto metrics definitions create --input - <<'EOF'
  {"name": "error rate by service",
   "queries": [
     {"name": "errors", "select": ["count(*)"], "from_expr": "\"collection\" = '.traces'",
      "where": "Span:status_code = 'STATUS_CODE_ERROR'", "groups": ["Resource:service.name"],
      "aggregation": [{"time": "count", "reduce_to": "avg"}]},
     {"name": "total", "select": ["count(*)"], "from_expr": "\"collection\" = '.traces'",
      "where": "", "groups": ["Resource:service.name"],
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

// queryDefinition evaluates one definition and flattens it into series
// rows (see timeseriesRows). Each formula's value is then replaced by the
// formula evaluated over the whole range, which costs one more request
// with num_of_slices=1: made only for definitions with formulas, and only
// when rows are printed (-o json prints the raw response).
//
// Live (EU, 2026-10-22): a formula entry's aggregates are [{COUNT,
// <buckets>}, {AVG, <mean of the per-bucket values>}], and so is each
// grouped formula series' value. That mean is not the formula over the
// range: errors / total * 100 per service read 25 for a service whose one
// span in the range was an error (100, in one of four buckets), and 2.84
// for a real 2.82%. With one slice, the single bucket is the range, which
// is the value the web UI shows.
func queryDefinition(ctx context.Context, app *App, id string, params url.Values, label string) (any, []map[string]any, error) {
	return querySeries(app, label, params.Get("num_of_slices") == "1", func(oneSlice bool) (any, error) {
		if !oneSlice {
			return fetchTimeseries(ctx, app, id, params)
		}
		whole := url.Values{}
		for k, v := range params {
			whole[k] = v
		}
		whole.Set("num_of_slices", "1")
		return fetchTimeseries(ctx, app, id, whole)
	})
}

// querySeries is queryDefinition for any timeseries request: fetch runs it
// with the requested slices, or with one slice for formula range values.
// alreadyOne says the requested slices already are one.
func querySeries(app *App, label string, alreadyOne bool, fetch func(oneSlice bool) (any, error)) (any, []map[string]any, error) {
	payload, err := fetch(false)
	if err != nil {
		return nil, nil, err
	}
	rows := timeseriesRows(payload, label)
	entries, _ := payload.(map[string]any)
	formulas := formulaNames(entries)
	if len(formulas) == 0 || !printsRows(app) {
		return payload, rows, nil
	}
	if alreadyOne {
		// Already the range: label it so, without another request.
		applyRangeValues(rows, payload, formulas)
		return payload, rows, nil
	}
	rangePayload, err := fetch(true)
	if err != nil {
		return nil, nil, err
	}
	applyRangeValues(rows, rangePayload, formulas)
	return payload, rows, nil
}

// printsRows reports whether printTimeseries prints series rows (table or
// csv) rather than the raw response.
func printsRows(app *App) bool {
	format, err := app.DetectFormat(false)
	return err == nil && (format == output.FormatTable || format == output.FormatCSV)
}

// formulaNames collects the definition's formula names from whichever
// entries carry its formulas list.
func formulaNames(entries map[string]any) map[string]bool {
	names := map[string]bool{}
	for _, e := range entries {
		m, _ := e.(map[string]any)
		list, _ := m["formulas"].([]any)
		for _, f := range list {
			fm, _ := f.(map[string]any)
			if n, _ := fm["name"].(string); n != "" {
				names[n] = true
			}
		}
	}
	return names
}

// applyRangeValues sets each formula row's value to its value in the
// one-slice response, and its stat to "formula". A series or group the
// one-slice response lacks keeps its per-bucket mean and its stat.
func applyRangeValues(rows []map[string]any, rangePayload any, formulas map[string]bool) {
	entries, _ := rangePayload.(map[string]any)
	for _, r := range rows {
		name, _ := r["series"].(string)
		e, _ := entries[name].(map[string]any)
		if !formulas[name] || e == nil {
			continue
		}
		group, _ := r["group"].(string)
		if v, ok := rangeValue(e, group); ok {
			r["value"] = v
			r["stat"] = "formula"
		}
	}
}

// rangeValue reads the single bucket of a one-slice series: the group's
// value for a grouped formula (an empty group name is a valid group),
// else the series' only point.
func rangeValue(e map[string]any, group string) (float64, bool) {
	if groups, _ := e["groups_series"].([]any); len(groups) > 0 {
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			if gm != nil && fmt.Sprint(gm["name"]) == group {
				return toNumber(gm["value"]), true
			}
		}
		return 0, false
	}
	points := seriesPoints(e["series"])
	if len(points) != 1 {
		return 0, false
	}
	return points[0], true
}

func newDefinitionQueryCmd() *cobra.Command {
	var tf timeseriesFlags
	cmd := &cobra.Command{
		Use:   "query <definition>",
		Short: "Evaluate a metric definition and show its series",
		Long: `Evaluates a saved metric definition over a time range (GET /timeseries/{id})
and prints one row per series: each query and formula, or each group of a
grouped one, with its value over the range and a sparkline of the buckets.
This is the data a widget built on the definition shows.

A formula's VALUE is the formula over the whole range (STAT "formula"), as
the web UI shows it: errors / total * 100 over all errors and spans in the
range. That takes one more request, with one slice. The TREND stays per
bucket.

-o json prints the raw API response. There, a formula's aggregates are the
mean of its per-bucket values, not the range value; add --slices 1 to get
the range value in JSON. Piped output is JSON: for a readable summary in
scripts or agents, add -o table.

The definition can be an id or a unique name. A warning on stderr says when
every series is zero: usually the from_expr or where matches nothing.`,
		Example: "  bronto metrics definitions query \"checkout-service requests\"\n" +
			"  bronto metrics definitions query <id> --since 7d --slices 48\n" +
			"  # A readable summary in scripts or agents (piped output is JSON otherwise)\n" +
			"  bronto metrics definitions query <id> -o table\n" +
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
			payload, rows, err := queryDefinition(cmd.Context(), app, id, params, "")
			if err != nil {
				return err
			}
			return printTimeseries(app, params.Get("time_range"), payload, rows, false, checkDefinition)
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
one table with a DEFINITION column. A formula's VALUE is the formula over
the whole range, as the web UI shows it (see 'bronto metrics definitions
query --help'). -o json prints an object mapping each definition id to its
raw /timeseries response.

Piped output is that JSON: for a readable summary in scripts or agents,
add -o table.

The widget can be an id or a unique name.`,
		Example: "  bronto widgets query \"Checkout requests\"\n" +
			"  # A readable summary in scripts or agents (piped output is JSON otherwise)\n" +
			"  bronto widgets query \"Checkout requests\" -o table\n" +
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
				label := names[id]
				if label == "" {
					label = id
				}
				payload, defRows, err := queryDefinition(ctx, app, id, params, label)
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
				rows = append(rows, defRows...)
			}
			return printTimeseries(app, params.Get("time_range"), raw, rows, true, checkDefinition)
		},
	}
	tf.register(cmd)
	return cmd
}

const checkDefinition = "the definition's from_expr and where (bronto metrics definitions get <definition>)"

// newMetricQueryCmd builds "metrics query": an ad-hoc query through
// POST /timeseries/search, which saves nothing. The flags describe one
// query the way a dashboard manifest's query does (manQuery.resolve
// builds it); --input takes a whole body, e.g. a definition's queries
// and formulas.
func newMetricQueryCmd() *cobra.Command {
	var tf timeseriesFlags
	var q manQuery
	var input string
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Run an ad-hoc timeseries query, without saving it",
		Long: `Runs a timeseries query once (POST /timeseries/search) and prints its
series like 'bronto metrics definitions query': one row per series or
group, with its value over the range and a sparkline of the buckets.
Nothing is saved.

Describe one query with flags: --select, a dataset (-d, repeatable) or
--from-expr, and optionally --where, --group-by and --agg. A select such
as count(*) or p95(Span:duration_nano) aggregates with its function; a
bare field needs --agg.

For several queries or formulas, pass a request body with --input (a
file, or - for stdin): {"queries": [...], "formulas": [...]}, the same
queries and formulas a metric definition holds. Time flags you set
override the body's time_range, num_of_slices and limit; the body's
values override the flag defaults.

-o json prints the raw API response. Piped output is JSON: for a readable
summary in scripts or agents, add -o table.`,
		Example: "  bronto metrics query --select 'count(*)' -d checkout-service --since 1h\n" +
			"  bronto metrics query --select 'p95(Span:duration_nano)' -d .traces/checkout-service \\\n" +
			"    --where \"Span:kind = 'SPAN_KIND_SERVER'\" --group-by Span:name -o table\n" +
			"  bronto metrics query --select duration_ms --agg max --from-expr \"\\\"collection\\\" = 'prod'\"\n" +
			"  # Several queries and a formula\n" +
			"  bronto metrics query --input - <<'EOF'\n" +
			"  {\"queries\": [\n" +
			"     {\"name\": \"errors\", \"select\": [\"count(*)\"], \"from_expr\": \"\\\"collection\\\" = '.traces'\",\n" +
			"      \"where\": \"Span:status_code = 'STATUS_CODE_ERROR'\", \"aggregation\": [{\"time\": \"count\"}]},\n" +
			"     {\"name\": \"total\", \"select\": [\"count(*)\"], \"from_expr\": \"\\\"collection\\\" = '.traces'\",\n" +
			"      \"where\": \"\", \"aggregation\": [{\"time\": \"count\"}]}],\n" +
			"   \"formulas\": [{\"name\": \"error_rate\", \"expression\": \"errors / total * 100\"}]}\n" +
			"  EOF",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			params, err := tf.params()
			if err != nil {
				return err
			}
			body := map[string]any{}
			fl := cmd.Flags()
			if fl.Changed("limit") && tf.limit < 1 {
				// tf.params treats 0 as unset, but the API needs a limit of at least 1.
				return clierr.New("usage_invalid_flags", fmt.Sprintf("--limit must be at least 1, got %d", tf.limit)).WithHint("Drop --limit to use the default.")
			}
			queryFlags := fl.Changed("select") || fl.Changed("dataset") || fl.Changed("from-expr") ||
				fl.Changed("where") || fl.Changed("group-by") || fl.Changed("agg")
			switch {
			case input != "" && queryFlags:
				return clierr.New("usage_conflicting_flags", "--input cannot be combined with --select, --dataset, --from-expr, --where, --group-by or --agg").
					WithHint("Put the query in the --input body, or drop --input and describe one query with flags.")
			case input != "":
				b, err := readBodyInput(cmd, input)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(b, &body); err != nil {
					return clierr.New("usage_invalid_body", "the --input body isn't a JSON object: "+err.Error()).
						WithHint(`Pass {"queries": [...], "formulas": [...]}; see 'bronto metrics query --help'.`)
				}
				if qs, _ := body["queries"].([]any); len(qs) == 0 {
					return clierr.New("usage_invalid_body", `the --input body needs at least one query in "queries"`).
						WithHint("See 'bronto metrics query --help' for an example.")
				}
			default:
				q.Agg = strings.ToLower(strings.TrimSpace(q.Agg))
				if err := checkQueryFlags(q); err != nil {
					return err
				}
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			// POST /timeseries/search saves nothing: run it under --dry-run
			// too, as a GET would be.
			app.DryRun = false
			ctx := cmd.Context()
			if input == "" {
				var lookup datasetLookup
				if len(q.Datasets) > 0 {
					ds, err := listDatasets(ctx, app)
					if err != nil {
						return err
					}
					lookup = lookupIn(ds)
				}
				aq, err := q.resolve(0, lookup, nil)
				if err != nil {
					return err
				}
				body["queries"] = []apiQuery{aq}
			}
			timeSet := fl.Changed("since") || fl.Changed("from") || fl.Changed("to") || fl.Changed("time-range")
			if _, ok := body["time_range"]; !ok || timeSet {
				tr := map[string]any{"natural": params.Get("time_range")}
				if params.Get("time_range") == "" {
					from, _ := strconv.ParseInt(params.Get("from_ts"), 10, 64)
					to, _ := strconv.ParseInt(params.Get("to_ts"), 10, 64)
					tr = map[string]any{"from_ts": from, "to_ts": to}
				}
				body["time_range"] = tr
			}
			if _, ok := body["num_of_slices"]; !ok || fl.Changed("slices") {
				body["num_of_slices"] = tf.slices
			}
			if _, ok := body["limit"]; (!ok && tf.limit != 0) || fl.Changed("limit") {
				body["limit"] = tf.limit
			}
			oneSlice := fmt.Sprint(body["num_of_slices"]) == "1"
			payload, rows, err := querySeries(app, "", oneSlice, func(whole bool) (any, error) {
				send := body
				if whole {
					send = map[string]any{}
					for k, v := range body {
						send[k] = v
					}
					send["num_of_slices"] = 1
				}
				b, err := json.Marshal(send)
				if err != nil {
					return nil, err
				}
				return doJSONRequest(ctx, app, http.MethodPost, "/timeseries/search", b)
			})
			if err != nil {
				return err
			}
			window := ""
			if tr, _ := body["time_range"].(map[string]any); tr != nil {
				window, _ = tr["natural"].(string)
			}
			return printTimeseries(app, window, payload, rows, false, "the query's dataset and where")
		},
	}
	tf.register(cmd)
	f := cmd.Flags()
	f.StringVar(&q.Select, "select", "", "what to compute, e.g. count(*), p95(Span:duration_nano), or a field with --agg")
	f.StringArrayVarP(&q.Datasets, "dataset", "d", nil, "dataset name, collection/name or UUID to query (repeatable)")
	f.StringVar(&q.FromExpr, "from-expr", "", `dataset selector expression, e.g. "collection" = '.traces' (instead of -d)`)
	f.StringVar(&q.Where, "where", "", "filter, e.g. Span:kind = 'SPAN_KIND_SERVER'")
	f.StringArrayVarP(&q.GroupBy, "group-by", "g", nil, "group-by key (repeatable, at most 5)")
	f.StringVar(&q.Agg, "agg", "", "aggregation for a field select: "+strings.Join(timeFunctions, ", "))
	f.StringVar(&input, "input", "", "read the request body from a file, or - for stdin")
	return cmd
}

// checkQueryFlags catches what the API would reject with a bare 400, with
// the flag that fixes it.
func checkQueryFlags(q manQuery) error {
	invalid := func(msg, hint string) error {
		return clierr.New("usage_invalid_flags", msg).WithHint(hint)
	}
	fn := defaultAgg(q.Select)
	switch {
	case strings.TrimSpace(q.Select) == "":
		return invalid("--select is required (or pass a request body with --input)",
			"e.g. --select 'count(*)', or --select duration_ms --agg p95.")
	case len(q.Datasets) == 0 && strings.TrimSpace(q.FromExpr) == "":
		return invalid("a dataset is required: pass -d <dataset> or --from-expr",
			"Run 'bronto datasets list' to see datasets.")
	case len(q.Datasets) > 0 && q.FromExpr != "":
		return invalid("use -d or --from-expr, not both", "Drop one of them.")
	case q.Agg != "" && !contains(timeFunctions, q.Agg):
		return invalid(fmt.Sprintf("unknown --agg %q", q.Agg), "Use one of "+strings.Join(timeFunctions, ", ")+".")
	case q.Agg != "" && fn != "" && q.Agg != fn:
		// The API ignores a different aggregation when the select already has a function.
		return invalid(fmt.Sprintf("--select %q already aggregates with %s, and the API ignores --agg %s", q.Select, fn, q.Agg),
			fmt.Sprintf("Drop --agg, or select the bare field with --agg %s.", q.Agg))
	case q.Agg == "" && fn == "":
		return invalid(fmt.Sprintf("--select %q is a field, so --agg must say how to aggregate it", q.Select),
			fmt.Sprintf("e.g. --agg p95, avg, sum or max, or --select 'max(%s)'.", q.Select))
	case len(q.GroupBy) > maxQueryGroups:
		return invalid(fmt.Sprintf("--group-by takes at most %d keys, got %d", maxQueryGroups, len(q.GroupBy)), "Drop some keys.")
	}
	return nil
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
// check says what to look at when every series is zero.
func printTimeseries(app *App, window string, raw any, rows []map[string]any, withDefinition bool, check string) error {
	if allZero(rows) {
		if window == "" {
			window = "the requested range"
		}
		// The second sentence is live-verified 2026-10-21: Bronto keys a
		// saved query by its content, and one first evaluated before fresh
		// data was visible kept answering zero for minutes, while a query
		// differing only in its where saw the data at once.
		app.Notef("Warning: every series is zero over %s. Check %s, or widen the range with --since. "+
			"If the data was ingested in the last few minutes, Bronto may still be answering a cached zero "+
			"for these queries: a query that differs (even only in its where) is evaluated fresh.\n", window, check)
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
	"minutes": 60e9, "hours": 3600e9, "days": 86400e9,
}

// formatSeriesValue renders a value for the table: a time-typed
// unit_config ({type: time, input: nanoseconds}) becomes "413 ms", the way
// the web UI shows it, and a percentage "12.5%" (input fraction is 0 to
// 1, percent 0 to 100); anything else is a plain number.
func formatSeriesValue(v float64, unit map[string]any) string {
	in, _ := unit["input"].(string)
	switch t, _ := unit["type"].(string); t {
	case "time":
		if f, ok := timeUnitNanos[in]; ok {
			return humanNanos(v * f)
		}
	case "percentage":
		switch in {
		case "fraction":
			return formatAggValue(v*100) + "%"
		case "percent":
			return formatAggValue(v) + "%"
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
