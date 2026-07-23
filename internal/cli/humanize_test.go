package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/bronto-community/bronto-cli/internal/output"
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

func datasetListRows2(rows []map[string]any) []map[string]any {
	return datasetListRows(rows, output.FormatTable)
}

func TestDatasetListRowsDerivesLastActivity(t *testing.T) {
	rows := datasetListRows2([]map[string]any{
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
	multi := append(append([]map[string]any{}, queries...), map[string]any{
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

func TestDatasetListRowsCSVUsesAbsoluteTime(t *testing.T) {
	ms := float64(time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).UnixMilli())
	rows := datasetListRows([]map[string]any{
		{"dataset": "a", "metadata": map[string]any{"last_heartbeat_at": ms}},
	}, output.FormatCSV)
	if got, _ := rows[0]["last_activity"].(string); got != "2026-07-01T12:00:00Z" {
		t.Errorf("csv last_activity = %q, want RFC3339", got)
	}
}

func TestCollectionListRows(t *testing.T) {
	rows := collectionListRows([]map[string]any{{
		"prod": []any{
			map[string]any{"dataset": "web", "id": "1"},
			map[string]any{"dataset": "api", "id": "2"},
		},
		".traces": []any{map[string]any{"dataset": "spans", "id": "3"}},
	}}, output.FormatTable)
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0]["collection"] != ".traces" || rows[0]["datasets"] != 1 {
		t.Fatalf("row0 = %v", rows[0])
	}
	if rows[1]["collection"] != "prod" || rows[1]["names"] != "api, web" {
		t.Fatalf("row1 = %v", rows[1])
	}

	// Non-map shape passes through untouched.
	passthrough := collectionListRows([]map[string]any{{"name": "x"}}, output.FormatTable)
	if len(passthrough) != 1 || passthrough[0]["name"] != "x" {
		t.Fatalf("passthrough = %v", passthrough)
	}
}

func TestResourceListPolish(t *testing.T) {
	ms := float64(time.Now().Add(-2 * time.Hour).UnixMilli())
	rows := resourceListPolish([]map[string]any{{
		"invited_at": ms,
		"metadata":   map[string]any{"created_at": ms, "modified_at": ms},
		"name":       "x",
	}}, output.FormatTable)
	if rows[0]["invited_at"] != "2h ago" {
		t.Fatalf("invited_at = %v, want humanized", rows[0]["invited_at"])
	}
	if rows[0]["created"] != "2h ago" || rows[0]["modified"] != "2h ago" {
		t.Fatalf("metadata provenance not derived: %v", rows[0])
	}

	// CSV: absolute timestamps.
	csvRows := resourceListPolish([]map[string]any{{
		"metadata": map[string]any{"created_at": float64(1751364000000)},
	}}, output.FormatCSV)
	if got, _ := csvRows[0]["created"].(string); !strings.HasSuffix(got, "Z") {
		t.Fatalf("csv created = %v, want RFC3339", csvRows[0]["created"])
	}
}

// TestMaskSecretRows pins the format-independent secret masking now applied
// before rendering (moved out of resourceListPolish so it covers json/jsonl
// too). Long values keep a short prefix; short ones reveal nothing.
func TestMaskSecretRows(t *testing.T) {
	rows := []map[string]any{
		{"api_key": "575731cd-7468-426b-a80c-997523507b05", "name": "x"},
		{"key": "short", "name": "y"}, // < 12 runes: reveals nothing
	}
	maskSecretRows(rows, []string{"api_key", "key"})
	if rows[0]["api_key"] != "575731cd…" {
		t.Fatalf("long key mask = %v, want 575731cd…", rows[0]["api_key"])
	}
	if rows[1]["key"] != "…" {
		t.Fatalf("short key mask = %v, want …", rows[1]["key"])
	}
	if rows[0]["name"] != "x" {
		t.Fatalf("non-secret field must be untouched: %v", rows[0]["name"])
	}
}

func TestUserListRows(t *testing.T) {
	secs := float64(time.Now().Add(-30 * time.Minute).Unix())
	rows := userListRows([]map[string]any{{
		"email":       "a@b.c",
		"last_logins": map[string]any{"Password": secs, "SSO": secs - 1000},
	}, {
		"email": "never@logged.in",
	}}, output.FormatTable)
	if rows[0]["last_login"] != "30m ago" {
		t.Fatalf("last_login = %v", rows[0]["last_login"])
	}
	if _, ok := rows[1]["last_login"]; ok {
		t.Fatalf("no logins must not derive a column: %v", rows[1])
	}
}

func TestDashboardListRows(t *testing.T) {
	rows := dashboardListRows([]map[string]any{
		{"widget_ids": []any{"a", "b", "c"}},
		{"name": "no-widgets"},
	}, output.FormatTable)
	if rows[0]["widgets_count"] != 3 {
		t.Fatalf("widgets_count = %v", rows[0]["widgets_count"])
	}
	if _, ok := rows[1]["widgets_count"]; ok {
		t.Fatalf("missing widget_ids must not derive a count: %v", rows[1])
	}
}
