package cli

import (
	"fmt"
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
		if hb, _ := meta["last_heartbeat_at"].(float64); hb > 0 {
			row["last_activity"] = timeAgo(hb, now)
		}
	}
	return rows
}
