package cli

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func newContextCmd() *cobra.Command {
	var sequence, timestamp int64
	var dataset, direction string
	var limit int
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Show events around a specific log event",
		Example: "  bronto context --sequence 111721913 -d <dataset> --timestamp 1711535140632\n" +
			"  bronto context --sequence 42 -d <dataset> --timestamp 1711535140632 --direction before",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch direction {
			case "before", "after", "both":
			default:
				return clierr.New("usage_invalid_direction",
					fmt.Sprintf("direction must be before, after, or both; got %q", direction))
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			logID, err := resolveDatasetRef(cmd.Context(), app, dataset)
			if err != nil {
				return err
			}
			params := url.Values{
				"sequence":  []string{strconv.FormatInt(sequence, 10)},
				"from":      []string{logID},
				"timestamp": []string{strconv.FormatInt(timestamp, 10)},
				"direction": []string{direction},
				"limit":     []string{strconv.Itoa(limit)},
			}
			var payload map[string]any
			client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
			if err := client.GetJSON(cmd.Context(), "/context", params, &payload); err != nil {
				return err
			}
			var events []map[string]any
			for _, field := range []string{"events", "result", "data"} {
				if list, ok := payload[field].([]any); ok {
					for _, item := range list {
						if m, ok := item.(map[string]any); ok {
							events = append(events, m)
						}
					}
					break
				}
			}
			return printEvents(app, events, eventView{})
		},
	}
	f := cmd.Flags()
	f.Int64Var(&sequence, "sequence", 0, "sequence number of the anchor event (required)")
	f.StringVarP(&dataset, "dataset", "d", "", "dataset (name or UUID) the event belongs to (required)")
	f.Int64Var(&timestamp, "timestamp", 0, "unix-ms timestamp of the anchor event (required)")
	f.StringVar(&direction, "direction", "both", "before | after | both")
	f.IntVarP(&limit, "limit", "n", 50, "events per direction")
	_ = cmd.MarkFlagRequired("sequence")
	_ = cmd.MarkFlagRequired("dataset")
	_ = cmd.MarkFlagRequired("timestamp")
	return cmd
}
