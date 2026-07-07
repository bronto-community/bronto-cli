package traces

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	hopSep  = "\x1f"
	pathSep = "\x1e"
)

type ShapeBucket struct {
	Identity  string
	Parent    string
	Service   string
	Name      string
	Offsets   []int64
	Durations []int64
	TraceIDs  map[string]bool
	Errors    int
}

func (b *ShapeBucket) add(traceID string, offset, duration int64, isErr bool) {
	if offset < 0 {
		offset = 0
	}
	if duration < 0 {
		duration = 0
	}
	b.Offsets = append(b.Offsets, offset)
	b.Durations = append(b.Durations, duration)
	b.TraceIDs[traceID] = true
	if isErr {
		b.Errors++
	}
}

func (b *ShapeBucket) NSamples() int { return len(b.Offsets) }
func (b *ShapeBucket) NTraces() int  { return len(b.TraceIDs) }
func (b *ShapeBucket) Depth() int {
	if b.Identity == "" {
		return 0
	}
	return strings.Count(b.Identity, pathSep)
}

func (b *ShapeBucket) AvgOffset() int64   { return mean(b.Offsets) }
func (b *ShapeBucket) AvgDuration() int64 { return mean(b.Durations) }

func (b *ShapeBucket) MinOffset() int64 {
	if len(b.Offsets) == 0 {
		return 0
	}
	m := b.Offsets[0]
	for _, o := range b.Offsets[1:] {
		if o < m {
			m = o
		}
	}
	return m
}

// MaxEnd is the latest (offset+duration) pair seen — NOT max(offset)+max(dur).
func (b *ShapeBucket) MaxEnd() int64 {
	var m int64
	for i := range b.Offsets {
		if e := b.Offsets[i] + b.Durations[i]; e > m {
			m = e
		}
	}
	return m
}

func mean(vals []int64) int64 {
	if len(vals) == 0 {
		return 0
	}
	var sum int64
	for _, v := range vals {
		sum += v
	}
	return sum / int64(len(vals))
}

type EntryMatch struct {
	EntryOnly     bool
	Service       string
	Operation     string
	ErrorsOnly    bool
	MinDurationMS float64
}

func (m EntryMatch) Active() bool {
	return m.EntryOnly || m.Service != "" || m.Operation != "" || m.ErrorsOnly || m.MinDurationMS > 0
}

func (m EntryMatch) Matches(s Span) bool {
	if m.EntryOnly && s.Kind != "SERVER" {
		return false
	}
	if m.Service != "" && s.Service != m.Service {
		return false
	}
	if m.Operation != "" && s.Name != m.Operation {
		return false
	}
	if m.ErrorsOnly && !s.IsError() {
		return false
	}
	if m.MinDurationMS > 0 && float64(s.DurationNS) < m.MinDurationMS*1e6 {
		return false
	}
	return true
}

// ComputeShape groups sampled spans by trace, re-roots each trace at its
// anchor (earliest matching span, else client-side root, else earliest),
// and merges identical (service,name) paths across traces into buckets.
// Identity is computed top-down during traversal — no parent-walk, no
// span mutation (v1's mutate/restore eliminated; results identical).
func ComputeShape(spans []Span, match EntryMatch) (map[string]*ShapeBucket, int) {
	byTrace := map[string][]Span{}
	for _, s := range spans {
		byTrace[s.TraceID] = append(byTrace[s.TraceID], s)
	}
	buckets := map[string]*ShapeBucket{}
	tracesUsed := 0
	traceIDs := make([]string, 0, len(byTrace))
	for id := range byTrace {
		traceIDs = append(traceIDs, id)
	}
	sort.Strings(traceIDs) // deterministic iteration
	for _, traceID := range traceIDs {
		tspans := byTrace[traceID]
		anchor, ok := pickAnchor(tspans, match)
		if !ok {
			continue
		}
		tracesUsed++
		children := map[string][]Span{}
		for _, s := range tspans {
			children[s.ParentSpanID] = append(children[s.ParentSpanID], s)
		}
		t0 := anchor.StartNS
		type node struct {
			span Span
			path string
		}
		seen := map[string]bool{}
		stack := []node{{anchor, hop(anchor)}}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if n.span.SpanID != "" {
				if seen[n.span.SpanID] {
					continue
				}
				seen[n.span.SpanID] = true
			}
			b := buckets[n.path]
			if b == nil {
				parent := ""
				if i := strings.LastIndex(n.path, pathSep); i >= 0 {
					parent = n.path[:i]
				}
				b = &ShapeBucket{Identity: n.path, Parent: parent,
					Service: n.span.Service, Name: n.span.Name,
					TraceIDs: map[string]bool{}}
				buckets[n.path] = b
			}
			offset := int64(0)
			if n.span.StartNS != 0 {
				offset = n.span.StartNS - t0
			}
			b.add(traceID, offset, n.span.DurationNS, n.span.IsError())
			for _, kid := range children[n.span.SpanID] {
				if kid.SpanID == n.span.SpanID {
					continue // self-reference guard
				}
				stack = append(stack, node{kid, n.path + pathSep + hop(kid)})
			}
		}
	}
	return buckets, tracesUsed
}

