package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
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
		expand          bool
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
			if expand {
				if len(groups) > 0 || explainOnly {
					return clierr.New("usage_invalid_flags",
						"-x/--expand applies to event results, not aggregates or query plans").
						WithHint("Drop -x, or drop -g/--group-by and --explain-only.")
				}
				f, err := app.DetectFormat(true)
				if err != nil {
					return err
				}
				if f != output.FormatTable {
					return clierr.New("usage_invalid_flags",
						"-x/--expand requires table output").
						WithHint("json/jsonl already carry every field; pass -o table to force the expanded view in a pipe.")
				}
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
			if limit < 1 || limit > 10000 {
				return clierr.New("usage_invalid_limit",
					fmt.Sprintf("limit must be between 1 and 10000, got %d", limit)).
					WithHint("The API caps event queries at 10000 results; use pagination or narrower time ranges for more.")
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
				// "*" makes the API populate the parsed KVs (message_kvs)
				// in events; without it only @raw comes back and the human
				// table has nothing to promote. "@raw" must stay explicit:
				// under a bare "*" the API nulls it out (probed live
				// 2026-07-22 against do11y/docs-analytics).
				effSelect = []string{"@time", "@raw", "*"}
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
				if len(selects) > 0 {
					// The user asked for specific columns: honor the
					// projection (EventRows would silently ignore --select).
					events = resp.SelectedRows()
				}
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
				view := eventView{expand: expand}
				if len(selects) == 0 {
					view.teachRef = "<dataset>"
					if len(datasets) > 0 {
						view.teachRef = datasets[0]
					}
				}
				return printEvents(app, events, view)
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
	f.BoolVarP(&expand, "expand", "x", false, "expanded record view: every field of every event, one per line (table output only)")
	return cmd
}

// eventView controls the human rendering of printEvents: expand switches
// to the vertical detail view; teachRef (the dataset ref as the user
// typed it, or "<dataset>") enables the discoverability footer for
// default, unprojected tables — empty disables it.
type eventView struct {
	expand   bool
	teachRef string
}

// printEvents renders event rows: streaming row-by-row for jsonl/raw,
// a capped-column table or full rows otherwise.
func printEvents(app *App, events []map[string]any, view eventView) error {
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
	if f == output.FormatTable {
		if view.expand {
			return p.PrintExpanded(rows, bronto.PriorityEventKeys, app.Color)
		}
		if err := p.PrintRows(eventTableColumns(rows), rows); err != nil {
			return err
		}
		if view.teachRef != "" && !app.Quiet && app.StdoutIsTTY && len(rows) > 0 {
			ref := view.teachRef
			if strings.ContainsAny(ref, " \t'\"") {
				ref = fmt.Sprintf("%q", ref)
			}
			_, _ = fmt.Fprintf(app.Stderr,
				"%s. %s available — 'bronto fields -d %s' lists them; '--select <field,...>' picks columns; '-x' expands a row.\n",
				countNoun(len(rows), "result"), countNoun(teachableFieldCount(rows), "field"), ref)
		}
		return nil
	}
	return p.PrintRows(bronto.EventColumns(rows, 0), rows)
}

// teachableFieldCount is the union of flattened event keys, excluding the
// plumbing (links, metadata.*) the table never promotes.
func teachableFieldCount(rows []map[string]any) int {
	seen := map[string]struct{}{}
	for _, r := range rows {
		for k := range r {
			if k == "links" || strings.HasPrefix(k, "metadata.") {
				continue
			}
			seen[k] = struct{}{}
		}
	}
	return len(seen)
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// eventTableColumns picks the human table columns for event rows: the
// usual priority/discovery order, but with the bulky plumbing fields
// (links, metadata.*) excluded — they turned the default search table
// into hundreds of characters per row. json/jsonl/csv keep every field;
// --fields overrides this selection entirely.
func eventTableColumns(rows []map[string]any) []string {
	filtered := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		fr := make(map[string]any, len(r))
		for k, v := range r {
			if k == "links" || strings.HasPrefix(k, "metadata.") {
				continue
			}
			fr[k] = v
		}
		filtered = append(filtered, fr)
	}
	return bronto.EventColumnsByFrequency(filtered, 8)
}
