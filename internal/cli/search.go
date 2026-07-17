package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

func newSearchCmd() *cobra.Command {
	var (
		datasets        []string
		fromExpr        string
		since, from, to string
		selects         []string
		groups          []string
		slices          int
		limit           int
		orderBy         string
		oldestFirst     bool
		explainOnly     bool
	)
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Run a one-shot query against Bronto",
		Long: "Runs a query (a Bronto WHERE expression) against one or more datasets.\n" +
			"Pass '-' as the query to read it from stdin.",
		Example: "  bronto search \"status >= 500\" --since 1h\n" +
			"  bronto search \"level = 'error'\" -d <dataset-uuid> --limit 50\n" +
			"  bronto search --select \"count()\" -g host --since 15m\n" +
			"  bronto search --explain-only --since 1d",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			where := ""
			if len(args) == 1 {
				where = args[0]
				if where == "-" {
					b, err := io.ReadAll(cmd.InOrStdin())
					if err != nil {
						return err
					}
					where = strings.TrimSpace(string(b))
				}
			}
			ids, expr, err := resolveDataset(cmd.Context(), app, datasets, fromExpr)
			if err != nil {
				return err
			}
			spec, err := timerange.Resolve(since, from, to, nil)
			if err != nil {
				return err
			}
			if spec.IsZero() {
				spec.TimeRange = "Last 15 minutes"
			}
			effSelect := selects
			if len(effSelect) == 0 && len(groups) == 0 && !explainOnly {
				effSelect = []string{"@time", "@raw"}
			}
			req := bronto.SearchRequest{
				From: ids, FromExpr: expr, Time: spec, Where: where,
				Select: effSelect, Groups: groups, Limit: limit, Slices: slices,
				OrderBy: orderBy, ExplainOnly: explainOnly,
			}
			if oldestFirst {
				mrf := false
				req.MostRecentFirst = &mrf
			}
			client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
			resp, err := client.Search(cmd.Context(), req)
			if err != nil {
				return err
			}
			if !app.Quiet && app.StdoutIsTTY {
				if ms, ok := resp.Explain["Execution time (millis)"]; ok {
					_, _ = fmt.Fprintf(app.Stderr, "Execution time: %v ms\n", ms)
				}
			}
			switch {
			case explainOnly:
				p, err := app.Printer(false)
				if err != nil {
					return err
				}
				return p.PrintJSON(resp.Explain)
			case len(resp.Groups) > 0 || len(groups) > 0:
				p, err := app.Printer(false)
				if err != nil {
					return err
				}
				rows := resp.GroupRows()
				if len(rows) == 0 && len(resp.GroupsSeries) > 0 {
					rows = resp.GroupsSeries
				}
				return p.PrintRows(bronto.EventColumns(rows, 0), rows)
			default:
				events := resp.EventRows()
				if len(events) == 0 {
					p, err := app.Printer(false)
					if err != nil {
						return err
					}
					switch {
					case len(resp.GroupsSeries) > 0:
						return p.PrintRows(bronto.EventColumns(resp.GroupsSeries, 0), resp.GroupsSeries)
					case len(resp.Totals) > 0:
						rows := []map[string]any{resp.Totals}
						return p.PrintRows(bronto.EventColumns(rows, 0), rows)
					}
				}
				return printEvents(app, events)
			}
		},
	}
	f := cmd.Flags()
	f.StringArrayVarP(&datasets, "dataset", "d", nil, "dataset name or UUID to search (repeatable)")
	f.StringVar(&fromExpr, "from-expr", "", "dataset selector expression, e.g. \"log_id = '<uuid>'\"")
	f.StringVar(&since, "since", "", "relative lookback: 30s, 15m, 1h, 2d, 1w, 1h30m")
	f.StringVar(&from, "from", "", "absolute start (RFC3339)")
	f.StringVar(&to, "to", "", "absolute end (RFC3339), requires --from")
	f.StringArrayVar(&selects, "select", nil, "column or aggregate to select (repeatable)")
	f.StringArrayVarP(&groups, "group-by", "g", nil, "group-by key (repeatable)")
	f.IntVar(&slices, "slices", 0, "timeseries buckets for aggregate queries")
	f.IntVarP(&limit, "limit", "n", 100, "maximum events to return (1-10000)")
	f.StringVar(&orderBy, "order-by", "", "SQL-style order, e.g. 'duration_ms DESC'")
	f.BoolVar(&oldestFirst, "oldest-first", false, "return oldest events first")
	f.BoolVar(&explainOnly, "explain-only", false, "return only the query plan / cost estimate")
	return cmd
}

// printEvents renders event rows: streaming row-by-row for jsonl/raw,
// a capped-column table or full rows otherwise.
func printEvents(app *App, events []map[string]any) error {
	rows := make([]map[string]any, 0, len(events))
	for _, e := range events {
		rows = append(rows, bronto.Flatten(e))
	}
	f, err := app.DetectFormat(true)
	if err != nil {
		return err
	}
	p, err := app.PrinterFor(f)
	if err != nil {
		return err
	}
	if f == output.FormatJSONL || f == output.FormatRaw {
		for _, r := range rows {
			if err := p.PrintRow(nil, r); err != nil {
				return err
			}
		}
		return nil
	}
	max := 0
	if f == output.FormatTable {
		max = 8
	}
	return p.PrintRows(bronto.EventColumns(rows, max), rows)
}
