package bronto

import (
	"reflect"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/timerange"
)

func TestBodyOmitsZeroValues(t *testing.T) {
	b := SearchRequest{FromExpr: "log_id = 'x'", Where: "status >= 500", Limit: 100,
		Time: timerange.Spec{TimeRange: "Last 15 minutes"}}.Body()
	want := map[string]any{
		"from_expr": "log_id = 'x'", "where": "status >= 500",
		"limit": 100, "time_range": "Last 15 minutes",
	}
	if !reflect.DeepEqual(b, want) {
		t.Fatalf("got %v want %v", b, want)
	}
}

func TestBodyAbsoluteTimeAndFlags(t *testing.T) {
	mrf := false
	b := SearchRequest{From: []string{"id1"}, Time: timerange.Spec{FromTs: 1, ToTs: 2},
		Select: []string{"count()"}, Groups: []string{"host"}, Slices: 60,
		MostRecentFirst: &mrf, OrderBy: "x DESC", ExplainOnly: true}.Body()
	for k, want := range map[string]any{
		"from_ts": int64(1), "to_ts": int64(2), "num_of_slices": 60,
		"most_recent_first": false, "order_by": "x DESC", "explain_only": true,
	} {
		if got := b[k]; !reflect.DeepEqual(got, want) {
			t.Errorf("body[%q] = %v (%T), want %v", k, got, got, want)
		}
	}
	if _, has := b["time_range"]; has {
		t.Error("time_range must be absent with from_ts/to_ts")
	}
}

func TestEventRowsPrefersEvents(t *testing.T) {
	r := &SearchResponse{Events: []map[string]any{{"a": 1}}, Result: []map[string]any{{"b": 2}}}
	if r.EventRows()[0]["a"] != 1 {
		t.Fatal("must prefer events")
	}
	r2 := &SearchResponse{Result: []map[string]any{{"b": 2}}}
	if r2.EventRows()[0]["b"] != 2 {
		t.Fatal("must fall back to result")
	}
}

func TestFlatten(t *testing.T) {
	got := Flatten(map[string]any{"a": map[string]any{"b": 1, "c": map[string]any{"d": "x"}}, "e": 2})
	want := map[string]any{"a.b": 1, "a.c.d": "x", "e": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestEventColumnsPriorityAndCap(t *testing.T) {
	rows := []map[string]any{
		{"zebra": 1, "@raw": "x", "@time": "t"},
		{"alpha": 2, "@status": "info"},
	}
	got := EventColumns(rows, 0)
	if got[0] != "@time" || got[1] != "@status" || got[2] != "@raw" {
		t.Fatalf("priority order wrong: %v", got)
	}
	capped := EventColumns(rows, 4)
	if len(capped) != 4 {
		t.Fatalf("cap: %v", capped)
	}
}

func TestGroupRowsMergesGroupObject(t *testing.T) {
	r := &SearchResponse{Groups: []map[string]any{
		{"group": map[string]any{"host": "web-1"}, "count": float64(3)},
		{"group": "plain", "value": float64(1)},
	}}
	rows := r.GroupRows()
	if rows[0]["host"] != "web-1" || rows[0]["count"] != float64(3) {
		t.Fatalf("row0 = %v", rows[0])
	}
	if rows[1]["group"] != "plain" {
		t.Fatalf("row1 = %v", rows[1])
	}
}

// TestSelectedRowsVsEventRows pins the live /search contract discovered
// 2026-07-20: every response populates BOTH events (raw) and result (the
// select projection). Projection consumers must use SelectedRows —
// EventRows silently ignores the select.
func TestSelectedRowsVsEventRows(t *testing.T) {
	body := `{"events":[{"@raw":"raw","attributes":{}}],"result":[{"$span.trace_id":"t1"}]}`
	var resp SearchResponse
	if err := DecodeJSON([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.EventRows()[0]["@raw"]; !ok {
		t.Fatal("EventRows must prefer the raw events")
	}
	if resp.SelectedRows()[0]["$span.trace_id"] != "t1" {
		t.Fatal("SelectedRows must prefer the projection")
	}
	// fallbacks when only one side is populated
	var evOnly SearchResponse
	_ = DecodeJSON([]byte(`{"events":[{"a":1}]}`), &evOnly)
	if len(evOnly.SelectedRows()) != 1 {
		t.Fatal("SelectedRows must fall back to events")
	}
}

// TestGroupRowsBracketedString pins the live group shape: a bracketed
// string, not a map.
func TestGroupRowsBracketedString(t *testing.T) {
	var resp SearchResponse
	_ = DecodeJSON([]byte(`{"groups":[{"group":"[events-helper]","value":3009.0,"count":3009}]}`), &resp)
	rows := resp.GroupRows()
	if rows[0]["group"] != "events-helper" {
		t.Fatalf("group = %v, want brackets stripped", rows[0]["group"])
	}
}
