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
	if human && !app.Quiet {
		_, _ = fmt.Fprintf(app.Stderr, "Live aggregate over %s (every %s, Ctrl-C to stop).\n",
			strings.ToLower(spec.TimeRange), interval)
	}

	history := map[string][]float64{}
	lastLines := 0
	for {
		req := bronto.SearchRequest{
			From: ids, FromExpr: expr, Time: spec, Where: where,
			Select: selects, Groups: groups, Limit: 50,
		}
		resp, err := client.Search(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // SIGINT: clean exit by contract
			}
			return err
		}
		rows := aggregateRows(resp, selects, groups)
		now := time.Now()
		for _, r := range rows {
			key, _ := r["group"].(string)
			v, _ := r[selects[0]].(float64)
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
			return nil
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

// aggregateRows normalizes group/aggregate responses into printable rows.
func aggregateRows(resp *bronto.SearchResponse, selects, groups []string) []map[string]any {
	var src []map[string]any
	if len(groups) > 0 {
		src = resp.GroupRows()
	} else {
		src = resp.SelectedRows()
	}
	rows := make([]map[string]any, 0, len(src))
	for _, r := range src {
		row := map[string]any{}
		if g, ok := r["group"]; ok {
			row["group"] = fmt.Sprint(g)
		} else if len(groups) == 0 {
			row["group"] = "total"
		}
		for _, sel := range selects {
			v := r[sel]
			if v == nil {
				v = r["value"] // groups rows carry the value here
			}
			row[sel] = toNumber(v)
		}
		rows = append(rows, row)
	}
	return rows
}

var sparkChars = []rune("▁▂▃▄▅▆▇█")

// sparkline renders vals scaled to their own max.
func sparkline(vals []float64) string {
	var max float64
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	if max == 0 {
		return strings.Repeat("▁", len(vals))
	}
	var sb strings.Builder
	for _, v := range vals {
		idx := int(v / max * float64(len(sparkChars)-1))
		sb.WriteRune(sparkChars[idx])
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
			vals = append(vals, formatAggValue(toNumber(r[s])))
		}
		vals = append(vals, fmt.Sprint(r["trend"]))
		_, _ = fmt.Fprintln(tw, strings.Join(vals, "\t"))
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintf(&buf, "updated %s\n", now.Format("15:04:05"))
	return buf.String()
}
