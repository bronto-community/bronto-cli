package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/bronto"
	"github.com/svrnm/bronto-cli/internal/clierr"
	"github.com/svrnm/bronto-cli/internal/output"
	"github.com/svrnm/bronto-cli/internal/timerange"
	"github.com/svrnm/bronto-cli/internal/traces"
)

func newTracesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "traces",
		Short: "Explore OpenTelemetry traces (APM-style views over the .traces logset)",
	}
	cmd.AddCommand(newTracesServicesCmd(), newTracesOperationsCmd(),
		newTracesAggregateCmd(), newTracesListCmd(), newTracesShowCmd(), newTracesShapeCmd())
	return cmd
}

// validatePositive rejects non-positive numeric flags with a typed usage error.
func validatePositive(name string, v int) error {
	if v < 1 {
		return clierr.New("usage_invalid_flag",
			fmt.Sprintf("--%s must be at least 1, got %d", name, v))
	}
	return nil
}

// tracesAgg builds the shared App + Aggregator pair.
func tracesAgg(cmd *cobra.Command, since, defaultSince string) (*App, *traces.Aggregator, error) {
	app, err := NewApp(cmd)
	if err != nil {
		return nil, nil, err
	}
	if since == "" {
		since = defaultSince
	}
	spec, err := timerange.Resolve(since, "", "", nil)
	if err != nil {
		return nil, nil, err
	}
	agg := &traces.Aggregator{
		Client: bronto.NewClient(app.HTTPClient, app.Config.BaseURL()),
		Time:   spec,
	}
	return app, agg, nil
}

func newTracesServicesCmd() *cobra.Command {
	var since string
	var errorsOnly bool
	var limit int
	cmd := &cobra.Command{
		Use:     "services",
		Short:   "Span counts and latency per service",
		Example: "  bronto traces services --since 15m\n  bronto traces services --errors",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, agg, err := tracesAgg(cmd, since, "15m")
			if err != nil {
				return err
			}
			rows, err := agg.Services(cmd.Context(), errorsOnly, limit)
			if err != nil {
				return err
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows([]string{"service", "spans", "avg", "max"}, rows)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "relative lookback (default 15m)")
	cmd.Flags().BoolVar(&errorsOnly, "errors", false, "only error spans")
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "max services")
	return cmd
}

func newTracesOperationsCmd() *cobra.Command {
	var since, service string
	var errorsOnly bool
	var limit int
	cmd := &cobra.Command{
		Use:   "operations",
		Short: "Span counts and latency per service and operation",
		Example: "  bronto traces operations --since 15m\n" +
			"  bronto traces operations -s checkout --errors -n 10",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, agg, err := tracesAgg(cmd, since, "15m")
			if err != nil {
				return err
			}
			rows, err := agg.Operations(cmd.Context(), service, errorsOnly, limit)
			if err != nil {
				return err
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows([]string{"service", "operation", "spans", "avg", "max"}, rows)
		},
	}
	f := cmd.Flags()
	f.StringVar(&since, "since", "", "relative lookback (default 15m)")
	f.StringVarP(&service, "service", "s", "", "filter by service name")
	f.BoolVar(&errorsOnly, "errors", false, "only error spans")
	f.IntVarP(&limit, "limit", "n", 25, "max operations")
	return cmd
}

