package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestIngestRoundtrip_SeedCoversMgmtKeySend documents (and asserts) that the
// shared seed fixture (seed_test.go) already proves the management-key
// send-then-search round trip end-to-end: seededData only returns once its
// data is confirmed visible to search, so a successful call here IS that
// assertion. No separate management-key send is needed.
func TestIngestRoundtrip_SeedCoversMgmtKeySend(t *testing.T) {
	dataset, marker := seededData(t)
	if dataset == "" || marker == "" {
		t.Fatal("seededData returned an empty dataset/marker after a successful seed")
	}
	if seededLogID(t) == "" {
		t.Fatal("seededData resolved no log_id for the seeded dataset after a successful seed")
	}
}

// TestIngestRoundtrip_IngestionKeySend live-verifies that an ingestion-only
// key can send data through the same path a management key can. The
// auth-negative suite already proves an ingestion key CANNOT read (403
// auth_insufficient_role); this proves it CAN write: the seed fixture sends
// a distinctly-marked event into the shared dataset with the ingestion key
// (sendProbes) and waits for it alongside the seed batch, and this test
// reads it back with the management key (ingestion keys can't search).
func TestIngestRoundtrip_IngestionKeySend(t *testing.T) {
	skipIfNoCreds(t)
	probes := seededProbes(t)
	if probes.ingestSkipped {
		t.Skip("BRONTO_IT_INGEST_KEY not set; skipping ingestion-key roundtrip test")
	}
	if probes.ingestErr != nil {
		t.Fatalf("ingestion-key probe send: %v", probes.ingestErr)
	}

	// The fixture sent this event with the ingestion key and its readiness
	// poll already waited for it to become searchable, so ONE search settles
	// it here — no second poll loop against the same eventual consistency.
	// Reading it back needs the management key: ingestion keys can't search
	// (TestAuthNegative_IngestionKeyOnReadEndpoint pins that).
	_, marker := seededData(t)
	r := NewRunner(t, mgmtKey())
	res := mustExitZero(t, r,
		searchArgs(seededLogID(t), fmt.Sprintf("ci_marker = '%s'", marker), "-o", "json", "-n", "100")...)
	if !strings.Contains(res.Stdout, probes.ingestToken) {
		t.Fatalf("search over the seeded marker does not surface the ingestion-key probe %s, "+
			"though the seed fixture saw it become searchable\nstdout: %s", probes.ingestToken, res.Stdout)
	}
}

// TestIngestRoundtrip_OneShotMessage covers the `send -m/--message` one-shot
// path — distinct from the NDJSON-stream-from-stdin path the seed fixture
// and the ingestion-key test above exercise.
func TestIngestRoundtrip_OneShotMessage(t *testing.T) {
	key := skipIfNoCreds(t)
	probes := seededProbes(t)
	if probes.oneShotErr != nil {
		t.Fatalf("one-shot probe send: %v", probes.oneShotErr)
	}

	// Sent by the fixture (sendProbes) and already waited for by its
	// readiness poll, so this is a single search rather than a poll loop.
	r := NewRunner(t, key)
	rows := mustRunJSONArray(t, r,
		searchArgs(seededLogID(t), fmt.Sprintf("message = '%s'", probes.oneShotMessage), "-o", "json", "-n", "1")...)
	if len(rows) == 0 {
		t.Fatalf("no rows for the one-shot message %q, though the seed fixture saw it become searchable",
			probes.oneShotMessage)
	}
}

// TestIngestRoundtrip_StructuredFieldsPassthrough asserts that extra fields
// sent alongside "message" — level, status ints, and the trace-shaped
// trace_id/span_id/duration_ms fields on a subset of seed events — survive
// ingestion -> search untouched, proving the CLI/API don't silently drop or
// coerce arbitrary structured fields.
func TestIngestRoundtrip_StructuredFieldsPassthrough(t *testing.T) {
	key := skipIfNoCreds(t)
	_, marker := seededData(t)
	r := NewRunner(t, key)
	logID := seededLogID(t)

	res, err := r.Run(t.Context(), "",
		searchArgs(logID, fmt.Sprintf("ci_marker = '%s'", marker),
			"--select", "@time", "--select", "ci_marker", "--select", "level",
			"--select", "status", "--select", "trace_id", "--select", "span_id", "--select", "duration_ms",
			"-o", "json", "-n", "50")...)
	if err != nil {
		t.Fatalf("running search: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("search exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("search -o json did not parse: %v\noutput: %s", err, res.Stdout)
	}
	if len(rows) == 0 {
		t.Fatal("search returned no rows for the seeded marker")
	}

	// Structured fields surface under the message_kvs. namespace on live
	// rows (see eventField in seed_test.go).
	var sawLevel, sawStatus, sawTrace bool
	for _, row := range rows {
		if l, ok := eventField(row, "level").(string); ok && l != "" {
			sawLevel = true
		}
		if v := eventField(row, "status"); v != nil && v != "" {
			sawStatus = true
		}
		if tid, ok := eventField(row, "trace_id").(string); ok && tid != "" {
			sawTrace = true
		}
	}
	if !sawLevel {
		t.Errorf("no seeded row surfaced a non-empty \"level\" field: %+v", rows)
	}
	if !sawStatus {
		t.Errorf("no seeded row surfaced a \"status\" field: %+v", rows)
	}
	if !sawTrace {
		t.Errorf("no seeded row surfaced a non-empty \"trace_id\" field (trace-shaped seed events): %+v", rows)
	}
}
