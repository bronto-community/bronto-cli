package cli

import (
	"net/url"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

func newFieldsCmd() *cobra.Command {
	var dataset, since string
	var limit int
	cmd := &cobra.Command{
		Use:   "fields",
		Short: "Discover field names (top keys) in a dataset",
		Example: "  bronto fields -d <dataset> --since 1h\n" +
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
				logID, err := resolveDatasetRef(cmd.Context(), app, dataset)
				if err != nil {
					return err
				}
				params.Set("log_id", logID)
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
	cmd.Flags().StringVarP(&dataset, "dataset", "d", "", "dataset name or UUID (omit for all datasets)")
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

	// Live shape: {"<log-id>": {"<key>": {rank, type, field_type, values}}}
	// (response additionalProperties -> TopKeys -> TopKeysPerLogOrMetric),
	// with per-key metadata one or two map levels down. Ranks are summed
	// when the same key appears under multiple logs.
	counts := map[string]float64{}
	collect := func(key string, meta map[string]any) {
		if n, ok := meta["rank"].(float64); ok {
			counts[key] += n
		} else if _, seen := counts[key]; !seen {
			counts[key] = 0
		}
	}
	for k, v := range payload {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if isTopKeyMeta(m) {
			collect(k, m)
			continue
		}
		for innerKey, innerVal := range m {
			if im, ok := innerVal.(map[string]any); ok && isTopKeyMeta(im) {
				collect(innerKey, im)
			}
		}
	}
	if len(counts) > 0 {
		rows := make([]map[string]any, 0, len(counts))
		for k, n := range counts {
			rows = append(rows, map[string]any{"key": k, "count": n})
		}
		sortKeyCountRows(rows)
		return rows
	}

	// flat {key: numericCount} object
	var rows []map[string]any
	for k, v := range payload {
		if n, ok := v.(float64); ok {
			rows = append(rows, map[string]any{"key": k, "count": n})
		}
	}
	sortKeyCountRows(rows)
	return rows
}

// isTopKeyMeta reports whether m looks like a TopKeysPerLogOrMetric object
// (per-key metadata) rather than another level of key nesting.
func isTopKeyMeta(m map[string]any) bool {
	for _, field := range []string{"type", "field_type", "rank"} {
		if _, ok := m[field]; ok {
			return true
		}
	}
	return false
}

// sortKeyCountRows orders rows by count descending, then key ascending so
// equal-count keys print deterministically.
func sortKeyCountRows(rows []map[string]any) {
	sort.Slice(rows, func(i, j int) bool {
		ci, _ := rows[i]["count"].(float64)
		cj, _ := rows[j]["count"].(float64)
		if ci != cj {
			return ci > cj
		}
		ki, _ := rows[i]["key"].(string)
		kj, _ := rows[j]["key"].(string)
		return ki < kj
	})
}
