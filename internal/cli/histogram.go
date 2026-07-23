package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/output"
)

// histogramBuckets converts sliced count(*) result rows into
// {time,timestamp,count} rows for rendering and machine output.
func histogramBuckets(rows []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		n := toNumber(r["count(*)"])
		ts := toNumber(r["@timestamp"]) // stringly-typed on the wire
		out = append(out, map[string]any{
			"time":      usageBucketTime(r["@time"]),
			"timestamp": int64(ts),
			"count":     n,
		})
	}
	return out
}

// renderHistogram draws unicode bars scaled to the largest bucket. Labels
// keep the day when the range spans more than one.
func renderHistogram(buckets []map[string]any, color bool) string {
	const barMax = 40
	var maxCount, total float64
	for _, b := range buckets {
		n, _ := b["count"].(float64)
		total += n
		if n > maxCount {
			maxCount = n
		}
	}
	multiDay := false
	if len(buckets) > 1 {
		first, _ := numericValue(buckets[0]["timestamp"])
		last, _ := numericValue(buckets[len(buckets)-1]["timestamp"])
		multiDay = time.UnixMilli(int64(last)).UTC().Day() != time.UnixMilli(int64(first)).UTC().Day()
	}
	var sb strings.Builder
	for _, b := range buckets {
		n, _ := b["count"].(float64)
		label := fmt.Sprint(b["time"])
		if ts, ok := numericValue(b["timestamp"]); ok && ts > 0 {
			layout := "15:04"
			if multiDay {
				layout = "01-02 15:04"
			}
			label = time.UnixMilli(int64(ts)).UTC().Format(layout)
		}
		bar := ""
		if maxCount > 0 && n > 0 {
			w := int(n / maxCount * barMax)
			if w < 1 {
				w = 1
			}
			bar = strings.Repeat("█", w)
		}
		if color && bar != "" {
			bar = "\x1b[36m" + bar + "\x1b[0m"
		}
		_, _ = fmt.Fprintf(&sb, "%s  %s %s\n", label, bar, formatCount(n))
	}
	bucketNote := ""
	if len(buckets) > 1 {
		a, _ := numericValue(buckets[0]["timestamp"])
		bb, _ := numericValue(buckets[1]["timestamp"])
		if d := time.Duration(int64(bb-a)) * time.Millisecond; d > 0 {
			bucketNote = fmt.Sprintf(" (bucket: %s)", d.Round(time.Second))
		}
	}
	_, _ = fmt.Fprintf(&sb, "total: %s events%s\n", formatCount(total), bucketNote)
	return sb.String()
}

func formatCount(n float64) string {
	if n == float64(int64(n)) {
		return fmt.Sprintf("%d", int64(n))
	}
	return fmt.Sprintf("%.1f", n)
}

// runHistogram executes the sliced count query and renders/prints it.
func runHistogram(ctx context.Context, app *App, client *bronto.Client, req bronto.SearchRequest, slices int) error {
	if slices <= 0 {
		slices = 24
	}
	req.Select = []string{"count(*)"}
	req.Groups = nil
	req.Slices = slices
	req.Limit = 0
	resp, err := client.Search(ctx, req)
	if err != nil {
		return err
	}
	buckets := histogramBuckets(resp.SelectedRows())
	format, err := app.DetectFormat(false)
	if err != nil {
		return err
	}
	p, err := app.PrinterFor(format)
	if err != nil {
		return err
	}
	if format != output.FormatTable {
		return p.PrintRows([]string{"time", "timestamp", "count"}, buckets)
	}
	if len(buckets) == 0 {
		if !app.Quiet {
			_, _ = fmt.Fprintln(app.Stderr, "No results.")
		}
		return nil
	}
	_, err = fmt.Fprint(app.Stdout, renderHistogram(buckets, app.Color))
	return err
}
