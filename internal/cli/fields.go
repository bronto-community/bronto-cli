package cli

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

func newFieldsCmd() *cobra.Command {
	var dataset, since string
	var limit int
	cmd := &cobra.Command{
		Use:   "fields [name-filter]",
		Short: "Discover field names (top keys) in a dataset",
		Long: "Discover field names (top keys) in a dataset, with a sample of the\n" +
			"values seen for each. Pass a name-filter to keep only fields whose\n" +
			"name contains it (case-insensitive) — handy for finding the exact\n" +
			"spelling of a field to use in a search query.",
		Example: "  bronto fields -d <dataset> --since 1h\n" +
			"  bronto fields -d <dataset> model      # fields with \"model\" in the name\n" +
			"  bronto fields --since 15m -n 20",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			spec, err := resolveRelativeSince(since, "Last 1 hour", "fields", "/top-keys")
			if err != nil {
				return err
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
			rows, err := topKeyRows(cmd.Context(), app, params)
			if err != nil {
				return err
			}
			if len(args) > 0 {
				rows = filterKeysByName(rows, args[0])
			}
			// The live /top-keys endpoint ignores its limit param (observed
			// 2026-07-18: -n 3 returned 9 keys), so enforce -n here after
			// the deterministic sort (and after the name filter, so -n caps
			// the matched set rather than the pre-filter set).
			if limit > 0 && len(rows) > limit {
				rows = rows[:limit]
			}
			format, err := app.DetectFormat(false)
			if err != nil {
				return err
			}
			// The values sample is an array on the wire; json/jsonl keep it
			// verbatim, but a table/csv cell needs a compact human string.
			if format == output.FormatTable || format == output.FormatCSV {
				for _, r := range rows {
					if vals, ok := r["values"].([]string); ok {
						r["values"] = displayValues(vals)
					}
				}
			}
			p, err := app.PrinterFor(format)
			if err != nil {
				return err
			}
			return p.PrintRows(fieldsColumns(rows), rows)
		},
	}
	cmd.Flags().StringVarP(&dataset, "dataset", "d", "", "dataset name or UUID (omit for all datasets)")
	cmd.Flags().StringVar(&since, "since", "1h", "relative lookback (single unit: 30s, 15m, 1h, 2d)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "maximum keys to return")
	// The positional name-filter completes to the dataset's own field names
	// (or flag hints when no -d is set yet); --dataset completion is wired
	// tree-wide by applyCompletions.
	cmd.ValidArgsFunction = completeFieldsOrHints
	return cmd
}

// resolveRelativeSince parses --since into a single-unit relative range for
// endpoints that accept only relative ranges (fields' /top-keys, usage's
// /usage): it applies defaultRange when --since is empty and rejects a
// compound --since (which resolves to absolute bounds).
func resolveRelativeSince(since, defaultRange, cmdName, endpoint string) (timerange.Spec, error) {
	spec, err := timerange.Resolve(since, "", "", nil)
	if err != nil {
		return timerange.Spec{}, err
	}
	if spec.IsZero() {
		spec.TimeRange = defaultRange
	}
	if spec.TimeRange == "" { // compound --since resolved to absolute bounds
		return timerange.Spec{}, clierr.New("usage_invalid_since",
			cmdName+" supports only single-unit --since values (e.g. 90m, 2h)").
			WithHint("The " + endpoint + " endpoint accepts relative ranges only.")
	}
	return spec, nil
}

// topKeyRows is the single call site for the /top-keys field-discovery
// endpoint: GET with the given params, normalized into rows. Callers build
// their own params (log_id, time_range, limit) and shape the rows.
func topKeyRows(ctx context.Context, app *App, params url.Values) ([]map[string]any, error) {
	var payload map[string]any
	client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
	if err := client.GetJSON(ctx, "/top-keys", params, &payload); err != nil {
		return nil, err
	}
	return normalizeTopKeys(payload), nil
}

// topKeyNames lists recently-seen field names for one dataset (logID) over
// timeRange — the common "what fields exist" query behind query check,
// search-filter resolution, completion, and ask grounding.
func topKeyNames(ctx context.Context, app *App, logID, timeRange string) ([]string, error) {
	rows, err := topKeyRows(ctx, app, url.Values{"time_range": {timeRange}, "log_id": {logID}})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		if k, ok := r["key"].(string); ok && k != "" {
			names = append(names, k)
		}
	}
	return names, nil
}

