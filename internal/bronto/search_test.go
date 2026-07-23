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

func TestEventColumnsByFrequencyRanking(t *testing.T) {
	rows := []map[string]any{
		{"@time": "t", "@status": "info", "@raw": "r", "common": 1, "rare": 1},
		{"@time": "t", "@status": "info", "@raw": "r", "common": 2},
		{"@time": "t", "@status": "info", "@raw": "r", "common": 3, "other": 1},
	}
	got := EventColumnsByFrequency(rows, 8)
	want := []string{"@time", "@status", "common", "other", "rare"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestEventColumnsByFrequencyTiesAlphabetical(t *testing.T) {
	rows := []map[string]any{
		{"@time": "t", "beta": 1, "alpha": 1},
	}
	got := EventColumnsByFrequency(rows, 8)
	// only 2 discovered keys: @raw absent anyway, alpha before beta.
	want := []string{"@time", "alpha", "beta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestEventColumnsByFrequencyRawLastWhenFewKeys(t *testing.T) {
	rows := []map[string]any{
		{"@time": "t", "@raw": "r", "level": "info", "host": "a"},
	}
	got := EventColumnsByFrequency(rows, 8)
	// 2 promoted keys < threshold: @raw retained, at the END.
	want := []string{"@time", "host", "level", "@raw"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestEventColumnsByFrequencyDropsRawAtThreshold(t *testing.T) {
	rows := []map[string]any{
		{"@time": "t", "@raw": "r", "a": 1, "b": 1, "c": 1},
	}
	got := EventColumnsByFrequency(rows, 8)
	want := []string{"@time", "a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestEventColumnsByFrequencyCap(t *testing.T) {
	rows := []map[string]any{
		{"@time": "t", "@status": "s", "@raw": "r",
			"a": 1, "b": 1, "c": 1, "d": 1, "e": 1, "f": 1, "g": 1},
	}
	got := EventColumnsByFrequency(rows, 8)
	want := []string{"@time", "@status", "a", "b", "c", "d", "e", "f"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestEventColumnsByFrequencyTightCapKeepsRaw(t *testing.T) {
	rows := []map[string]any{
		{"@time": "t", "@status": "s", "@raw": "r", "a": 1, "b": 1, "c": 1},
	}
	// cap 5 leaves room for only 2 promoted keys (< threshold), so @raw
	// stays and occupies the final slot.
	got := EventColumnsByFrequency(rows, 5)
	want := []string{"@time", "@status", "a", "b", "@raw"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestEventColumnsByFrequencyEmptyAndUncapped(t *testing.T) {
	if got := EventColumnsByFrequency(nil, 8); len(got) != 0 {
		t.Fatalf("empty rows: %v", got)
	}
	rows := []map[string]any{{"@raw": "r", "x": 1}}
	got := EventColumnsByFrequency(rows, 0)
	want := []string{"x", "@raw"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uncapped got %v want %v", got, want)
	}
}

func TestEventColumnsByFrequencyDistinctTiebreakAndNullDrop(t *testing.T) {
	rows := []map[string]any{
		{"@time": "t", "varied": "a", "constant": "x", "allnull": nil},
		{"@time": "t", "varied": "b", "constant": "x", "allnull": "null"},
	}
	got := EventColumnsByFrequency(rows, 8)
	// varied (2 distinct) beats constant (1 distinct) despite equal
	// presence; allnull is never promoted.
	want := []string{"@time", "varied", "constant"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestEventColumnsByFrequencySkipsTimestampDuplicates(t *testing.T) {
	rows := []map[string]any{
		{"@time": "t", "message_kvs.timestamp": "2026-01-01T00:00:00Z", "@timestamp": "x", "level": "info"},
	}
	got := EventColumnsByFrequency(rows, 8)
	want := []string{"@time", "level"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestEventColumnsByFrequencyNullPaddingIgnored(t *testing.T) {
	rows := []map[string]any{
		{"@time": "t", "common": "a", "padded": nil},
		{"@time": "t", "common": "b", "padded": nil},
		{"@time": "t", "common": "c", "padded": "real"},
		{"@time": "t", "common": "d", "rare": "x"},
	}
	got := EventColumnsByFrequency(rows, 8)
	// common: 4 non-null; padded and rare: 1 each, tie → alphabetical.
	want := []string{"@time", "common", "padded", "rare"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