func hop(s Span) string { return s.Service + hopSep + s.Name }

func pickAnchor(tspans []Span, match EntryMatch) (Span, bool) {
	if len(tspans) == 0 {
		return Span{}, false
	}
	if match.Active() {
		var best Span
		found := false
		for _, s := range tspans {
			if match.Matches(s) && (!found || s.StartNS < best.StartNS) {
				best, found = s, true
			}
		}
		if found {
			return best, true
		}
	}
	byID := map[string]bool{}
	for _, s := range tspans {
		if s.SpanID != "" {
			byID[s.SpanID] = true
		}
	}
	var root Span
	found := false
	for _, s := range tspans {
		if !byID[s.ParentSpanID] && (!found || s.StartNS < root.StartNS) {
			root, found = s, true
		}
	}
	if found {
		return root, true
	}
	earliest := tspans[0]
	for _, s := range tspans[1:] {
		if s.StartNS < earliest.StartNS {
			earliest = s
		}
	}
	return earliest, true
}

func FilterMinTraces(buckets map[string]*ShapeBucket, min int) map[string]*ShapeBucket {
	out := map[string]*ShapeBucket{}
	for k, b := range buckets {
		if b.NTraces() >= min {
			out[k] = b
		}
	}
	return out
}

func ShapeRows(buckets map[string]*ShapeBucket) []map[string]any {
	list := sortedBuckets(buckets, func(a, b *ShapeBucket) bool {
		if a.Depth() != b.Depth() {
			return a.Depth() < b.Depth()
		}
		return a.AvgOffset() < b.AvgOffset()
	})
	rows := make([]map[string]any, 0, len(list))
	for _, b := range list {
		var parent any
		if b.Parent != "" {
			lastHop := b.Parent
			if i := strings.LastIndex(lastHop, pathSep); i >= 0 {
				lastHop = lastHop[i+1:]
			}
			parent = strings.ReplaceAll(lastHop, hopSep, "/")
		}
		rows = append(rows, map[string]any{
			"service": b.Service, "name": b.Name, "depth": b.Depth(),
			"parent": parent, "samples": b.NSamples(), "traces": b.NTraces(),
			"avg_offset_ns": b.AvgOffset(), "avg_duration_ns": b.AvgDuration(),
			"min_offset_ns": b.MinOffset(), "max_end_ns": b.MaxEnd(),
			"errors": b.Errors,
		})
	}
	return rows
}

func sortedBuckets(buckets map[string]*ShapeBucket, less func(a, b *ShapeBucket) bool) []*ShapeBucket {
	list := make([]*ShapeBucket, 0, len(buckets))
	for _, b := range buckets {
		list = append(list, b)
	}
	sort.SliceStable(list, func(i, j int) bool { return less(list[i], list[j]) })
	return list
}

