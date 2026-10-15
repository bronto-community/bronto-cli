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
	// A missing field makes a comparison UNKNOWN, and NOT UNKNOWN is still
	// UNKNOWN (SQL three-valued logic) — verified live: both
	// "nosuchfield != 'x'" and "NOT nosuchfield = 'x'" return no events.
	ev := map[string]any{"present": "x", "nil": nil}
	for q, want := range map[string]bool{
		"missing = 'x'":                         false,
		"missing != 'x'":                        false,
		"NOT missing = 'x'":                     false,
		"missing NOT IN ('x')":                  false,
		"missing NOT LIKE '%x%'":                false,
		"NOT missing = 'x' OR present ~ 'x'":    true, // UNKNOWN OR TRUE
		"NOT (missing = 'x' AND present = 'y')": true, // NOT (UNKNOWN AND FALSE)
		"missing IS NULL":                       true,
		"nil IS NULL":                           true,
		"present IS NULL":                       false,
		"NOT missing IS NULL":                   false,
		"present IS NOT NULL":                   true,
		"EXISTS present":                        true,
		"EXISTS (nil)":                          false,
		"NOT EXISTS missing":                    true, // EXISTS is never UNKNOWN
		"nil = 'x'":                             false,
		"present ~ 'x'":                         true,
		"present > 5":                           false, // a number never matches a non-numeric string
		"present = 5":                           false,
		"present = other":                       false, // value-side attribute missing: UNKNOWN
		"NOT present = other":                   false,
	} {
		if got := mustMatcher(t, q).Match(ev); got != want {
			t.Errorf("%q = %v, want %v", q, got, want)
		}
	}
}

