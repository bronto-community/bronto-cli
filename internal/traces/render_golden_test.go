package traces

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update regenerates the golden files: go test ./internal/traces/ -update.
var update = flag.Bool("update", false, "update golden files")

// assertGolden compares got against testdata/<name>, or rewrites it under
// -update. This pins the exact multi-line renderer output byte-for-byte, so
// a mutation to bar arithmetic, indentation, or label layout fails here
// rather than slipping past the looser Contains-based structure tests.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s: %v (run: go test ./internal/traces/ -update)", name, err)
	}
	if got != string(want) {
		t.Fatalf("output does not match %s:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// TestRenderWaterfallGolden pins the full RenderWaterfall output. The
// existing structure test only asserts substrings (Contains), so bar-offset
// and indentation mutations survive it; this catches them.
func TestRenderWaterfallGolden(t *testing.T) {
	var buf bytes.Buffer
	RenderWaterfall(&buf, testSpans(), 20, false)
	assertGolden(t, "waterfall.golden", buf.String())
}

// TestRenderBarGolden pins the exact bar string across the offset/width
// arithmetic and every clamp in RenderBar, so a boundary/arithmetic
// mutation (leftPad, barLen, the >width and <1 clamps, the StartNS==0
// reset, the total<1 guard) fails here instead of surviving. width 10,
// traceStart 0, total 100 unless noted.
func TestRenderBarGolden(t *testing.T) {
	cases := []struct {
		name                      string
		start, dur, tStart, total int64
		width                     int
		want                      string
	}{
		{"first-half", 0, 50, 0, 100, 10, "█████·····"},
		{"second-half", 50, 50, 0, 100, 10, "·····█████"},
		{"full-width", 0, 100, 0, 100, 10, "██████████"},
		{"zero-duration-clamps-to-one-cell", 0, 0, 0, 100, 10, "█·········"},
		{"offset-past-axis-clamps-right", 1000, 50, 0, 100, 10, "·········█"},
		{"overrun-truncates-to-width", 50, 60, 0, 100, 10, "·····█████"},
		{"trace-start-offset", 20, 20, 10, 100, 10, "·██·······"},
		{"total-below-one-clamped", 0, 1, 0, 0, 10, "██████████"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RenderBar(Span{StartNS: c.start, DurationNS: c.dur}, c.tStart, c.total, c.width, false)
			if got != c.want {
				t.Fatalf("RenderBar = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRenderShapeGolden pins the full RenderShape output (header, legend,
// tree labels, bars, presence/entry counts) — the outer composition the
// existing table test only spot-checks with Contains.
func TestRenderShapeGolden(t *testing.T) {
	buckets, used := ComputeShape(shapeSpans(), EntryMatch{EntryOnly: true})
	var buf bytes.Buffer
	RenderShape(&buf, buckets, used, 5, 20, false)
	assertGolden(t, "shape.golden", buf.String())
}

// TestRenderShapeBarGolden extends the shape-bar glyph test to the
// full-width and single-cell-avg boundaries, pinning the avg/band/rest
// segmentation math.
func TestRenderShapeBarGolden(t *testing.T) {
	cases := []struct {
		name    string
		b       *ShapeBucket
		axisEnd int64
		width   int
		want    string
	}{
		{
			// avg offset 30 -> cell 3, avg dur 20 -> len 2; band min 20 -> 2, maxEnd 60 -> 6
			name:    "avg-band-rest",
			b:       &ShapeBucket{Offsets: []int64{20, 40}, Durations: []int64{20, 20}},
			axisEnd: 100, width: 10, want: "··▒██▒····",
		},
		{
			// avg from 0 spanning the whole axis: all cells are the avg segment
			name:    "avg-fills-width",
			b:       &ShapeBucket{Offsets: []int64{0}, Durations: []int64{100}},
			axisEnd: 100, width: 10, want: "██████████",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RenderShapeBar(c.b, c.axisEnd, c.width, false)
			if got != c.want {
				t.Fatalf("RenderShapeBar = %q, want %q", got, c.want)
			}
		})
	}
}
