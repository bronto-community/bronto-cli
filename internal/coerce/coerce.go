// Package coerce converts loosely-typed JSON-decoded values to numbers.
// The Bronto API returns numbers as float64, json.Number (under UseNumber),
// or occasionally int/int64, and several packages independently reduced
// them to a float64 — this is the single source of that logic.
package coerce

import (
	"encoding/json"
	"strconv"
)

// Number coerces a JSON-decoded numeric value (float64, json.Number, int64,
// int) to a float64. ok is false for any other type. A string is NOT
// parsed — callers that want that use NumberOrParse.
func Number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f, true
		}
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}

// NumberOrParse is Number plus a string fast path (strconv.ParseFloat), for
// wire shapes that carry numbers as strings.
func NumberOrParse(v any) (float64, bool) {
	if f, ok := Number(v); ok {
		return f, true
	}
	if s, ok := v.(string); ok {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}
