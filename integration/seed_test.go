package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- shared seed fixture ----------------------------------------------------
//
// The data-path suites (ingest_roundtrip_test.go, query_test.go,
// exports_test.go) all need SOME real, marked data sitting in a dataset
// before they can do anything interesting. Rather than each suite seeding
// (and waiting on) its own batch — paying Bronto's ingest-to-search eventual-
// consistency latency N times over — this file seeds exactly ONE batch per
// test binary run (sync.Once, triggered lazily by whichever data-dependent
// test happens to run first) and every suite shares it via seededData.
//
// Why sync.Once and not TestMain (like the sweeper in main_test.go): laziness.
// A run that only exercises smoke/auth-negative/CRUD never pays the seed
// cost at all; TestMain would seed unconditionally for every credentialed
// run whether or not anything needed it.
const (
	seedPollInterval       = 5 * time.Second
	seedPollTimeout        = 3 * time.Minute
	seedPollTimeoutNightly = 10 * time.Minute
)

// seedPollBudget returns the readiness-poll timeout: 3 minutes normally, 10
// minutes when BRONTO_IT_NIGHTLY=1 (the nightly workflow's longer budget,
// per the plan — nightly tolerates more eventual-consistency slack in
// exchange for running unattended against a fuller account).
func seedPollBudget() time.Duration {
	if os.Getenv("BRONTO_IT_NIGHTLY") == "1" {
		return seedPollTimeoutNightly
	}
	return seedPollTimeout
}

type seedState struct {
	dataset string
	marker  string
	err     error
}

var (
	seedOnce     sync.Once
	seedStateVal seedState
)

// seededData seeds ~20 structured NDJSON events (once per test binary, lazily
// on first call) into a run-scoped dataset with a unique ci_marker field,
// blocks until they're visible to search, and returns (dataset name, marker)
// to every caller thereafter.
//
// Skips t cleanly when BRONTO_IT_MGMT_KEY is unset (via skipIfNoCreds).
// Fails t hard — no retries, no flaky markers — if seeding itself fails or
// the readiness poll times out; per the plan, that failure carries the last
// search response for one-click triage (see pollSeedVisible below).
func seededData(t *testing.T) (dataset, marker string) {
	t.Helper()
	key := skipIfNoCreds(t)
	seedOnce.Do(func() {
		seedStateVal = doSeed(key)
	})
	if seedStateVal.err != nil {
		t.Fatalf("seed fixture: %v", seedStateVal.err)
	}
	return seedStateVal.dataset, seedStateVal.marker
}

// doSeed sends the seed batch and waits for it to become searchable. It
// deliberately reports failures via a returned error rather than t.Fatal:
// doSeed runs inside sync.Once.Do, shared across every test that calls
// seededData, and t.Fatal/FailNow calls runtime.Goexit() — which, if it fired
// here, would unwind out of Once.Do before seedStateVal.err was ever
// assigned, leaving every later caller with a bogus zero-value (empty
// dataset/marker, no error) instead of a clear failure. Only seededData's
// own t (whichever test triggered the Once) turns this error into a t.Fatal.
func doSeed(key string) seedState {
	dataset := resourceName("logs")
	marker := newMarker()

	dir, err := os.MkdirTemp("", "bronto-it-seed-")
	if err != nil {
		return seedState{err: fmt.Errorf("creating seed config dir: %w", err)}
	}
	defer func() { _ = os.RemoveAll(dir) }()
	r := newSweepRunner(binPath, key, dir)

	// Budget: the readiness poll's own window, plus headroom for the send
	// itself and the datasets-list lookups the poll performs.
	ctx, cancel := context.WithTimeout(context.Background(), seedPollBudget()+2*time.Minute)
	defer cancel()

	res, err := r.Run(ctx, seedLines(marker), "send", "-d", dataset)
	if err != nil {
		return seedState{err: fmt.Errorf("running seed send: %w", err)}
	}
	if res.ExitCode != 0 {
		return seedState{err: fmt.Errorf("seed send exited %d\nstdout: %s\nstderr: %s",
			res.ExitCode, res.Stdout, res.Stderr)}
	}

	if err := pollSeedVisible(ctx, r, dataset, marker, seedPollBudget(), seedPollInterval); err != nil {
		return seedState{err: err}
	}
	return seedState{dataset: dataset, marker: marker}
}

