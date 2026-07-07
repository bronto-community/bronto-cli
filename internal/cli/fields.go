package cli

import (
	"net/url"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/bronto"
	"github.com/svrnm/bronto-cli/internal/clierr"
	"github.com/svrnm/bronto-cli/internal/timerange"
)

func newFieldsCmd() *cobra.Command {
	var dataset, since string
	var limit int
	cmd := &cobra.Command{
		Use:   "fields",
		Short: "Discover field names (top keys) in a dataset",
		Example: "  bronto fields -d <dataset-uuid> --since 1h\n" +
			"  bronto fields --since 15m -n 20",
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
				spec.TimeRange = "Last 1 hour"
			}
			if spec.TimeRange == "" { // compound --since resolved to absolute bounds
				return clierr.New("usage_invalid_since",
					"fields supports only single-unit --since values (e.g. 90m, 2h)").
					WithHint("The /top-keys endpoint accepts relative ranges only.")
			}
			params := url.Values{"time_range": []string{spec.TimeRange}}
			if dataset != "" {
				params.Set("log_id", dataset)
			}
			if limit > 0 {
				params.Set("limit", strconv.Itoa(limit))
			}
			var payload map[string]any
			client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
			if err := client.GetJSON(cmd.Context(), "/top-keys", params, &payload); err != nil {
				return err
			}
			rows := normalizeTopKeys(payload)
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows([]string{"key", "count"}, rows)
		},
	}
	cmd.Flags().StringVarP(&dataset, "dataset", "d", "", "dataset UUID (omit for all datasets)")
	cmd.Flags().StringVar(&since, "since", "1h", "relative lookback (single unit: 30s, 15m, 1h, 2d)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "maximum keys to return")
	return cmd
}

func normalizeTopKeys(payload map[string]any) []map[string]any {
	for _, field := range []string{"top_keys", "keys", "data"} {
		if list, ok := payload[field].([]any); ok {
			rows := make([]map[string]any, 0, len(list))
			for _, item := range list {
				if m, ok := item.(map[string]any); ok {
					rows = append(rows, m)
				} else {
					rows = append(rows, map[string]any{"value": item})
				}
			}
			return rows
		}
	}
	// flat {key: numericCount} object
	var rows []map[string]any
	for k, v := range payload {
		if n, ok := v.(float64); ok {
			rows = append(rows, map[string]any{"key": k, "count": n})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i]["count"].(float64) > rows[j]["count"].(float64)
	})
	return rows
}
