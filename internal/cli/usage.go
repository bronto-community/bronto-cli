package cli

import (
	"net/url"

	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/bronto"
	"github.com/svrnm/bronto-cli/internal/clierr"
	"github.com/svrnm/bronto-cli/internal/timerange"
)

// newUsageCmd implements "bronto usage": GET /usage with a time_range query
// param (same single-unit rule as the fields command — the endpoint only
// accepts relative ranges, so a compound --since that resolves to absolute
// from_ts/to_ts bounds is rejected) and log_id when --dataset is given.
func newUsageCmd() *cobra.Command {
	var dataset, since string
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show ingestion/search/export usage over a time period",
		Example: "  bronto usage --since 7d\n" +
			"  bronto usage --since 1h --dataset <dataset-uuid>",
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
			if dataset != "" {
				params.Set("log_id", dataset)
			}
			var payload any
			client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
			if err := client.GetJSON(cmd.Context(), "/usage", params, &payload); err != nil {
				return err
			}
			rows := rowsFromPayload(payload)
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows(bronto.EventColumns(rows, 8), rows)
		},
	}
	cmd.Flags().StringVarP(&dataset, "dataset", "d", "", "dataset UUID (omit for usage across all datasets)")
	cmd.Flags().StringVar(&since, "since", "7d", "relative lookback (single unit: 1h, 7d)")
	return cmd
}
