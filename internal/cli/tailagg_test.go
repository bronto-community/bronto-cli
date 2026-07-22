package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func TestSparkline(t *testing.T) {
	if got := sparkline([]float64{0, 0, 0}); got != "▁▁▁" {
		t.Fatalf("all-zero: %q", got)
	}
	got := sparkline([]float64{0, 4, 8})
	if []rune(got)[2] != '█' {
		t.Fatalf("max must render full block: %q", got)
	}
	if []rune(got)[0] != '▁' {
		t.Fatalf("zero must render lowest block: %q", got)
	}
}

func TestFormatAggValue(t *testing.T) {
	if got := formatAggValue(42); got != "42" {
		t.Fatalf("integral: %q", got)
	}
	if got := formatAggValue(3.14159); got != "3.14" {
		t.Fatalf("fractional: %q", got)
	}
}

func TestAggregateRowsGroupsAndTotals(t *testing.T) {
	resp := &bronto.SearchResponse{Groups: []map[string]any{
		{"group": "[web-1]", "value": json.Number("3")},
		{"group": "[web-2]", "value": json.Number("5")},
	}}
	rows := aggregateRows(resp, []string{"count(*)"}, []string{"host"})
	if len(rows) != 2 || rows[0]["group"] != "web-1" || rows[0]["count(*)"] != 3.0 {
		t.Fatalf("group rows = %+v", rows)
	}

	total := &bronto.SearchResponse{Result: []map[string]any{{"count(*)": json.Number("7")}}}
	rows = aggregateRows(total, []string{"count(*)"}, nil)
	if len(rows) != 1 || rows[0]["group"] != "total" || rows[0]["count(*)"] != 7.0 {
		t.Fatalf("total rows = %+v", rows)
	}
}

func TestRenderAggFrame(t *testing.T) {
	rows := []map[string]any{
		{"group": "web-1", "count(*)": 3.0, "trend": "▁▄█"},
	}
	frame := renderAggFrame(rows, []string{"count(*)"}, []string{"host"}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	for _, want := range []string{"HOST", "COUNT(*)", "TREND", "web-1", "▁▄█", "updated 03:04:05"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q:\n%s", want, frame)
		}
	}
}

func TestTailAggregateJSONLSnapshots(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"groups":[{"group":"[info]","value":41},{"group":"[error]","value":2}]}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"tail", "--no-follow", "-g", "level",
		"-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 snapshot lines, got %d: %q", len(lines), out.String())
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatal(err)
	}
	if row["group"] != "info" || row["count(*)"] != 41.0 {
		t.Fatalf("row = %+v", row)
	}
	for _, k := range []string{"ts", "trend"} {
		if _, ok := row[k]; !ok {
			t.Fatalf("snapshot missing %q: %+v", k, row)
		}
	}
}

func TestTailAggregateHumanFrame(t *testing.T) {
	old := stdoutIsTTY
	stdoutIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"groups":[{"group":"[web]","value":9}]}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"tail", "--no-follow", "-g", "host", "--select", "count(*)",
		"-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"HOST", "COUNT(*)", "TREND", "web", "9", "updated "} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("frame missing %q:\n%s", want, out.String())
		}
	}
	if !strings.Contains(errb.String(), "Live aggregate") {
		t.Fatalf("stderr banner missing: %q", errb.String())
	}
}

func TestTailAggregateRejectsLineFiltersAndFormats(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"tail", "-g", "host", "--include", "x"}, "usage_invalid_flags"},
		{[]string{"tail", "-g", "host", "--highlight", "x"}, "usage_invalid_flags"},
		{[]string{"tail", "-g", "host", "-o", "json"}, "usage_invalid_output_format"},
		{[]string{"tail", "-g", "host", "-o", "csv"}, "usage_invalid_output_format"},
		{[]string{"tail", "-g", "host", "-o", "raw"}, "usage_invalid_output_format"},
	} {
		root := NewRootCmd()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append(tc.args, "-d", "11111111-1111-1111-1111-111111111111",
			"--api-key", "k", "--base-url", "http://127.0.0.1:0"))
		err := root.Execute()
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != tc.code {
			t.Fatalf("%v: want %s, got %v", tc.args, tc.code, err)
		}
	}
}
