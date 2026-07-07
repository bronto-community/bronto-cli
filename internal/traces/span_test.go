package traces

import "testing"

func TestRowToSpanCoercionAndBackfills(t *testing.T) {
	row := map[string]any{
		"$span.trace_id": "t1", "$span.span_id": "s1", "$span.parent_span_id": nil,
		"$span.name": "GET /x", "$span.kind": "SPAN_KIND_SERVER",
		"$span.duration_nano": "0", "$span.start_time_unix_nano": float64(100),
		"$span.end_time_unix_nano": "250.0",
		"$span.status_code":        "STATUS_CODE_ERROR", "$service.name": "cart",
	}
	s := RowToSpan(row)
	if s.TraceID != "t1" || s.ParentSpanID != "" || s.Kind != "SERVER" || s.Service != "cart" {
		t.Fatalf("basic fields: %+v", s)
	}
	if s.DurationNS != 150 { // backfill: end-start when duration==0
		t.Fatalf("duration backfill = %d, want 150", s.DurationNS)
	}
	if !s.IsError() {
		t.Fatal("STATUS_CODE_ERROR must be an error")
	}

	// end backfill: end==0, start+duration known
	row2 := map[string]any{"$span.start_time_unix_nano": float64(100),
		"$span.duration_nano": float64(50), "$span.end_time_unix_nano": float64(0),
		"$span.status_code": "STATUS_CODE_UNSET"}
	s2 := RowToSpan(row2)
	if s2.EndNS != 150 {
		t.Fatalf("end backfill = %d, want 150", s2.EndNS)
	}
	if s2.IsError() {
		t.Fatal("UNSET is not an error")
	}
}

func TestFormatDurationNS(t *testing.T) {
	cases := []struct {
		ns   int64
		want string
	}{
		{0, "—"}, {-5, "—"},
		{500, "0.5µs"},
		{999_999, "1000.0µs"},
		{1_000_000, "1.00ms"},
		{9_820_000, "9.82ms"},
		{999_000_000, "999.00ms"},
		{1_000_000_000, "1.00s"},
		{83_500_000_000, "83.50s"},
	}
	for _, c := range cases {
		if got := FormatDurationNS(c.ns); got != c.want {
			t.Errorf("FormatDurationNS(%d) = %q, want %q", c.ns, got, c.want)
		}
	}
}
