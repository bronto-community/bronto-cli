package integration

import (
	"encoding/json"
	"testing"
)

// TestTraces_ServicesTolerant asserts clean, well-formed output from
// `traces services` without requiring any actual trace data.
//
// The seed fixture's trace-shaped events (trace_id/span_id/duration_ms
// fields on a few seed rows — see seed_test.go) are sent as ordinary NDJSON
// fields to a conventional log dataset, not as real OpenTelemetry spans:
// Bronto's `.traces` logset (what every `traces` subcommand queries, via
// internal/traces.FromExpr = "logset = '.traces'") is populated by actual
// OTel ingestion, which this harness does not perform. So this run's seed
// data will almost certainly NOT surface here.
//
// The point of this test is narrower and still valuable: prove `traces
// services` behaves cleanly (exit 0, a parseable JSON array) against
// whatever the account's .traces logset actually holds — empty or not —
// rather than asserting on data this harness can't reliably produce.
func TestTraces_ServicesTolerant(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)

	res, err := r.Run(t.Context(), "", "traces", "services", "--since", "1h", "-o", "json")
	if err != nil {
		t.Fatalf("running traces services: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("traces services exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("traces services -o json did not parse as a JSON array: %v\noutput: %s", err, res.Stdout)
	}
	// rows may legitimately be empty (no OTel data ingested by this harness);
	// that's the expected, tolerated outcome — see the comment above.
}
