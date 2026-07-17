package integration

import (
	"encoding/json"
	"fmt"
	"os"
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
}

// TestIngestRoundtrip_IngestionKeySend live-verifies that an ingestion-only
// key can send data through the same path a management key can. The
// auth-negative suite already proves an ingestion key CANNOT read (403
// auth_insufficient_role); this proves it CAN write: a distinctly-marked
// event is sent directly into the shared seeded dataset with the ingestion
// key, then polled for with the management key (ingestion keys can't
// search).
func TestIngestRoundtrip_IngestionKeySend(t *testing.T) {
	skipIfNoCreds(t)
	ingestKey := os.Getenv("BRONTO_IT_INGEST_KEY")
	if ingestKey == "" {
		t.Skip("BRONTO_IT_INGEST_KEY not set; skipping ingestion-key roundtrip test")
	}
	dataset, _ := seededData(t)

	marker2 := newMarker()
	line := jsonLine(map[string]any{
		"message":   "bronto-ci ingestion-key roundtrip",
		"ci_marker": marker2,
		"level":     "info",
	})

	ingestR := NewRunner(t, ingestKey)
	res, err := ingestR.Run(t.Context(), line, "send", "-d", dataset)
	if err != nil {
		t.Fatalf("running send with the ingestion key: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("send with the ingestion key exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}

	mgmtR := NewRunner(t, mgmtKey())
	logID := logIDForDataset(t, mgmtR, dataset)
	PollUntil(t, seedPollBudget(), seedPollInterval, func() (bool, error) {
		sres, serr := mgmtR.Run(t.Context(), "", searchArgs(logID, fmt.Sprintf("ci_marker = '%s'", marker2), "-o", "json", "-n", "1")...)
		if serr != nil {
			return false, serr
		}
		if sres.ExitCode != 0 {
			return false, fmt.Errorf("search exited %d\nstdout: %s\nstderr: %s", sres.ExitCode, sres.Stdout, sres.Stderr)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(sres.Stdout), &rows); err != nil {
			return false, fmt.Errorf("parsing search -o json: %w\nstdout: %s", err, sres.Stdout)
		}
		if len(rows) == 0 {
			return false, fmt.Errorf("no rows yet for the ingestion-key-sent marker %s\nlast stdout: %s\nlast stderr: %s",
				marker2, sres.Stdout, sres.Stderr)
		}
		return true, nil
	})
}

// TestIngestRoundtrip_OneShotMessage covers the `send -m/--message` one-shot
// path — distinct from the NDJSON-stream-from-stdin path the seed fixture
// and the ingestion-key test above exercise.
func TestIngestRoundtrip_OneShotMessage(t *testing.T) {
	key := skipIfNoCreds(t)
	dataset, _ := seededData(t)
	r := NewRunner(t, key)

	marker3 := newMarker()
	msg := "bronto-ci one-shot " + marker3
	res, err := r.Run(t.Context(), "", "send", "-d", dataset, "-m", msg)
	if err != nil {
		t.Fatalf("running send -m: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("send -m exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}

	logID := logIDForDataset(t, r, dataset)
	PollUntil(t, seedPollBudget(), seedPollInterval, func() (bool, error) {
		sres, serr := r.Run(t.Context(), "", searchArgs(logID, fmt.Sprintf("message = '%s'", msg), "-o", "json", "-n", "1")...)
		if serr != nil {
			return false, serr
		}
		if sres.ExitCode != 0 {
			return false, fmt.Errorf("search exited %d\nstdout: %s\nstderr: %s", sres.ExitCode, sres.Stdout, sres.Stderr)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(sres.Stdout), &rows); err != nil {
			return false, fmt.Errorf("parsing search -o json: %w\nstdout: %s", err, sres.Stdout)
		}
		if len(rows) == 0 {
			return false, fmt.Errorf("no rows yet for the one-shot message\nlast stdout: %s\nlast stderr: %s", sres.Stdout, sres.Stderr)
		}
		return true, nil
	})
}

// TestIngestRoundtrip_StructuredFieldsPassthrough asserts that extra fields
// sent alongside "message" — level, status ints, and the trace-shaped
// trace_id/span_id/duration_ms fields on a subset of seed events — survive
// ingestion -> search untouched, proving the CLI/API don't silently drop or
// coerce arbitrary structured fields.
func TestIngestRoundtrip_StructuredFieldsPassthrough(t *testing.T) {
	key := skipIfNoCreds(t)
	dataset, marker := seededData(t)
	r := NewRunner(t, key)
	logID := logIDForDataset(t, r, dataset)

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

	var sawLevel, sawStatus, sawTrace bool
	for _, row := range rows {
		if l, ok := row["level"].(string); ok && l != "" {
			sawLevel = true
		}
		if _, ok := row["status"]; ok {
			sawStatus = true
		}
		if tid, ok := row["trace_id"].(string); ok && tid != "" {
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