// RenderShapeBar: █ = average position/duration (priority), ▒ = min/max
// spread band, · = outside. Band clamped into [0,width]; avg segment
// truncated by the loop bound (extraction §4.8).
func RenderShapeBar(b *ShapeBucket, axisEnd int64, width int, color bool) string {
	span := axisEnd
	if span < 1 {
		span = 1
	}
	avgLeft := int(b.AvgOffset() * int64(width) / span)
	avgLen := int(b.AvgDuration() * int64(width) / span)
	if avgLen < 1 {
		avgLen = 1
	}
	avgRight := avgLeft + avgLen

	minOff := b.MinOffset()
	if minOff < 0 {
		minOff = 0
	}
	maxEnd := b.MaxEnd()
	if maxEnd < minOff+1 {
		maxEnd = minOff + 1
	}
	bandLeft := int(minOff * int64(width) / span)
	bandRight := int(maxEnd * int64(width) / span)
	if bandRight < bandLeft+1 {
		bandRight = bandLeft + 1
	}
	if bandLeft > width-1 {
		bandLeft = width - 1
	}
	if bandRight > width {
		bandRight = width
	}

	var sb strings.Builder
	for i := 0; i < width; i++ {
		switch {
		case i >= avgLeft && i < avgRight:
			if color {
				c := ansiGreen
				if b.Errors > 0 {
					c = ansiRed
				}
				sb.WriteString(c + "█" + ansiReset)
			} else {
				sb.WriteString("█")
			}
		case i >= bandLeft && i < bandRight:
			if color {
				sb.WriteString(ansiDim + ansiCyan + "▒" + ansiReset)
			} else {
				sb.WriteString("▒")
			}
		default:
			if color {
				sb.WriteString(ansiDim + "·" + ansiReset)
			} else {
				sb.WriteString("·")
			}
		}
	}
	return sb.String()
}

func RenderShape(w io.Writer, buckets map[string]*ShapeBucket, tracesUsed, totalSpans, width int, color bool) {
	var axisEnd int64 = 1
	for _, b := range buckets {
		if e := b.AvgOffset() + b.AvgDuration(); e > axisEnd {
			axisEnd = e
		}
		if e := b.MaxEnd(); e > axisEnd {
			axisEnd = e
		}
	}
	_, _ = fmt.Fprintf(w, "%d trace(s), %d span(s) · axis 0 → %s\n",
		tracesUsed, totalSpans, FormatDurationNS(axisEnd))
	_, _ = fmt.Fprintln(w, "legend: █ avg position · ▒ min/max spread · · before/after")

	children := map[string][]*ShapeBucket{}
	for _, b := range buckets {
		children[b.Parent] = append(children[b.Parent], b)
	}
	for k := range children {
		kids := children[k]
		sort.SliceStable(kids, func(i, j int) bool {
			if kids[i].AvgOffset() != kids[j].AvgOffset() {
				return kids[i].AvgOffset() < kids[j].AvgOffset()
			}
			return kids[i].AvgDuration() > kids[j].AvgDuration()
		})
	}
	roots := children[""]
	if len(roots) == 0 && len(buckets) > 0 {
		minDepth := -1
		for _, b := range buckets {
			if minDepth == -1 || b.Depth() < minDepth {
				minDepth = b.Depth()
			}
		}
		for _, b := range buckets {
			if b.Depth() == minDepth {
				roots = append(roots, b)
			}
		}
		sort.SliceStable(roots, func(i, j int) bool { return roots[i].AvgOffset() < roots[j].AvgOffset() })
	}

	maxLabel := 0
	for _, b := range buckets {
		if l := utf8.RuneCountInString(b.Service) + utf8.RuneCountInString(b.Name) + 1 + 2*b.Depth(); l > maxLabel {
			maxLabel = l
		}
	}
	nameCol := maxLabel + 2
	if nameCol > 60 {
		nameCol = 60
	}
	if nameCol < 16 {
		nameCol = 16
	}
	anyErrors := false
	for _, b := range buckets {
		if b.Errors > 0 {
			anyErrors = true
		}
	}

	type sframe struct {
		b     *ShapeBucket
		depth int
	}
	stack := make([]sframe, 0, len(buckets))
	for i := len(roots) - 1; i >= 0; i-- {
		stack = append(stack, sframe{roots[i], 0})
	}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		b := f.b
		label := truncateTo(strings.Repeat("  ", f.depth)+b.Service+"/"+b.Name, nameCol)
		presence := fmt.Sprintf("%d", b.NTraces())
		if b.NTraces() < tracesUsed {
			presence = fmt.Sprintf("%d/%d", b.NTraces(), tracesUsed)
		}
		errCell := ""
		if anyErrors && b.Errors > 0 {
			errCell = fmt.Sprintf(" %d", b.Errors)
		}
		// %-*s pads by byte width; padTo pads by rune width so multi-byte
		// labels still line up in the name column.
		_, _ = fmt.Fprintf(w, "%s %s %9s %9s %7s%s\n", padTo(label, nameCol),
			RenderShapeBar(b, axisEnd, width, color),
			FormatDurationNS(b.AvgDuration()), FormatDurationNS(b.AvgOffset()),
			presence, errCell)
		kids := children[b.Identity]
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, sframe{kids[i], f.depth + 1})
		}
	}
}