func normalizeTopKeys(payload map[string]any) []map[string]any {
	for _, field := range []string{"top_keys", "keys", "data"} {
		if list, ok := payload[field].([]any); ok {
			return toRows(list)
		}
	}

	// Live shape: {"<log-id>": {"<key>": {rank, type, field_type, values}}}
	// (response additionalProperties -> TopKeys -> TopKeysPerLogOrMetric),
	// with per-key metadata one or two map levels down. Ranks are summed
	// when the same key appears under multiple logs; type/field_type carry
	// through (rank is deprecated and often 0 live, so the metadata is the
	// actually-useful part).
	type keyMeta struct {
		count     float64
		typ, src  string
		values    []string
		valueSeen map[string]bool
	}
	agg := map[string]*keyMeta{}
	collect := func(key string, meta map[string]any) {
		km := agg[key]
		if km == nil {
			km = &keyMeta{}
			agg[key] = km
		}
		if n, ok := numericValue(meta["rank"]); ok {
			km.count += n
		}
		if s, ok := meta["type"].(string); ok && km.typ == "" {
			km.typ = strings.ToLower(s)
		}
		if s, ok := meta["field_type"].(string); ok && km.src == "" {
			// MESSAGE_KVP / ATTRIBUTE read poorly in a table.
			km.src = strings.ToLower(strings.TrimSuffix(s, "_KVP"))
		}
		// The live shape carries a SAMPLE of observed values as a map
		// {value: {rank}}; its keys are the values. Dedupe across logs.
		if vm, ok := meta["values"].(map[string]any); ok {
			if km.valueSeen == nil {
				km.valueSeen = map[string]bool{}
			}
			for val := range vm {
				if !km.valueSeen[val] {
					km.valueSeen[val] = true
					km.values = append(km.values, val)
				}
			}
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
	if len(agg) > 0 {
		rows := make([]map[string]any, 0, len(agg))
		for k, km := range agg {
			row := map[string]any{"key": k, "count": km.count}
			if km.typ != "" {
				row["type"] = km.typ
			}
			if km.src != "" {
				row["source"] = km.src
			}
			if len(km.values) > 0 {
				sort.Strings(km.values)
				row["values"] = km.values
			}
			rows = append(rows, row)
		}
		sortKeyCountRows(rows)
		return rows
	}

	// flat {key: numericCount} object
	var rows []map[string]any
	for k, v := range payload {
		if n, ok := numericValue(v); ok {
			rows = append(rows, map[string]any{"key": k, "count": n})
		}
	}
	sortKeyCountRows(rows)
	return rows
}

// fieldsColumns picks the table/csv columns from what the rows actually
// carry: count only when at least one is non-zero (the live API's rank is
// deprecated and usually 0 — a column of zeros is worse than none), and
// type/source when present.
func fieldsColumns(rows []map[string]any) []string {
	cols := []string{"key"}
	hasCount, hasType, hasSource, hasValues := false, false, false, false
	for _, r := range rows {
		if n, ok := numericValue(r["count"]); ok && n > 0 {
			hasCount = true
		}
		if _, ok := r["type"]; ok {
			hasType = true
		}
		if _, ok := r["source"]; ok {
			hasSource = true
		}
		if _, ok := r["values"]; ok {
			hasValues = true
		}
	}
	if hasCount {
		cols = append(cols, "count")
	}
	if hasType {
		cols = append(cols, "type")
	}
	if hasSource {
		cols = append(cols, "source")
	}
	if hasValues {
		cols = append(cols, "values")
	}
	if len(cols) == 1 {
		cols = append(cols, "count") // legacy shapes: keep the old two-column view
	}
	return cols
}

// filterKeysByName keeps rows whose key contains sub (case-insensitive). It
// lets a user narrow a noisy field list to the handful they care about —
// e.g. "bronto fields -d ... model" to find the exact spelling of $model.
func filterKeysByName(rows []map[string]any, sub string) []map[string]any {
	needle := strings.ToLower(sub)
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		if k, ok := r["key"].(string); ok && strings.Contains(strings.ToLower(k), needle) {
			out = append(out, r)
		}
	}
	return out
}

// displayValues renders a sampled value set as one compact table/csv cell:
// a few examples comma-joined, an ellipsis when more were sampled, and a
// legible placeholder for the empty string. The full set stays available in
// json/jsonl output, so this only has to hint at the shape of the data.
func displayValues(vals []string) string {
	const show = 5
	if len(vals) == 0 {
		return ""
	}
	shown := vals
	more := false
	if len(shown) > show {
		shown = shown[:show]
		more = true
	}
	parts := make([]string, len(shown))
	for i, v := range shown {
		if v == "" {
			parts[i] = "(empty)"
		} else {
			parts[i] = v
		}
	}
	out := strings.Join(parts, ", ")
	if more {
		out += ", …"
	}
	return out
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
