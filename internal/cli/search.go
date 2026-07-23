package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/bronto-community/bronto-cli/internal/patterns"
	"github.com/bronto-community/bronto-cli/internal/query"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

func newSearchCmd() *cobra.Command {
	var (
		saved           string
		showPatterns    bool
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
			// --saved fills defaults BEFORE dataset resolution so its
			// stored from-ids participate in scope selection.
			var savedTimeRange string
			if saved != "" {
				sd, tr, serr := loadSavedSearch(cmd.Context(), app, saved)
				if serr != nil {
					return serr
				}
				savedTimeRange = tr
				if where == "" && len(args) == 0 {
					where = sd.where
				}
				if len(datasets) == 0 && fromExpr == "" && len(sd.from) > 0 {
					datasets = sd.from
				}
			}
			ids, expr, err := resolveDataset(cmd.Context(), app, datasets, fromExpr)
			if err != nil {
				return err
			}
			if showPatterns && (len(selects) > 0 || len(groups) > 0 || explainOnly) {
				return clierr.New("usage_invalid_flags",
					"--patterns clusters raw events and cannot combine with --select, --group-by, or --explain-only")
			}
			if showPatterns && !cmd.Flags().Changed("limit") {
				limit = 2000 // clustering wants a real sample, not the default page
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
			if spec.IsZero() && savedTimeRange != "" {
				spec.TimeRange = savedTimeRange
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
				return enrichQueryError(err, where)
			}
			if showPatterns {
				return printPatterns(app, resp.EventRows())
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
				return printEvents(app, events)
			}
		},
	}
	f := cmd.Flags()
	f.StringVar(&saved, "saved", "", "run a saved search by name or id (explicit query/dataset/time flags override its stored values)")
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
	f.BoolVar(&showPatterns, "patterns", false, "cluster matching events into templates with counts (drain-style)")
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
	if f == output.FormatTable {
		return p.PrintRows(eventTableColumns(rows), rows)
	}
	return p.PrintRows(bronto.EventColumns(rows, 0), rows)
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
	return bronto.EventColumns(filtered, 8)
}

type savedSearchDetails struct {
	where string
	from  []string
}

// loadSavedSearch resolves a saved search by name/id and extracts its
// search_details as defaults for the search command: stored where, the
// colon-separated from log-ids, and the stored time_range (returned
// separately — it feeds the timerange spec only when no time flags are
// given).
func loadSavedSearch(ctx context.Context, app *App, ref string) (savedSearchDetails, string, error) {
	id, err := resolveKindRef(ctx, app, "saved-searches", ref)
	if err != nil {
		return savedSearchDetails{}, "", err
	}
	payload, err := doJSONRequest(ctx, app, http.MethodGet, "/saved-searches/"+url.PathEscape(id), nil)
	if err != nil {
		return savedSearchDetails{}, "", err
	}
	obj, _ := payload.(map[string]any)
	sd, _ := obj["search_details"].(map[string]any)
	if sd == nil {
		return savedSearchDetails{}, "", clierr.New("saved_search_invalid",
			"saved search has no search_details to run")
	}
	out := savedSearchDetails{}
	if w, _ := sd["where"].(string); w != "" {
		out.where = w
	}
	if f, _ := sd["from"].(string); f != "" {
		out.from = strings.Split(f, ":")
	}
	tr, _ := sd["time_range"].(string)
	return out, tr, nil
}

// printPatterns clusters the fetched events' raw lines into templates.
func printPatterns(app *App, events []map[string]any) error {
	lines := make([]string, 0, len(events))
	for _, ev := range events {
		line := ""
		if v, ok := ev["@raw"]; ok && v != nil {
			line = fmt.Sprint(v)
		}
		if line == "" {
			if v, ok := ev["message"]; ok && v != nil {
				line = fmt.Sprint(v)
			}
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	clusters := patterns.Extract(lines)
	rows := make([]map[string]any, 0, len(clusters))
	for _, c := range clusters {
		rows = append(rows, map[string]any{
			"count":   c.Count,
			"pattern": c.Template,
			"example": c.Example,
		})
	}
	p, err := app.Printer(false)
	if err != nil {
		return err
	}
	if !app.Quiet && len(rows) > 0 {
		_, _ = fmt.Fprintf(app.Stderr, "%d pattern(s) from %d event(s)\n", len(rows), len(lines))
	}
	return p.PrintRows([]string{"count", "pattern"}, rows)
}

// enrichQueryError attaches a local parse diagnosis (caret included) to a
// server-side 400 when our parser also rejects the query. Advisory only:
// the local grammar is narrower than the server's, so parse failures
// never block a request — they just explain a rejection after the fact.
func enrichQueryError(err error, where string) error {
	var ce *clierr.Error
	if where == "" || !errors.As(err, &ce) || ce.Code != "api_error" {
		return err
	}
	if _, perr := query.Parse(where); perr != nil {
		var pe *query.ParseError
		if errors.As(perr, &pe) {
			return ce.WithHint("Local query check: " + pe.Msg + "\n  " +
				strings.ReplaceAll(pe.Caret(where), "\n", "\n  "))
		}
	}
	return err
}
