package cli

import (
	"testing"
	"time"
)

func TestTimeAgo(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) float64 { return float64(now.Add(-d).UnixMilli()) }
	cases := []struct {
		in   float64
		want string
	}{
		{0, ""},
		{-5, ""},
		{ms(10 * time.Second), "just now"},
		{ms(3 * time.Minute), "3m ago"},
		{ms(6 * time.Hour), "6h ago"},
		{ms(48 * time.Hour), "2d ago"},
		{ms(200 * 24 * time.Hour), "2025-12-29"},
	}
	for _, c := range cases {
		if got := timeAgo(c.in, now); got != c.want {
			t.Errorf("timeAgo(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{21667, "21.7 kB"},
		{6288, "6.3 kB"},
		{3_200_000_000, "3.2 GB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDatasetListRowsDerivesLastActivity(t *testing.T) {
	rows := datasetListRows([]map[string]any{
		{"dataset": "a", "metadata": map[string]any{"last_heartbeat_at": float64(time.Now().Add(-2 * time.Hour).UnixMilli())}},
		{"dataset": "b"}, // no metadata: no derived column, no panic
	})
	if got, _ := rows[0]["last_activity"].(string); got != "2h ago" {
		t.Errorf("last_activity = %q, want 2h ago", got)
	}
	if _, ok := rows[1]["last_activity"]; ok {
		t.Error("row without metadata must not gain last_activity")
	}
}

func TestUsageSeriesRows(t *testing.T) {
	queries := []map[string]any{{
		"name": "ingestion_org_usage",
		"key":  "bytes_total",
		"series": []any{
			map[string]any{"@time": "Fri Jul 10 18:53:00 UTC 2026", "count": "6", "value": "21667.0"},
			map[string]any{"@time": "Sat Jul 11 18:53:00 UTC 2026", "count": "0", "value": "0.0"},
		},
	}}
	cols, rows := usageSeriesRows(queries)
	if len(cols) != 3 || cols[0] != "time" || cols[1] != "events" || cols[2] != "volume" {
		t.Fatalf("cols = %v", cols)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0]["time"] != "2026-07-10 18:53" || rows[0]["events"] != 6.0 || rows[0]["volume"] != "21.7 kB" {
		t.Fatalf("row0 = %v", rows[0])
	}

	// Two named queries -> metric column prepended.
	multi := append(queries, map[string]any{
		"name":   "search_usage",
		"key":    "events_total",
		"series": []any{map[string]any{"@time": "bogus-format", "count": "1", "value": "2"}},
	})
	cols, rows = usageSeriesRows(multi)
	if cols[0] != "metric" || len(rows) != 3 {
		t.Fatalf("multi cols=%v rows=%d", cols, len(rows))
	}
	if rows[2]["metric"] != "search_usage" || rows[2]["time"] != "bogus-format" || rows[2]["volume"] != 2.0 {
		t.Fatalf("multi row = %v", rows[2])
	}
}

func TestFieldsColumnsAdaptive(t *testing.T) {
	// Live shape with dead rank: no count column, type/source shown.
	cols := fieldsColumns([]map[string]any{{"key": "a", "count": 0.0, "type": "string", "source": "message"}})
	if len(cols) != 3 || cols[0] != "key" || cols[1] != "type" || cols[2] != "source" {
		t.Fatalf("cols = %v", cols)
	}
	// Non-zero counts keep the count column.
	cols = fieldsColumns([]map[string]any{{"key": "a", "count": 5.0}})
	if len(cols) != 2 || cols[1] != "count" {
		t.Fatalf("cols = %v", cols)
	}
	// Nothing useful at all: legacy two-column view.
	cols = fieldsColumns([]map[string]any{{"key": "a", "count": 0.0}})
	if len(cols) != 2 || cols[1] != "count" {
		t.Fatalf("cols = %v", cols)
	}
}