func TestMatcherFieldFallbackPrefix(t *testing.T) {
	ev := map[string]any{"message_kvs.status": "502", "level": "info", "message_kvs.level": "error"}
	for q, want := range map[string]bool{
		"status = 502":      true,  // falls back to message_kvs.status
		"level = 'info'":    true,  // exact key wins over the fallback
		"level = 'error'":   false, // ...so the prefixed value is not consulted
		"missing = 'x'":     false,
		"NOT missing = 'x'": false, // a missing field is NULL, as on the server
		"level != 'error'":  true,  // exact key wins for != too
	} {
		m := mustMatcher(t, q)
		m.SetFieldFallbackPrefix("message_kvs.")
		if got := m.Match(ev); got != want {
			t.Errorf("%q = %v, want %v", q, got, want)
		}
	}
	if mustMatcher(t, "status = 502").Match(ev) {
		t.Error("without a prefix, message_kvs.status must not answer for status")
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

// TestMatcherServerOperators pins local evaluation of the operators the
// server accepts to the results the live API returned for the same
// expressions.
func TestMatcherServerOperators(t *testing.T) {
	raw := `{"level":"error","message":"Upstream slow to respond","status":502,"host":"web-2"}`
	ev := map[string]any{
		"@raw": raw, "level": "error", "message": "Upstream slow to respond",
		"status": json.Number("502"), "host": "web-2", "code": "502",
	}
	for q, want := range map[string]bool{
		// IN / NOT IN
		"status IN (500, 502)":         true,
		"status in (500, 503)":         false,
		"status NOT IN (500, 502)":     false,
		"status NOT IN (500, 503)":     true,
		"status IN ('500', '502')":     true, // numeric strings compare numerically
		"level IN ('warn', 'error')":   true,
		"status IN (500, missing)":     false, // UNKNOWN
		"status IN (502, missing)":     true,
		"NOT status IN (500, missing)": false, // NOT UNKNOWN
		"level IN (level)":             true,  // attribute reference
		// LIKE: anchored, case-sensitive, only % is a wildcard
		"message LIKE '%slow%'":                   true,
		"message LIKE 'Upstream%'":                true,
		"message LIKE 'upstream%'":                false,
		"message LIKE 'Upstream'":                 false,
		"message LIKE '%respond'":                 true,
		"message LIKE '%'":                        true,
		"message LIKE 'Upstream slow to respond'": true,
		"message LIKE 'Upstream slow to respon_'": false, // _ is literal (verified live)
		"host LIKE 'web_2'":                       false,
		"host LIKE 'web-%'":                       true,
		"message NOT LIKE '%slow%'":               false,
		"message LIKE '%.*%'":                     false, // regex metacharacters are literal
		"status LIKE '5%'":                        true,  // numbers match by string form
		// ILIKE
		"message ILIKE '%SLOW%'":     true,
		"message NOT ILIKE '%SLOW%'": false,
		"level ILIKE 'ERR%'":         true,
		// regex family
		"message REGEXP 'sl.w'":     true,
		"message RLIKE '^slow'":     false,
		"message NOT REGEXP 'fail'": true,
		"message ~* 'UPSTREAM'":     true,
		"message ~ 'UPSTREAM'":      false,
		"message !~* 'UPSTREAM'":    false,
		// <> and ordering
		"status <> 200":  true,
		"status <> 502":  false,
		"status > '500'": true, // numeric when both sides convert
		"level > 'a'":    true, // otherwise by string
		"level < 'e'":    false,
		"code = 502":     true,
		// LOWER()
		"LOWER(message) = 'upstream slow to respond'": true,
		"lower(level) IN ('error')":                   true,
		// double-quoted field names
		`"level" = 'error'`: true,
		// barewords and double-quoted values are attribute references
		"level = error":   false,
		`level = "error"`: false,
		"level = level":   true,
		`code = "status"`: true,
		// free text: @raw ILIKE '%term%'
		"slow":                 true,
		"SLOW":                 true,
		`"slow to respond"`:    true,
		"'slow to respond'":    true,
		"timeout":              false,
		"NOT slow":             false,
		"web-2":                true,
		"502":                  true,
		"slow AND NOT timeout": true,
		"'50%'":                true, // % in a term is a wildcard, as on the server
		// precedence: AND binds tighter than OR, NOT looser than comparisons
		"level = 'info' OR level = 'error' AND status = 502":   true,
		"level = 'error' OR level = 'info' AND status = 200":   true,
		"(level = 'error' OR level = 'info') AND status = 200": false,
		"NOT level = 'info' AND status IN (502)":               true,
		"NOT (level = 'error' AND status IN (502))":            false,
		"level = 'error' && status >= 500":                     true,
	} {
		if got := mustMatcher(t, q).Match(ev); got != want {
			t.Errorf("%q = %v, want %v", q, got, want)
		}
	}
}

func TestMatcherFreeTextWithoutRawIsUnknown(t *testing.T) {
	ev := map[string]any{"level": "error"}
	for _, q := range []string{"error", "NOT error"} {
		if mustMatcher(t, q).Match(ev) {
			t.Errorf("%q matched an event without @raw", q)
		}
	}
}

// TestMatcherLargeIntegersStayExact: 2^53+1 and 2^53 round to the same
// float64, but they are different ids.
func TestMatcherLargeIntegersStayExact(t *testing.T) {
	for _, id := range []any{"9007199254740993", json.Number("9007199254740993")} {
		ev := map[string]any{"id": id}
		for q, want := range map[string]bool{
			"id = '9007199254740992'":               false,
			"id = 9007199254740992":                 false,
			"id = '9007199254740993'":               true,
			"id = 9007199254740993":                 true,
			"id != 9007199254740992":                true,
			"id IN (9007199254740992, 1)":           false,
			"id NOT IN ('9007199254740992')":        true,
			"id > 9007199254740992":                 true,
			"id < 9007199254740994":                 true,
			"id >= '9007199254740994'":              false,
			"id = 9007199254740993.0":               true,
			"id = 9007199254740993.000000000000001": false,
		} {
			if got := mustMatcher(t, q).Match(ev); got != want {
				t.Errorf("id=%#v: %q = %v, want %v", id, q, got, want)
			}
		}
	}
}

// countingValue counts how often the matcher reads its string form.
type countingValue struct{ n *int }

func (c countingValue) String() string { *c.n++; return "x" }

// TestMatcherShortCircuits: FALSE AND x and TRUE OR x must not evaluate
// x (typically the expensive regex side).
func TestMatcherShortCircuits(t *testing.T) {
	for q, want := range map[string]int{
		"status = 500 AND msg ~ 'x'":     0,
		"status = 200 OR msg ~ 'x'":      0,
		"status = 200 AND msg ~ 'x'":     1,
		"status = 500 OR msg ~ 'x'":      1,
		"missing = 1 AND msg ~ 'x'":      1, // UNKNOWN AND x still needs x
		"NOT status = 200 AND msg ~ 'x'": 0,
	} {
		n := 0
		mustMatcher(t, q).Match(map[string]any{"status": 200, "msg": countingValue{&n}})
		if n != want {
			t.Errorf("%q evaluated the right side %d times, want %d", q, n, want)
		}
	}
}

func TestMatcherNaNStringsCompareAsText(t *testing.T) {
	ev := map[string]any{"v": "nan"}
	if !mustMatcher(t, "v = 'nan'").Match(ev) {
		t.Fatal("\"nan\" must equal 'nan' as text, not as a NaN float")
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
