package cli

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

// trendLen is how many intervals the per-group sparkline remembers.
const trendLen = 12

// runTailAggregate is tail's aggregate mode (angle-grinder pattern): the
// same group-by query re-run every interval, redrawn in place at a TTY —
// a tiny live dashboard. Piped output emits one JSONL snapshot per tick.
func runTailAggregate(ctx context.Context, app *App, client *bronto.Client,
	ids []string, expr, where string, spec timerange.Spec,
	selects, groups []string, interval time.Duration, noFollow bool) error {

	if len(selects) == 0 {
		selects = []string{"count(*)"}
	}
	format, err := app.DetectFormat(true)
	if err != nil {
		return err
	}
	human := format == output.FormatTable
	var p *output.Printer
	if !human {
		if format == output.FormatJSON || format == output.FormatCSV || format == output.FormatRaw {
			return errAggFormat(format)
		}
		if p, err = app.PrinterFor(format); err != nil {
			return err
		}
	}
	if human && !app.Quiet && !noFollow {
		_, _ = fmt.Fprintf(app.Stderr, "Live aggregate over %s (every %s, Ctrl-C to stop).\n",
			strings.ToLower(spec.TimeRange), interval)
	}

	history := map[string][]float64{}
	lastLines := 0
	warnedMissing := false
	for {
		req := bronto.SearchRequest{
			From: ids, FromExpr: expr, Time: spec, Where: where,
			Select: selects, Groups: groups, Limit: 50,
		}
		resp, err := client.Search(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err() // SIGINT mid-request: main exits 130 without an error message
			}
			return err
		}
		rows := aggregateRows(resp, selects, groups)
		if missing := missingSelects(rows, selects); len(missing) > 0 && !warnedMissing && !app.Quiet {
			warnedMissing = true
			_, _ = fmt.Fprintf(app.Stderr, "Warning: the API returned no values for %s; they show as - (null in JSON Lines).\n",
				strings.Join(missing, ", "))
		}
		now := time.Now()
		for _, r := range rows {
			key, _ := r["group"].(string)
			v, ok := r[selects[0]].(float64)
			if !ok {
				v = math.NaN() // no value: a gap in the trend
			}
			history[key] = append(history[key], v)
			if h := history[key]; len(h) > trendLen {
				history[key] = h[len(h)-trendLen:]
			}
			r["trend"] = sparkline(history[key])
		}
		if human {
			frame := renderAggFrame(rows, selects, groups, now)
			if lastLines > 0 {
				_, _ = fmt.Fprintf(app.Stdout, "\x1b[%dA\x1b[J", lastLines)
			}
			_, _ = fmt.Fprint(app.Stdout, frame)
			lastLines = strings.Count(frame, "\n")
		} else {
			for _, r := range rows {
				r["ts"] = now.UTC().Format(time.RFC3339)
				if err := p.PrintRow(nil, r); err != nil {
					return err
				}
			}
		}
		if noFollow {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func errAggFormat(f output.Format) error {
	return clierr.New("usage_invalid_output_format",
		fmt.Sprintf("tail aggregates stream snapshots and cannot produce %s; use jsonl or table", f))
}

// formatAggValue renders aggregate values: integral counts without
// decimals, everything else with two.
func formatAggValue(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// aggregateRows normalizes group/aggregate responses into printable rows,
// one per group with a column per select. Grouped responses carry one
// entry per (group, stat) pair, so they are pivoted on the stat; a select
// the response has no value for stays nil. Without groups the totals are
// the single row: result then holds a time series, one row per bucket.
func aggregateRows(resp *bronto.SearchResponse, selects, groups []string) []map[string]any {
	if len(groups) == 0 {
		src := resp.Totals
		if !hasAnyKey(src, selects) {
			rows := resp.SelectedRows()
			if len(rows) == 0 {
				return nil
			}
			src = rows[0]
		}
		row := map[string]any{"group": "total"}
		for _, sel := range selects {
			row[sel] = numberOrNil(src[sel])
		}
		return []map[string]any{row}
	}

	isSelect := map[string]bool{}
	for _, sel := range selects {
		isSelect[sel] = true
	}
	var rows []map[string]any
	byGroup := map[string]map[string]any{}
	for _, r := range resp.GroupRows() {
		key := fmt.Sprint(r["group"])
		row, ok := byGroup[key]
		if !ok {
			row = map[string]any{"group": key}
			for _, sel := range selects {
				row[sel] = nil
			}
			byGroup[key] = row
			rows = append(rows, row)
		}
		stat, hasStat := r["stat"].(string)
		switch {
		case hasStat && isSelect[stat]:
			row[stat] = numberOrNil(r["value"])
		case hasStat && len(selects) == 1:
			// The API spelled the one stat differently; it can only be this select.
			row[selects[0]] = numberOrNil(r["value"])
		case hasStat:
			// A stat that matches no select: leave every column alone.
		default:
			for _, sel := range selects {
				if v, ok := r[sel]; ok {
					row[sel] = numberOrNil(v)
				}
			}
			if len(selects) == 1 && row[selects[0]] == nil {
				row[selects[0]] = numberOrNil(r["value"])
			}
		}
	}
	return rows
}

func hasAnyKey(m map[string]any, keys []string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// numberOrNil converts an aggregate value to float64, keeping a missing
// value nil so it prints as "-" (table) or null (JSONL), not as 0.
func numberOrNil(v any) any {
	if v == nil {
		return nil
	}
	return toNumber(v)
}

// missingSelects lists the selects that no row has a value for.
func missingSelects(rows []map[string]any, selects []string) []string {
	if len(rows) == 0 {
		return nil
	}
	var missing []string
	for _, sel := range selects {
		found := false
		for _, r := range rows {
			if r[sel] != nil {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, sel)
		}
	}
	return missing
}

var sparkChars = []rune("▁▂▃▄▅▆▇█")

// sparkline renders vals scaled over their own [min, max] range, with the
// floor at zero unless a value is negative. A flat series renders as the
// highest block if it is above zero and the lowest otherwise; NaN/Inf
// values render as a gap.
func sparkline(vals []float64) string {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range vals {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		lo = math.Min(lo, v)
		hi = math.Max(hi, v)
	}
	// A series that has always been above zero keeps zero as its floor,
	// so steady traffic does not render as a flat line at the bottom.
	if lo > 0 {
		lo = 0
	}
	top := len(sparkChars) - 1
	var sb strings.Builder
	for _, v := range vals {
		switch {
		case math.IsNaN(v) || math.IsInf(v, 0):
			sb.WriteRune(' ')
		case hi <= lo:
			if v <= 0 {
				sb.WriteRune(sparkChars[0])
			} else {
				sb.WriteRune(sparkChars[top])
			}
		default:
			idx := int((v - lo) / (hi - lo) * float64(top))
			sb.WriteRune(sparkChars[max(0, min(idx, top))])
		}
	}
	return sb.String()
}

func renderAggFrame(rows []map[string]any, selects, groups []string, now time.Time) string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 2, 4, 2, ' ', 0)
	header := "GROUP"
	if len(groups) > 0 {
		header = strings.ToUpper(strings.Join(groups, ","))
	}
	cols := make([]string, 0, len(selects)+2)
	cols = append(cols, header)
	for _, s := range selects {
		cols = append(cols, strings.ToUpper(s))
	}
	cols = append(cols, "TREND")
	_, _ = fmt.Fprintln(tw, strings.Join(cols, "\t"))
	for _, r := range rows {
		vals := make([]string, 0, len(selects)+2)
		vals = append(vals, fmt.Sprint(r["group"]))
		for _, s := range selects {
			if r[s] == nil {
				vals = append(vals, "-")
				continue
			}
			vals = append(vals, formatAggValue(toNumber(r[s])))
		}
		vals = append(vals, fmt.Sprint(r["trend"]))
		_, _ = fmt.Fprintln(tw, strings.Join(vals, "\t"))
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintf(&buf, "updated %s\n", now.Format("15:04:05"))
	return buf.String()
}
