// Package traces implements the trace explorer: span model, aggregations,
// and the waterfall/shape algorithms over Bronto's .traces logset.
// Field literals and formulas follow v1 exactly (see
// docs/superpowers/specs/2026-07-07-v1-traces-extraction.md).
package traces

import (
	"fmt"
	"strconv"
	"strings"
)

const FromExpr = "logset = '.traces'"

var SpanFields = []string{
	"$span.trace_id", "$span.span_id", "$span.parent_span_id",
	"$span.name", "$span.kind", "$span.duration_nano",
	"$span.start_time_unix_nano", "$span.end_time_unix_nano",
	"$span.status_code", "$service.name", "$service.namespace",
}

type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	Kind         string // SPAN_KIND_ prefix stripped: SERVER, CLIENT, ...
	Service      string
	Status       string // raw: STATUS_CODE_ERROR etc.
	StartNS      int64
	EndNS        int64
	DurationNS   int64
}

func RowToSpan(row map[string]any) Span {
	s := Span{
		TraceID:      str(row, "$span.trace_id"),
		SpanID:       str(row, "$span.span_id"),
		ParentSpanID: str(row, "$span.parent_span_id"),
		Name:         str(row, "$span.name"),
		Kind:         strings.TrimPrefix(str(row, "$span.kind"), "SPAN_KIND_"),
		Service:      str(row, "$service.name"),
		Status:       str(row, "$span.status_code"),
		StartNS:      toInt64(row["$span.start_time_unix_nano"]),
		EndNS:        toInt64(row["$span.end_time_unix_nano"]),
		DurationNS:   toInt64(row["$span.duration_nano"]),
	}
	if s.DurationNS == 0 && s.EndNS > s.StartNS && s.StartNS > 0 {
		s.DurationNS = s.EndNS - s.StartNS
	}
	if s.EndNS == 0 && s.StartNS > 0 && s.DurationNS > 0 {
		s.EndNS = s.StartNS + s.DurationNS
	}
	return s
}

func (s Span) IsError() bool {
	return strings.HasSuffix(strings.ToUpper(s.Status), "ERROR")
}

// FormatDurationNS renders nanoseconds for humans: µs below 1ms (1 decimal),
// ms below 1s (2 decimals), seconds above (2 decimals), em dash for <=0.
func FormatDurationNS(ns int64) string {
	if ns <= 0 {
		return "—"
	}
	ms := float64(ns) / 1e6
	switch {
	case ms < 1:
		return fmt.Sprintf("%.1fµs", float64(ns)/1e3)
	case ms < 1000:
		return fmt.Sprintf("%.2fms", ms)
	default:
		return fmt.Sprintf("%.2fs", ms/1000)
	}
}

func str(row map[string]any, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// toInt64 tolerantly coerces API numbers that may arrive as float64,
// int, or numeric strings. Unparseable values become 0 (v1 semantics).
func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return int64(f)
		}
	}
	return 0
}