// pollSeedVisible blocks until the seeded marker is visible to search, or
// timeout elapses. It has two things to wait for in sequence, both subject
// to eventual consistency: the dataset itself appearing in `datasets list`
// (ingestion auto-creates the log/dataset on first event, but the
// management-plane listing may lag slightly behind), and then the marker
// itself appearing in `search`. Both are re-checked on every tick so the
// function makes progress on whichever is still pending.
//
// On timeout, the returned error carries the LAST command's stdout/stderr
// (one-click triage per the plan) — no auto-retry beyond this single
// bounded window, no flaky test markers.
func pollSeedVisible(ctx context.Context, r *Runner, datasetName, marker string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var logID, lastStdout, lastStderr, lastStep string
	for {
		if logID == "" {
			if res, err := r.Run(ctx, "", "datasets", "list", "-o", "json"); err == nil {
				lastStdout, lastStderr, lastStep = res.Stdout, res.Stderr, "datasets list"
				if res.ExitCode == 0 {
					var rows []map[string]any
					if json.Unmarshal([]byte(res.Stdout), &rows) == nil {
						for _, row := range rows {
							if n, _ := row["log"].(string); n == datasetName {
								if id, _ := row["log_id"].(string); id != "" {
									logID = id
								}
								break
							}
						}
					}
				}
			}
		}
		if logID != "" {
			res, err := r.Run(ctx, "", "search", "-d", logID, "--where",
				fmt.Sprintf("ci_marker = '%s'", marker), "--since", "1h", "-o", "json", "-n", "1")
			if err == nil {
				lastStdout, lastStderr, lastStep = res.Stdout, res.Stderr, "search"
				if res.ExitCode == 0 {
					var rows []map[string]any
					if json.Unmarshal([]byte(res.Stdout), &rows) == nil && len(rows) > 0 {
						return nil
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"seed data for dataset %q (marker %s) not visible to search after %s (last step: %s)\n"+
					"last stdout: %s\nlast stderr: %s",
				datasetName, marker, timeout, lastStep, lastStdout, lastStderr)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("seed poll canceled: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// logIDForDataset resolves the log_id (UUID) of a dataset by its name (the
// "log" field in `datasets list -o json`), for use with commands whose
// --dataset/-d flags take an actual UUID rather than a name (search,
// fields, context, exports create --dataset). Reused by query_test.go,
// ingest_roundtrip_test.go, and exports_test.go against the seeded dataset.
func logIDForDataset(t *testing.T, r *Runner, name string) string {
	t.Helper()
	rows := mustRunJSONArray(t, r, "datasets", "list", "-o", "json")
	for _, row := range rows {
		if n, _ := row["log"].(string); n == name {
			if id, _ := row["log_id"].(string); id != "" {
				return id
			}
		}
	}
	t.Fatalf("dataset %q not found in `datasets list` (it should already exist: data was seeded into it)", name)
	return ""
}

// --- seed batch construction -------------------------------------------------

// seedTotalEvents / seedTraceEvents: ~20 events total, the last few
// trace-shaped (trace_id/span_id/duration_ms) for the traces suite, though
// traces_test.go does not expect them to surface under .traces — see its
// tolerance comment.
const (
	seedTotalEvents = 20
	seedTraceEvents = 3
)

var seedLevels = []string{"debug", "info", "warn", "error"}
var seedStatuses = []int{200, 201, 301, 404, 500}

// seedLines builds the NDJSON body sent by the seed fixture: seedTotalEvents
// events all carrying ci_marker=marker, varied level/status fields, with the
// last seedTraceEvents additionally carrying OTel-shaped trace_id/span_id/
// duration_ms fields.
func seedLines(marker string) string {
	var out string
	plain := seedTotalEvents - seedTraceEvents
	for i := 0; i < plain; i++ {
		out += jsonLine(map[string]any{
			"message":   fmt.Sprintf("bronto-ci seed event %d", i),
			"ci_marker": marker,
			"level":     seedLevels[i%len(seedLevels)],
			"status":    seedStatuses[i%len(seedStatuses)],
			"seq":       i,
		})
	}
	for i := 0; i < seedTraceEvents; i++ {
		out += jsonLine(map[string]any{
			"message":     fmt.Sprintf("bronto-ci seed trace %d", i),
			"ci_marker":   marker,
			"level":       "info",
			"status":      200,
			"trace_id":    randHex(16),
			"span_id":     randHex(8),
			"duration_ms": 10 + i*5,
			"seq":         plain + i,
		})
	}
	return out
}

// jsonLine marshals ev as one NDJSON line (compact JSON + trailing newline).
func jsonLine(ev map[string]any) string {
	enc, err := json.Marshal(ev)
	if err != nil {
		// ev is always a map[string]any built from string/int/float literals
		// in this package — never actually fails.
		panic(err)
	}
	return string(enc) + "\n"
}

// newMarker returns a fresh, effectively-unique marker string (32 hex
// characters from crypto/rand), used as the ci_marker field value that lets
// every search in this run scope itself to exactly the data it seeded.
func newMarker() string {
	return randHex(16)
}

func randHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand.Read failing is effectively unheard-of on any real OS;
		// fall back to a timestamp so the run degrades instead of panicking.
		return fmt.Sprintf("fallback%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// --- shared search-arg builders ----------------------------------------------
//
// searchArgs/searchMarkerArgs build `search` argv slices against an already-
// resolved dataset log_id (see logIDForDataset) — never a from_expr guess
// over the dataset's name: the query-syntax reference documents from_expr
// selecting datasets by tag ("tag.env = 'prod'"), not by name, so resolving
// the real log_id via `datasets list` and passing -d is the certain path.

// searchArgs builds ["search", "-d", logID, "--where", where, "--since", "1h", extra...].
func searchArgs(logID, where string, extra ...string) []string {
	args := []string{"search", "-d", logID, "--where", where, "--since", "1h"}
	return append(args, extra...)
}

// seedSelectFields are the columns searchMarkerArgs requests by default:
// enough to prove structured passthrough (ci_marker/level/status) without
// callers having to spell out --select repeatedly.
var seedSelectFields = []string{"@time", "ci_marker", "level", "status"}

// searchMarkerArgs builds a `search` invocation scoped to logID and
// `ci_marker = '<marker>'`, selecting seedSelectFields, plus any extra args
// (e.g. -o json, --fields, --jq) appended after that.
func searchMarkerArgs(logID, marker string, extra ...string) []string {
	var selects []string
	for _, f := range seedSelectFields {
		selects = append(selects, "--select", f)
	}
	return searchArgs(logID, fmt.Sprintf("ci_marker = '%s'", marker), append(selects, extra...)...)
}

// --- hermetic self-tests ------------------------------------------------------
//
// These need no live credentials and always run, giving this file (and the
// query-arg-building logic every data-path suite depends on) a real
// self-check even in plain, credential-less CI.

func TestSeedLines_Shape(t *testing.T) {
	marker := "test-marker-abc123"
	body := seedLines(marker)
	lines := splitNonEmptyLines(t, body)
	if len(lines) != seedTotalEvents {
		t.Fatalf("seedLines produced %d lines, want %d", len(lines), seedTotalEvents)
	}

	traceCount := 0
	for i, line := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d did not parse as JSON: %v\nline: %s", i, err, line)
		}
		if m, _ := ev["ci_marker"].(string); m != marker {
			t.Fatalf("line %d ci_marker = %q, want %q", i, m, marker)
		}
		if msg, _ := ev["message"].(string); msg == "" {
			t.Fatalf("line %d missing non-empty message: %+v", i, ev)
		}
		if _, ok := ev["trace_id"]; ok {
			traceCount++
			if _, ok := ev["span_id"]; !ok {
				t.Fatalf("line %d has trace_id but no span_id: %+v", i, ev)
			}
			if _, ok := ev["duration_ms"]; !ok {
				t.Fatalf("line %d has trace_id but no duration_ms: %+v", i, ev)
			}
		}
	}
	if traceCount != seedTraceEvents {
		t.Fatalf("seedLines produced %d trace-shaped lines, want %d", traceCount, seedTraceEvents)
	}
}

func splitNonEmptyLines(t *testing.T, s string) []string {
	t.Helper()
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			if line := s[start:i]; line != "" {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	return lines
}

func TestNewMarker_UniqueAndNonEmpty(t *testing.T) {
	a, b := newMarker(), newMarker()
	if a == "" || b == "" {
		t.Fatal("newMarker returned an empty string")
	}
	if a == b {
		t.Fatalf("two consecutive newMarker calls collided: %q", a)
	}
}

func TestSearchArgsHelpers(t *testing.T) {
	const logID = "11111111-1111-1111-1111-111111111111"
	const marker = "m-1"

	args := searchArgs(logID, "status >= 500", "-o", "json")
	want := []string{"search", "-d", logID, "--where", "status >= 500", "--since", "1h", "-o", "json"}
	if !equalStrings(args, want) {
		t.Fatalf("searchArgs = %v, want %v", args, want)
	}

	markerArgs := searchMarkerArgs(logID, marker, "-n", "5")
	if markerArgs[len(markerArgs)-2] != "-n" || markerArgs[len(markerArgs)-1] != "5" {
		t.Fatalf("searchMarkerArgs did not append extra args verbatim: %v", markerArgs)
	}
	joined := strings.Join(markerArgs, " ")
	if !strings.Contains(joined, "ci_marker = 'm-1'") {
		t.Fatalf("searchMarkerArgs where clause missing marker: %v", markerArgs)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
