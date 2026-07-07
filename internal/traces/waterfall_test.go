package traces

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func testSpans() []Span {
	return []Span{
		{TraceID: "t", SpanID: "root", Name: "POST /add", Service: "cart",
			Kind: "SERVER", StartNS: 0, DurationNS: 100, EndNS: 100},
		{TraceID: "t", SpanID: "c1", ParentSpanID: "root", Name: "HGET", Service: "cart",
			Kind: "CLIENT", StartNS: 10, DurationNS: 30, EndNS: 40},
		{TraceID: "t", SpanID: "c2", ParentSpanID: "root", Name: "HMSET", Service: "cart",
			Kind: "CLIENT", StartNS: 50, DurationNS: 40, EndNS: 90,
			Status: "STATUS_CODE_ERROR"},
		{TraceID: "t", SpanID: "orphan", ParentSpanID: "gone", Name: "stray", Service: "web",
			StartNS: 5, DurationNS: 10, EndNS: 15},
	}
}

func TestBuildTreeRootsAndOrphans(t *testing.T) {
	roots, children := BuildTree(testSpans())
	if len(roots) != 2 { // true root + orphan (parent "gone" not in batch)
		t.Fatalf("roots = %d: %v", len(roots), roots)
	}
	if roots[0].SpanID != "root" { // sorted by StartNS: 0 < 5
		t.Fatalf("first root = %s", roots[0].SpanID)
	}
	kids := children["root"]
	if len(kids) != 2 || kids[0].SpanID != "c1" { // sorted by StartNS
		t.Fatalf("children = %v", kids)
	}
}

func TestBuildTreeFallbackEarliest(t *testing.T) {
	// self-referencing cycle: no span's parent is missing, so no roots
	spans := []Span{
		{SpanID: "a", ParentSpanID: "b", StartNS: 20},
		{SpanID: "b", ParentSpanID: "a", StartNS: 10},
	}
	roots, _ := BuildTree(spans)
	if len(roots) != 1 || roots[0].SpanID != "b" {
		t.Fatalf("fallback root = %v", roots)
	}
}

func TestTraceBounds(t *testing.T) {
	start, end, total := TraceBounds(testSpans())
	// testSpans has positive StartNS: 10, 50, 5; min positive = 5 (orphan)
	// max EndNS = 100 (root); maxDur = 100; so total = max(95, 100) = 100
	if start != 5 || end != 100 || total != 100 {
		t.Fatalf("bounds = %d..%d total %d", start, end, total)
	}
}

func TestRenderBarClamped(t *testing.T) {
	// span occupying the second half of a width-10 axis
	s := Span{StartNS: 50, DurationNS: 50}
	bar := RenderBar(s, 0, 100, 10, false)
	if bar != "·····█████" {
		t.Fatalf("bar = %q", bar)
	}
	// pathological offset beyond axis must not overflow width
	far := Span{StartNS: 1000, DurationNS: 50}
	bar2 := RenderBar(far, 0, 100, 10, false)
	if len([]rune(bar2)) != 10 {
		t.Fatalf("overflow: %q (%d runes)", bar2, len([]rune(bar2)))
	}
}

func TestRenderWaterfallStructure(t *testing.T) {
	var buf bytes.Buffer
	RenderWaterfall(&buf, testSpans(), 20, false)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.Contains(lines[0], "4 span(s) across 2 service(s)") {
		t.Fatalf("header = %q", lines[0])
	}
	// DFS order: root, its children (c1, c2), then orphan root
	if !strings.Contains(lines[1], "cart/POST /add") {
		t.Fatalf("line1 = %q", lines[1])
	}
	if !strings.HasPrefix(strings.TrimLeft(lines[2], " "), "cart/HGET") ||
		!strings.HasPrefix(lines[2], "  ") {
		t.Fatalf("child not indented: %q", lines[2])
	}
	if !strings.Contains(lines[3], "ERROR") {
		t.Fatalf("error span unmarked: %q", lines[3])
	}
	if !strings.Contains(lines[4], "web/stray") {
		t.Fatalf("orphan missing: %q", lines[4])
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatal("color=false must not emit ANSI codes")
	}
}

func TestWaterfallRowsDepthAndOrder(t *testing.T) {
	rows := WaterfallRows(testSpans())
	if len(rows) != 4 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0]["depth"] != 0 || rows[1]["depth"] != 1 || rows[3]["depth"] != 0 {
		t.Fatalf("depths: %v %v %v", rows[0]["depth"], rows[1]["depth"], rows[3]["depth"])
	}
	if rows[2]["error"] != true {
		t.Fatalf("error flag: %v", rows[2])
	}
}

func TestWaterfallTerminatesOnCycles(t *testing.T) {
	cyclic := []Span{
		{SpanID: "a", ParentSpanID: "b", StartNS: 20, DurationNS: 5, Name: "a", Service: "s"},
		{SpanID: "b", ParentSpanID: "a", StartNS: 10, DurationNS: 5, Name: "b", Service: "s"},
	}
	done := make(chan []map[string]any, 1)
	go func() { done <- WaterfallRows(cyclic) }()
	select {
	case rows := <-done:
		if len(rows) != 2 {
			t.Fatalf("rows = %d, want 2 (each span once)", len(rows))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaterfallRows hangs on cyclic input")
	}
	// self-referencing single span
	self := []Span{{SpanID: "x", ParentSpanID: "x", StartNS: 1, DurationNS: 1}}
	done2 := make(chan []map[string]any, 1)
	go func() { done2 <- WaterfallRows(self) }()
	select {
	case rows := <-done2:
		if len(rows) != 1 {
			t.Fatalf("self-ref rows = %d", len(rows))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaterfallRows hangs on self-referencing span")
	}
}

func TestTraceBoundsIgnoresNonPositiveStarts(t *testing.T) {
	spans := []Span{
		{SpanID: "a", StartNS: -50, DurationNS: 10, EndNS: 0},
		{SpanID: "b", StartNS: 100, DurationNS: 20, EndNS: 120},
	}
	start, end, total := TraceBounds(spans)
	if start != 100 || end != 120 || total < 20 {
		t.Fatalf("bounds = %d..%d total %d", start, end, total)
	}
}
