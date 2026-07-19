package cli

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

// newUsageCmd implements "bronto usage": GET /usage with a time_range query
// param (same single-unit rule as the fields command — the endpoint only
// accepts relative ranges, so a compound --since that resolves to absolute
// from_ts/to_ts bounds is rejected).
func newUsageCmd() *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show ingestion/search/export usage over a time period",
		Long: "Show ingestion/search/export usage over a time period.\n\n" +
			"For per-dataset usage, use: bronto api GET /usage/organizations/logs",
		Example: "  bronto usage --since 7d\n" +
			"  bronto usage --since 1h",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			spec, err := timerange.Resolve(since, "", "", nil)
			if err != nil {
				return err
			}
			if spec.IsZero() {
				spec.TimeRange = "Last 7 days"
			}
			if spec.TimeRange == "" { // compound --since resolved to absolute bounds
				return clierr.New("usage_invalid_since",
					"usage supports only single-unit --since values (e.g. 90m, 2h, 7d)").
					WithHint("The /usage endpoint accepts relative ranges only.")
			}
			params := url.Values{"time_range": []string{spec.TimeRange}}
			var payload any
			client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
			if err := client.GetJSON(cmd.Context(), "/usage", params, &payload); err != nil {
				return err
			}
			format, err := app.DetectFormat(false)
			if err != nil {
				return err
			}
			p, err := app.PrinterFor(format)
			if err != nil {
				return err
			}
			// Machine formats get the API payload verbatim; the human view
			// flattens each query's series into per-bucket rows — the raw
			// envelope (filter/group_keys/aux) buries the actual numbers.
			if format != output.FormatTable && format != output.FormatCSV {
				return p.PrintJSON(payload)
			}
			cols, rows := usageSeriesRows(rowsFromPayload(payload))
			return p.PrintRows(cols, rows)
		},
	}
	cmd.Flags().StringVar(&since, "since", "7d", "relative lookback (single unit: 1h, 7d)")
	return cmd
}

// usageSeriesRows flattens /usage query objects into human rows: one per
// series bucket, with events (count) and a humanized value ("21.7 kB" when
// the query key is bytes-shaped). A metric column is prepended when the
// response carries more than one named query.
func usageSeriesRows(queries []map[string]any) ([]string, []map[string]any) {
	var rows []map[string]any
	multi := len(queries) > 1
	for _, q := range queries {
		name, _ := q["name"].(string)
		key, _ := q["key"].(string)
		series, _ := q["series"].([]any)
		for _, pt := range series {
			m, ok := pt.(map[string]any)
			if !ok {
				continue
			}
			row := map[string]any{
				"time":   usageBucketTime(m["@time"]),
				"events": toNumber(m["count"]),
			}
			val := toNumber(m["value"])
			if strings.Contains(key, "byte") {
				row["volume"] = humanBytes(val)
			} else {
				row["volume"] = val
			}
			if multi {
				row["metric"] = name
			}
			rows = append(rows, row)
		}
	}
	cols := []string{"time", "events", "volume"}
	if multi {
		cols = append([]string{"metric"}, cols...)
	}
	return cols, rows
}

// usageBucketTime compacts the API's verbose bucket label ("Fri Jul 10
// 18:53:00 UTC 2026") to "2026-07-10 18:53"; unknown layouts pass through.
func usageBucketTime(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	if t, err := time.Parse("Mon Jan 2 15:04:05 MST 2006", s); err == nil {
		return t.Format("2006-01-02 15:04")
	}
	return s
}

// toNumber coerces the API's stringly-typed series numbers ("21667.0",
// "6") to float64; non-numeric values become 0.
func toNumber(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case json.Number:
		if n, err := t.Float64(); err == nil {
			return n
		}
	case string:
		if n, err := strconv.ParseFloat(t, 64); err == nil {
			return n
		}
	}
	return 0
}
