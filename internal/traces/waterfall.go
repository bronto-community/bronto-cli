package traces

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
	ansiCyan  = "\x1b[36m"
)

// BuildTree indexes spans into a parent->children map and finds roots.
// A span is a root when its parent is absent from THIS batch — which
// includes true roots and orphans whose parent fell outside the query
// window (extraction §5.1).
func BuildTree(spans []Span) ([]Span, map[string][]Span) {
	byID := make(map[string]bool, len(spans))
	for _, s := range spans {
		if s.SpanID != "" {
			byID[s.SpanID] = true
		}
	}
	children := map[string][]Span{}
	for _, s := range spans {
		children[s.ParentSpanID] = append(children[s.ParentSpanID], s)
	}
	for k := range children {
		kids := children[k]
		sort.SliceStable(kids, func(i, j int) bool {
			if kids[i].StartNS != kids[j].StartNS {
				return kids[i].StartNS < kids[j].StartNS
			}
			return kids[i].DurationNS < kids[j].DurationNS
		})
	}
	var roots []Span
	for _, s := range spans {
		if !byID[s.ParentSpanID] {
			roots = append(roots, s)
		}
	}
	if len(roots) == 0 && len(spans) > 0 {
		earliest := spans[0]
		for _, s := range spans[1:] {
			if s.StartNS < earliest.StartNS {
				earliest = s
			}
		}
		roots = []Span{earliest}
	}
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].StartNS < roots[j].StartNS })
	return roots, children
}

func TraceBounds(spans []Span) (start, end, total int64) {
	var maxDur int64 = 1
	if len(spans) == 0 {
		return 0, 0, 0
	}
	for _, s := range spans {
		if s.StartNS > 0 && (start == 0 || s.StartNS < start) {
			start = s.StartNS
		}
		if s.EndNS > end {
			end = s.EndNS
		}
		if s.DurationNS > maxDur {
			maxDur = s.DurationNS
		}
	}
	if end < start {
		end = start
	}
	total = end - start
	if total < maxDur {
		total = maxDur
	}
	return start, end, total
}

// RenderBar draws a span's position on the trace axis. Unlike v1, bars are
// clamped into [0,width] so pathological offsets cannot overflow the column.
func RenderBar(s Span, traceStart, total int64, width int, color bool) string {
	if total < 1 {
		total = 1
	}
	offset := s.StartNS - traceStart
	if offset < 0 || s.StartNS == 0 {
		offset = 0
	}
	length := s.DurationNS
	if length < 1 {
		length = 1
	}
	leftPad := int(offset * int64(width) / total)
	if leftPad > width-1 {
		leftPad = width - 1
	}
	barLen := int(length * int64(width) / total)
	if barLen < 1 {
		barLen = 1
	}
	if leftPad+barLen > width {
		barLen = width - leftPad
	}
	rightPad := width - leftPad - barLen
	dots := func(n int) string { return strings.Repeat("·", n) }
	bar := strings.Repeat("█", barLen)
	if !color {
		return dots(leftPad) + bar + dots(rightPad)
	}
	c := ansiGreen
	if s.IsError() {
		c = ansiRed
	}
	return ansiDim + dots(leftPad) + ansiReset + c + bar + ansiReset + ansiDim + dots(rightPad) + ansiReset
}

type frame struct {
	span  Span
	depth int
}

func dfsOrder(spans []Span) []frame {
	roots, children := BuildTree(spans)
	var out []frame
	visited := map[string]bool{}
	stack := make([]frame, 0, len(spans))
	for i := len(roots) - 1; i >= 0; i-- {
		stack = append(stack, frame{roots[i], 0})
	}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if f.span.SpanID != "" {
			if visited[f.span.SpanID] {
				continue
			}
			visited[f.span.SpanID] = true
		}
		out = append(out, f)
		if f.span.SpanID == "" {
			continue // children map keyed "" holds roots, not children
		}
		kids := children[f.span.SpanID]
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, frame{kids[i], f.depth + 1})
		}
	}
	return out
}

// padTo right-pads s with spaces to the given rune width (not byte
// length), so multi-byte runes don't throw off column alignment.
func padTo(s string, width int) string {
	n := width - utf8.RuneCountInString(s)
	if n <= 0 {
		return s
	}
	return s + strings.Repeat(" ", n)
}

// truncateTo shortens s to width runes, appending "…" when truncated.
// Rune-aware so multi-byte characters are never split mid-encoding.
func truncateTo(s string, width int) string {
	if utf8.RuneCountInString(s) <= width || width <= 0 {
		return s
	}
	r := []rune(s)
	return string(r[:width-1]) + "…"
}

func RenderWaterfall(w io.Writer, spans []Span, width int, color bool) {
	services := map[string]bool{}
	maxLabel := 0
	for _, s := range spans {
		services[s.Service] = true
		if l := utf8.RuneCountInString(s.Service) + utf8.RuneCountInString(s.Name) + 1; l > maxLabel {
			maxLabel = l
		}
	}
	nameCol := maxLabel + 4
	if nameCol > 70 {
		nameCol = 70
	}
	start, _, total := TraceBounds(spans)
	_, _ = fmt.Fprintf(w, "%d span(s) across %d service(s), total %s\n",
		len(spans), len(services), FormatDurationNS(total))

	for _, f := range dfsOrder(spans) {
		indent := strings.Repeat("  ", f.depth)
		labelPlain := f.span.Service + "/" + f.span.Name
		full := indent + labelPlain
		padded := padTo(full, nameCol)
		if utf8.RuneCountInString(padded) == utf8.RuneCountInString(full) {
			padded += " " // always keep at least one gap before the bar
		}
		bar := RenderBar(f.span, start, total, width, color)
		status := ""
		switch {
		case f.span.IsError():
			if color {
				status = " " + ansiRed + "ERROR" + ansiReset
			} else {
				status = " ERROR"
			}
		case f.span.Status != "" && !strings.HasSuffix(strings.ToUpper(f.span.Status), "UNSET"):
			st := strings.TrimPrefix(f.span.Status, "STATUS_CODE_")
			if color {
				status = " " + ansiDim + st + ansiReset
			} else {
				status = " " + st
			}
		}
		_, _ = fmt.Fprintf(w, "%s%s %s%s\n", padded, bar, FormatDurationNS(f.span.DurationNS), status)
	}
}

// WaterfallRows is the machine form of the waterfall: DFS order with depth.
func WaterfallRows(spans []Span) []map[string]any {
	rows := make([]map[string]any, 0, len(spans))
	for _, f := range dfsOrder(spans) {
		s := f.span
		rows = append(rows, map[string]any{
			"depth": f.depth, "service": s.Service, "operation": s.Name,
			"trace_id": s.TraceID, "span_id": s.SpanID, "parent_span_id": s.ParentSpanID,
			"start_ns": s.StartNS, "duration_ns": s.DurationNS,
			"duration": FormatDurationNS(s.DurationNS),
			"status":   strings.TrimPrefix(s.Status, "STATUS_CODE_"),
			"error":    s.IsError(),
		})
	}
	return rows
}
