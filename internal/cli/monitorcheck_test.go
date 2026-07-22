package cli

import (
	"strings"
	"testing"
)

func TestLintMonitor(t *testing.T) {
	known := map[string]bool{"11111111-1111-1111-1111-111111111111": true}
	good := map[string]any{
		"name": "m", "comparison_operator": "ABOVE", "threshold": 1.0,
		"window":  "Last 10 minutes",
		"actions": []any{map[string]any{"type": "EMAIL", "email": "a@b.c"}},
		"queries": []any{map[string]any{
			"name": "q", "select": []any{"count(*)"}, "where": "status >= 500",
			"from": []any{"11111111-1111-1111-1111-111111111111"},
		}},
	}
	if probs := lintMonitor(good, known, true); len(probs) != 0 {
		t.Fatalf("good monitor flagged: %v", probs)
	}

	bad := map[string]any{
		"comparison_operator": "NOPE",
		"window":              "Last 2 minutes",
		"actions":             []any{map[string]any{"type": "EMAIL"}},
		"queries": []any{map[string]any{
			"select": []any{}, "where": "status >",
			"from": []any{"99999999-9999-9999-9999-999999999999"},
		}},
	}
	probs := lintMonitor(bad, known, true)
	joined := strings.Join(probs, "\n")
	for _, want := range []string{
		"missing required field \"name\"",
		"missing required field \"threshold\"",
		"comparison_operator \"NOPE\"",
		"below the 5-minute minimum",
		"EMAIL action needs an email",
		"missing name",
		"select must be a non-empty array",
		"expected a value",
		"not found in this account",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems missing %q:\n%s", want, joined)
		}
	}

	// offline: dataset checks skipped, everything else still lints
	offline := lintMonitor(bad, nil, false)
	if strings.Contains(strings.Join(offline, "\n"), "not found in this account") {
		t.Fatal("dataset checks must be skipped when the account is unreachable")
	}
}

func TestLintMonitorWindowBounds(t *testing.T) {
	if p := lintMonitorWindow("Last 1 days"); len(p) != 0 {
		t.Fatalf("1 day must pass: %v", p)
	}
	if p := lintMonitorWindow("Last 2 days"); len(p) == 0 {
		t.Fatal("2 days must exceed the max")
	}
	if p := lintMonitorWindow("10 minutes"); len(p) == 0 {
		t.Fatal("missing 'Last' prefix must fail")
	}
}
