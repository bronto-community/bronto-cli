package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// timeAgo renders an epoch-milliseconds timestamp as a coarse relative age
// ("3m ago"); empty string for zero/negative input. Ages beyond ~90 days
// fall back to the date — "412d ago" reads worse than "2026-03-01".
func timeAgo(ms float64, now time.Time) string {
	if ms <= 0 {
		return ""
	}
	t := time.UnixMilli(int64(ms))
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 90*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.UTC().Format("2006-01-02")
	}
}

// humanBytes renders a byte count with a binary-ish 1000 step and one
// decimal ("21.7 kB", "3.2 GB"); plain integer below 1000.
func humanBytes(b float64) string {
	if b < 1000 {
		return fmt.Sprintf("%.0f B", b)
	}
	units := []string{"kB", "MB", "GB", "TB", "PB"}
	v := b
	for _, u := range units {
		v /= 1000
		if v < 1000 {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f EB", v/1000)
}

// datasetListRows derives the human columns for `bronto datasets list`:
// last_activity from metadata.last_heartbeat_at (epoch ms).
func datasetListRows(rows []map[string]any) []map[string]any {
	now := time.Now()
	for _, row := range rows {
		meta, _ := row["metadata"].(map[string]any)
		if hb, ok := numericValue(meta["last_heartbeat_at"]); ok && hb > 0 {
			row["last_activity"] = timeAgo(hb, now)
		}
	}
	return rows
}

// numericValue coerces decoded JSON numbers — json.Number after
// bronto.DecodeJSON, float64 from plain unmarshals in tests — to float64.
func numericValue(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f, true
		}
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	}
	return 0, false
}

// collectionListRows expands /collections rows — maps of collection name
// to dataset arrays — into one row per collection with a dataset count
// and joined names, which is what a human scanning the table wants.
func collectionListRows(rows []map[string]any) []map[string]any {
	var out []map[string]any
	for _, row := range rows {
		names := make([]string, 0, len(row))
		for k := range row {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, collection := range names {
			datasets, ok := row[collection].([]any)
			if !ok {
				// Not the map-of-arrays shape: keep the row untouched.
				out = append(out, row)
				break
			}
			dsNames := make([]string, 0, len(datasets))
			for _, d := range datasets {
				if m, ok := d.(map[string]any); ok {
					if n, _ := m["dataset"].(string); n != "" {
						dsNames = append(dsNames, n)
					}
				}
			}
			sort.Strings(dsNames)
			out = append(out, map[string]any{
				"collection": collection,
				"datasets":   len(datasets),
				"names":      strings.Join(dsNames, ", "),
			})
		}
	}
	return out
}
