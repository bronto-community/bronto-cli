package traces

import (
	"bytes"
	"strings"
	"testing"
)

// two traces with identical shape: entry -> db call; one trace has an extra retry span
func shapeSpans() []Span {
	return []Span{
		// trace 1
		{TraceID: "t1", SpanID: "a1", Name: "POST /add", Service: "cart", Kind: "SERVER",
			StartNS: 1000, DurationNS: 100, EndNS: 1100},
		{TraceID: "t1", SpanID: "b1", ParentSpanID: "a1", Name: "HGET", Service: "cart",
			Kind: "CLIENT", StartNS: 1010, DurationNS: 30, EndNS: 1040},
		// trace 2
		{TraceID: "t2", SpanID: "a2", Name: "POST /add", Service: "cart", Kind: "SERVER",
			StartNS: 5000, DurationNS: 120, EndNS: 5120},
		{TraceID: "t2", SpanID: "b2", ParentSpanID: "a2", Name: "HGET", Service: "cart",
			Kind: "CLIENT", StartNS: 5020, DurationNS: 40, EndNS: 5060,
			Status: "STATUS_CODE_ERROR"},
		{TraceID: "t2", SpanID: "c2", ParentSpanID: "a2", Name: "HGET", Service: "cart",
			Kind: "CLIENT", StartNS: 5070, DurationNS: 20, EndNS: 5090},
	}
}

func TestComputeShapeBucketsAndStats(t *testing.T) {
	buckets, used := ComputeShape(shapeSpans(), EntryMatch{EntryOnly: true})
	if used != 2 {
		t.Fatalf("tracesUsed = %d", used)
	}
	if len(buckets) != 2 { // entry bucket + cart/HGET bucket (same path in both traces)
		t.Fatalf("buckets = %d: %v", len(buckets), keysOf(buckets))
	}
	var entry, hget *ShapeBucket
	for _, b := range buckets {
		switch b.Name {
		case "POST /add":
			entry = b
		case "HGET":
			hget = b
		}
	}
	if entry == nil || hget == nil {
		t.Fatal("missing buckets")
	}
	if entry.Parent != "" || entry.Depth() != 0 {
		t.Fatalf("entry parentage: %q depth %d", entry.Parent, entry.Depth())
	}
	if hget.NSamples() != 3 || hget.NTraces() != 2 { // 1 + 2 occurrences, 2 traces
		t.Fatalf("hget samples=%d traces=%d", hget.NSamples(), hget.NTraces())
	}
	if hget.Errors != 1 {
		t.Fatalf("errors = %d", hget.Errors)
	}
	// offsets relative to each trace's anchor: 10, 20, 70 -> avg 33
	if hget.AvgOffset() != 33 {
		t.Fatalf("avg offset = %d", hget.AvgOffset())
	}
	if hget.MinOffset() != 10 {
		t.Fatalf("min offset = %d", hget.MinOffset())
	}
	if hget.MaxEnd() != 90 { // max(10+30, 20+40, 70+20)
		t.Fatalf("max end = %d", hget.MaxEnd())
	}
}

func keysOf(m map[string]*ShapeBucket) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestComputeShapeAnchorFallbackToRoot(t *testing.T) {
	// no SERVER span; match inactive -> root anchor
	spans := []Span{
		{TraceID: "t", SpanID: "r", Name: "job", Service: "worker", Kind: "INTERNAL",
			StartNS: 100, DurationNS: 50},
		{TraceID: "t", SpanID: "k", ParentSpanID: "r", Name: "step", Service: "worker",
			Kind: "INTERNAL", StartNS: 110, DurationNS: 10},
	}
	buckets, used := ComputeShape(spans, EntryMatch{})
	if used != 1 || len(buckets) != 2 {
		t.Fatalf("used=%d buckets=%d", used, len(buckets))
	}
}

func TestFilterMinTraces(t *testing.T) {
	buckets, _ := ComputeShape(shapeSpans(), EntryMatch{EntryOnly: true})
	if got := FilterMinTraces(buckets, 2); len(got) != 2 {
		t.Fatalf("min=2: %d", len(got))
	}
	if got := FilterMinTraces(buckets, 3); len(got) != 0 {
		t.Fatalf("min=3: %d", len(got))
	}
}

func TestShapeRowsSortedByDepthThenOffset(t *testing.T) {
	buckets, _ := ComputeShape(shapeSpans(), EntryMatch{EntryOnly: true})
	rows := ShapeRows(buckets)
	if rows[0]["depth"] != 0 || rows[1]["depth"] != 1 {
		t.Fatalf("order: %v", rows)
	}
	if rows[1]["traces"] != 2 || rows[1]["samples"] != 3 {
		t.Fatalf("row1 = %v", rows[1])
	}
}

func TestRenderShapeBarGlyphs(t *testing.T) {
	b := &ShapeBucket{Offsets: []int64{20, 40}, Durations: []int64{20, 20},
		TraceIDs: map[string]bool{"t1": true, "t2": true}}
	// axis 100, width 10: avg offset 30 -> cell 3; avg dur 20 -> len 2
	// band: min 20 -> cell 2; maxEnd 60 -> cell 6
	bar := RenderShapeBar(b, 100, 10, false)
	if bar != "··▒██▒····" {
		t.Fatalf("bar = %q", bar)
	}
}

func TestRenderShapeTable(t *testing.T) {
	buckets, used := ComputeShape(shapeSpans(), EntryMatch{EntryOnly: true})
	var buf bytes.Buffer
	RenderShape(&buf, buckets, used, 5, 20, false)
	out := buf.String()
	if !strings.Contains(out, "2 trace(s), 5 span(s)") {
		t.Fatalf("header: %q", out)
	}
	if !strings.Contains(out, "legend:") {
		t.Fatal("legend missing")
	}
	if !strings.Contains(out, "cart/POST /add") || !strings.Contains(out, "  cart/HGET") {
		t.Fatalf("tree labels: %q", out)
	}
	// hget appears in both traces -> presence shown as bare "2"; entry likewise
	if strings.Contains(out, "2/2") {
		t.Fatalf("full presence must be bare count: %q", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatal("color=false must not emit ANSI")
	}
}
