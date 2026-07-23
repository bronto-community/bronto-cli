package query

import (
	"encoding/json"
	"testing"
)

func mustMatcher(t *testing.T, q string) *Matcher {
	t.Helper()
	n, err := Parse(q)
	if err != nil {
		t.Fatalf("parse %q: %v", q, err)
	}
	m, err := NewMatcher(n)
	if err != nil {
		t.Fatalf("matcher %q: %v", q, err)
	}
	return m
}

func TestMatcherComparisons(t *testing.T) {
	ev := map[string]any{
		"status": json.Number("502"), "level": "error",
		"duration_ms": 120.5, "path": "/api/v1/checkout",
	}
	for q, want := range map[string]bool{
		"status >= 500":                       true,
		"status < 500":                        false,
		"status = 502":                        true,
		"status != 502":                       false,
		"level = 'error'":                     true,
		"level != 'error'":                    false,
		"level = 'info'":                      false,
		"duration_ms > 100 AND status >= 500": true,
		"level = 'info' OR status = 502":      true,
		"NOT level = 'info'":                  true,
		"NOT status >= 500":                   false,
		"path ~ 'checkout$'":                  true,
		"path !~ 'checkout$'":                 false,
		"path ~ '^/admin'":                    false,
		"(level = 'info' OR level = 'error') AND status >= 500": true,
	} {
		if got := mustMatcher(t, q).Match(ev); got != want {
			t.Errorf("%q = %v, want %v", q, got, want)
		}
	}
}

func TestMatcherAbsentAndNullFields(t *testing.T) {
	ev := map[string]any{"present": "x", "nil": nil}
	for q, want := range map[string]bool{
		"missing = 'x'":     false,
		"missing != 'x'":    false, // absent fails every comparison
		"NOT missing = 'x'": true,
		"nil = 'x'":         false,
		"present ~ 'x'":     true,
		"present > 5":       false, // non-numeric value fails ordered compare
	} {
		if got := mustMatcher(t, q).Match(ev); got != want {
			t.Errorf("%q = %v, want %v", q, got, want)
		}
	}
}

func TestMatcherStringlyNumbers(t *testing.T) {
	// numbers arriving as strings (common in parsed logs) compare numerically
	ev := map[string]any{"status": "502"}
	if !mustMatcher(t, "status >= 500").Match(ev) {
		t.Fatal("stringly number must compare numerically")
	}
	if !mustMatcher(t, "status = 502").Match(ev) {
		t.Fatal("stringly number must equal numerically")
	}
}

func TestNewMatcherRejectsBadRegex(t *testing.T) {
	n, err := Parse("path ~ '['")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewMatcher(n); err == nil {
		t.Fatal("invalid regex must fail at construction")
	}
}
