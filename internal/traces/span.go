// Package traces implements the trace explorer: span model, aggregations,
// and the waterfall/shape algorithms over Bronto's .traces logset.
// Field literals and formulas follow v1 exactly (see
// .design/specs/2026-07-07-v1-traces-extraction.md).
package traces

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/coerce"
)

const FromExpr = "logset = '.traces'"

var SpanFields = []string{
	"Span:trace_id", "Span:span_id", "Span:parent_span_id",
	"Span:name", "Span:kind", "Span:duration_nano",
	"Span:start_time_unix_nano", "Span:end_time_unix_nano",
	"Span:status_code", "Resource:service.name", "Resource:service.namespace",
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
		TraceID:      str(row, "Span:trace_id"),
		SpanID:       str(row, "Span:span_id"),
		ParentSpanID: str(row, "Span:parent_span_id"),
		Name:         str(row, "Span:name"),
		Kind:         strings.TrimPrefix(str(row, "Span:kind"), "SPAN_KIND_"),
		Service:      str(row, "Resource:service.name"),
		Status:       str(row, "Span:status_code"),
		StartNS:      toInt64(row["Span:start_time_unix_nano"]),
		EndNS:        toInt64(row["Span:end_time_unix_nano"]),
		DurationNS:   toInt64(row["Span:duration_nano"]),
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
	// json.Number carries full int64 precision (span ids, epoch nanos);
	// take that path before coerce reduces through float64.
	if n, ok := v.(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	f, _ := coerce.NumberOrParse(v)
	return int64(f)
}

// toFloat tolerantly coerces API numbers that may arrive as float64,
// int64, int, or numeric strings. Unparseable values become 0 (v1 semantics).
func toFloat(v any) float64 {
	f, _ := coerce.NumberOrParse(v)
	return f
}