func newTracesAggregateCmd() *cobra.Command {
	var (
		since, service, kind, where string
		by                          []string
		errorsOnly, includeEmpty    bool
		rootOnly, allSpans          bool
		limit                       int
	)
	cmd := &cobra.Command{
		Use:   "aggregate",
		Short: "Aggregate span attribute values across traces",
		Example: "  bronto traces aggregate --by http.route\n" +
			"  bronto traces aggregate --by db.system --all-spans --kind client",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(by) == 0 {
				return clierr.New("usage_missing_flag", "at least one --by attribute is required").
					WithHint("Example: --by http.route")
			}
			if err := validatePositive("limit", limit); err != nil {
				return err
			}
			app, agg, err := tracesAgg(cmd, since, "15m")
			if err != nil {
				return err
			}
			effRootOnly := rootOnly
			if allSpans {
				effRootOnly = false
			}
			opts := traces.AttrOptions{
				By: by, RootOnly: effRootOnly, Service: service, Kind: kind,
				Where: where, ErrorsOnly: errorsOnly, IncludeEmpty: includeEmpty, Limit: limit,
			}
			rows, columns, dropped, err := agg.Attributes(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if len(rows) == 0 && effRootOnly && dropped > 0 && app.StdoutIsTTY && !app.Quiet {
				_, _ = fmt.Fprintln(app.Stderr,
					"Root spans on ingress/proxy services often lack app attributes — "+
						"try --all-spans --kind server --service <name>.")
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows(columns, rows)
		},
	}
	f := cmd.Flags()
	f.StringArrayVarP(&by, "by", "b", nil, "attribute to group by (repeatable, required)")
	f.BoolVar(&rootOnly, "root-only", true, "only consider root spans")
	f.BoolVar(&allSpans, "all-spans", false, "consider all spans, not just roots")
	cmd.MarkFlagsMutuallyExclusive("root-only", "all-spans")
	f.StringVarP(&service, "service", "s", "", "filter by service name")
	f.StringVarP(&kind, "kind", "k", "", "filter by span kind (server, client, ...)")
	f.BoolVar(&errorsOnly, "errors", false, "only error spans")
	f.StringVarP(&where, "where", "w", "", "additional raw WHERE clause")
	f.BoolVar(&includeEmpty, "include-empty", false, "include rows where an attribute is missing")
	f.IntVarP(&limit, "limit", "n", 50, "max rows")
	f.StringVar(&since, "since", "", "relative lookback (default 15m)")
	return cmd
}

func newTracesListCmd() *cobra.Command {
	var since, service, operation string
	var minDurationMS float64
	var errorsOnly bool
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List individual spans",
		Example: "  bronto traces list -s checkout --errors\n" +
			"  bronto traces list --min-duration-ms 500 -n 20",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, agg, err := tracesAgg(cmd, since, "15m")
			if err != nil {
				return err
			}
			rows, err := agg.ListSpans(cmd.Context(), traces.ListOptions{
				Service: service, Operation: operation, MinDurationMS: minDurationMS,
				ErrorsOnly: errorsOnly, Limit: limit,
			})
			if err != nil {
				return err
			}
			// Piped default is JSONL, consistent with `bronto search`
			// (streaming=true only changes the no-flag/piped default;
			// table/csv/json/jsonl all still print the fixed columns below).
			p, err := app.Printer(true)
			if err != nil {
				return err
			}
			return p.PrintRows([]string{"@time", "service", "operation", "duration", "status", "trace_id"}, rows)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&service, "service", "s", "", "filter by service name")
	f.StringVar(&operation, "operation", "", "filter by operation (span) name")
	f.Float64Var(&minDurationMS, "min-duration-ms", 0, "only spans at least this long")
	f.BoolVar(&errorsOnly, "errors", false, "only error spans")
	f.IntVarP(&limit, "limit", "n", 50, "max spans")
	f.StringVar(&since, "since", "", "relative lookback (default 15m)")
	return cmd
}

func newTracesShowCmd() *cobra.Command {
	var since string
	var limit, barWidth int
	cmd := &cobra.Command{
		Use:   "show <trace-id>",
		Short: "Render a single trace as a waterfall",
		Example: "  bronto traces show 4bf92f3577b34da6a3ce929d0e0e4736\n" +
			"  bronto traces show <trace-id> --since 6h -n 200",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			app, agg, err := tracesAgg(cmd, since, "1h")
			if err != nil {
				return err
			}
			mrf := false
			resp, err := agg.Client.Search(cmd.Context(), bronto.SearchRequest{
				FromExpr: traces.FromExpr, Time: agg.Time,
				Where:           "$span.trace_id = " + traces.Quote(id),
				Select:          append([]string{"@time"}, traces.SpanFields...),
				Limit:           limit,
				MostRecentFirst: &mrf,
			})
			if err != nil {
				return err
			}
			eventRows := resp.EventRows()
			if len(eventRows) == 0 {
				return clierr.New("trace_not_found", fmt.Sprintf("no spans found for trace %s", id)).
					WithHint("Widen the window with --since (default 1h).")
			}
			spans := make([]traces.Span, 0, len(eventRows))
			for _, r := range eventRows {
				spans = append(spans, traces.RowToSpan(r))
			}
			if len(spans) == limit && app.StdoutIsTTY && !app.Quiet {
				_, _ = fmt.Fprintf(app.Stderr,
					"Showing the first %d spans — the trace may be larger; raise -n to see more.\n", limit)
			}
			return printWaterfall(app, spans, barWidth)
		},
	}
	f := cmd.Flags()
	f.StringVar(&since, "since", "", "relative lookback (default 1h)")
	f.IntVarP(&limit, "limit", "n", 500, "max spans to fetch")
	f.IntVar(&barWidth, "bar-width", 40, "waterfall bar width in characters")
	return cmd
}

// printWaterfall renders a trace's spans: a human waterfall for table
// format, streaming rows (jsonl/raw) or a full rows dump (json/csv)
// otherwise — mirroring search.go's printEvents format branching.
func printWaterfall(app *App, spans []traces.Span, barWidth int) error {
	f, err := app.DetectFormat(true)
	if err != nil {
		return err
	}
	if f == output.FormatTable {
		traces.RenderWaterfall(app.Stdout, spans, barWidth, app.Color)
		return nil
	}
	p, err := app.PrinterFor(f)
	if err != nil {
		return err
	}
	rows := traces.WaterfallRows(spans)
	if f == output.FormatJSONL || f == output.FormatRaw {
		for _, r := range rows {
			if err := p.PrintRow(nil, r); err != nil {
				return err
			}
		}
		return nil
	}
	columns := []string{"depth", "service", "operation", "duration", "status", "trace_id", "span_id", "parent_span_id"}
	return p.PrintRows(columns, rows)
}

