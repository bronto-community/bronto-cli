package cli

import (
	"strings"
	"testing"
)

func TestHistogramBuckets(t *testing.T) {
	rows := []map[string]any{
		{"@time": "Wed Jul 22 05:42:55 UTC 2026", "@timestamp": "1784698975896", "count(*)": "14868.0"},
		{"@time": "Wed Jul 22 05:48:55 UTC 2026", "@timestamp": "1784699335896", "count(*)": "0.0"},
	}
	b := histogramBuckets(rows)
	if len(b) != 2 || b[0]["count"] != 14868.0 {
		t.Fatalf("buckets = %v", b)
	}
	if b[0]["timestamp"].(int64) != 1784698975896 {
		t.Fatalf("timestamp = %v (stringly-typed wire value must parse)", b[0]["timestamp"])
	}
}

func TestRenderHistogram(t *testing.T) {
	buckets := histogramBuckets([]map[string]any{
		{"@time": "t", "@timestamp": "1784698975896", "count(*)": "100"},
		{"@time": "t", "@timestamp": "1784699335896", "count(*)": "50"},
		{"@time": "t", "@timestamp": "1784699695896", "count(*)": "0"},
	})
	out := renderHistogram(buckets, false)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %q", out)
	}
	full := strings.Count(lines[0], "█")
	half := strings.Count(lines[1], "█")
	if full != 40 || half != 20 {
		t.Fatalf("bar scaling wrong: full=%d half=%d", full, half)
	}
	if strings.Count(lines[2], "█") != 0 {
		t.Fatalf("zero bucket must have no bar: %q", lines[2])
	}
	if !strings.Contains(lines[3], "total: 150 events") || !strings.Contains(lines[3], "bucket: 6m0s") {
		t.Fatalf("summary = %q", lines[3])
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatal("no ANSI without color")
	}
	if !strings.Contains(renderHistogram(buckets, true), "\x1b[36m") {
		t.Fatal("color mode must colorize bars")
	}
}