func newTracesShapeCmd() *cobra.Command {
	var (
		since, service, operation, where string
		errorsOnly                       bool
		minDurationMS                    float64
		entry, anySpan                   bool
		sample, minTraces, barWidth      int
	)
	cmd := &cobra.Command{
		Use:   "shape",
		Short: "Show the merged call-tree shape across a sample of traces",
		Example: "  bronto traces shape -s checkout --sample 20\n" +
			"  bronto traces shape --any-span --operation 'SELECT users'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validatePositive("sample", sample); err != nil {
				return err
			}
			if err := validatePositive("min-traces", minTraces); err != nil {
				return err
			}
			app, agg, err := tracesAgg(cmd, since, "1h")
			if err != nil {
				return err
			}
			useEntry := entry
			if anySpan {
				useEntry = false
			}
			sampleWhere, err := shapeSampleWhere(useEntry, service, operation, errorsOnly, minDurationMS, where)
			if err != nil {
				return err
			}
			traceIDs, err := agg.FindSampleTraceIDs(cmd.Context(), sampleWhere, sample)
			if err != nil {
				return err
			}
			if len(traceIDs) == 0 {
				return clierr.New("trace_not_found", "no traces matched the filter").
					WithHint("Widen the window with --since, or relax --service/--operation/--errors.")
			}
			spans, err := agg.FetchTraceSpans(cmd.Context(), traceIDs)
			if err != nil {
				return err
			}
			if len(spans) == 0 {
				return clierr.New("trace_not_found", "no spans found for the sampled traces").
					WithHint("Widen the window with --since (default 1h).")
			}
			match := traces.EntryMatch{
				EntryOnly: useEntry, Service: service, Operation: operation,
				ErrorsOnly: errorsOnly, MinDurationMS: minDurationMS,
			}
			buckets, tracesUsed := traces.ComputeShape(spans, match)
			visible := traces.FilterMinTraces(buckets, minTraces)
			if len(visible) == 0 {
				return clierr.New("usage_min_traces_too_high",
					fmt.Sprintf("no shape buckets have at least %d traces", minTraces)).
					WithHint("Lower --min-traces or increase --sample.")
			}
			f, err := app.DetectFormat(true)
			if err != nil {
				return err
			}
			if f == output.FormatTable {
				traces.RenderShape(app.Stdout, visible, tracesUsed, len(spans), barWidth, app.Color)
				return nil
			}
			p, err := app.PrinterFor(f)
			if err != nil {
				return err
			}
			rows := traces.ShapeRows(visible)
			if f == output.FormatJSONL || f == output.FormatRaw {
				for _, r := range rows {
					if err := p.PrintRow(nil, r); err != nil {
						return err
					}
				}
				return nil
			}
			columns := []string{"service", "name", "depth", "parent", "samples", "traces",
				"avg_duration_ns", "avg_offset_ns", "errors"}
			return p.PrintRows(columns, rows)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&service, "service", "s", "", "filter by service name")
	f.StringVar(&operation, "operation", "", "filter by operation (span) name")
	f.StringVarP(&where, "where", "w", "", "additional raw WHERE clause")
	f.BoolVar(&errorsOnly, "errors", false, "only error spans/traces")
	f.Float64Var(&minDurationMS, "min-duration-ms", 0, "only entry spans at least this long")
	f.BoolVar(&entry, "entry", true, "sample by entry (server) spans")
	f.BoolVar(&anySpan, "any-span", false, "sample by any matching span, not just entries")
	cmd.MarkFlagsMutuallyExclusive("entry", "any-span")
	f.IntVar(&sample, "sample", 30, "number of traces to sample")
	f.IntVar(&minTraces, "min-traces", 1, "drop buckets present in fewer than this many traces")
	f.StringVar(&since, "since", "", "relative lookback (default 1h)")
	f.IntVar(&barWidth, "bar-width", 50, "waterfall bar width in characters")
	return cmd
}

// shapeSampleWhere builds the sample-selection WHERE clause: an entry
// (server-span) kind filter unless --any-span, plus service/operation/
// errors/duration filters and a parenthesized raw --where (extraction §4.1).
func shapeSampleWhere(entry bool, service, operation string, errorsOnly bool, minDurationMS float64, rawWhere string) (string, error) {
	var clauses []string
	if entry {
		kindClause, err := traces.KindClause("server")
		if err != nil {
			return "", err
		}
		clauses = append(clauses, kindClause)
	}
	if service != "" {
		clauses = append(clauses, "$service.name = "+traces.Quote(service))
	}
	if operation != "" {
		clauses = append(clauses, "$span.name = "+traces.Quote(operation))
	}
	if errorsOnly {
		clauses = append(clauses, traces.ErrorsClause)
	}
	if minDurationMS > 0 {
		clauses = append(clauses, fmt.Sprintf("$span.duration_nano > %d", int64(minDurationMS*1e6)))
	}
	if rawWhere != "" {
		clauses = append(clauses, "("+rawWhere+")")
	}
	return traces.AndJoin(clauses...), nil
}
