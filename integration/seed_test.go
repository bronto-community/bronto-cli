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
	// seedPollInterval is the FIRST readiness-poll delay; every later delay
	// backs off toward pollMaxInterval (see backoffInterval in harness.go).
	// Ingest-to-search latency is tens of seconds, so a fixed short cadence
	// spent most of its requests on ticks that could not yet succeed.
	seedPollInterval       = 2 * time.Second
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
	logID   string
	marker  string
	err     error

	probes probeState
}

// probeState carries the two one-off events the fixture sends alongside the
// seed batch for the ingest-roundtrip tests (see doSeed): the `send -m`
// one-shot and, when BRONTO_IT_INGEST_KEY is set, an event sent with the
// ingestion-scoped key.
//
// They ride along with the seed batch so the fixture's single readiness poll
// covers their visibility too — previously each test sent its own event and
// then ran its own 3-minute poll loop, paying Bronto's ingest-to-search
// latency (and its request cost) three times over for one shared wait.
//
// Each probe carries its OWN send error rather than failing the fixture:
// a broken ingestion key must fail TestIngestRoundtrip_IngestionKeySend, not
// every data-dependent test in the suite.
type probeState struct {
	oneShotMessage string
	oneShotToken   string
	oneShotErr     error

	ingestToken   string
	ingestErr     error
	ingestSkipped bool
}

var (
	seedOnce     sync.Once
	seedStateVal seedState
)

// seededData seeds ~20 structured NDJSON events (once per test binary, lazily
// on first call) into a run-scoped dataset with a unique ci_marker field,
// blocks until they — and the ride-along probes (see probeState) — are
// visible to search, and returns (dataset name, marker) to every caller
// thereafter. Companions: seededLogID for the dataset's log_id, seededProbes
// for the probe events.
//
// Skips t cleanly when BRONTO_IT_MGMT_KEY is unset (via skipIfNoCreds).
// Fails t hard — no retries, no flaky markers — if seeding itself fails or
// the readiness poll times out; per the plan, that failure carries the last
// search response for one-click triage (see pollSeedVisible below).
func seededData(t *testing.T) (dataset, marker string) {
	t.Helper()
	s := seeded(t)
	return s.dataset, s.marker
}

// seededLogID returns the seeded dataset's log_id (the UUID form -d wants for
// search/fields/context/tail/exports), resolved once by the fixture's own
// readiness poll and cached. Callers used to each re-derive it with a full
// `datasets list`, which cost one management-plane request per test for a
// value the fixture already had in hand.
func seededLogID(t *testing.T) string {
	t.Helper()
	return seeded(t).logID
}

// seededProbes returns the ride-along probe events' state (see probeState),
// triggering the fixture if it hasn't run yet.
func seededProbes(t *testing.T) probeState {
	t.Helper()
	return seeded(t).probes
}

// seeded runs the fixture once and hands back its state, failing t if
// seeding itself failed.
func seeded(t *testing.T) seedState {
	t.Helper()
	key := skipIfNoCreds(t)
	seedOnce.Do(func() {
		seedStateVal = doSeed(key)
	})
	if seedStateVal.err != nil {
		t.Fatalf("seed fixture: %v", seedStateVal.err)
	}
	return seedStateVal
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

	probes := sendProbes(ctx, r, dir, dataset, marker)

	logID, err := pollSeedVisible(ctx, r, dataset, marker, probes, seedPollBudget(), seedPollInterval)
	if err != nil {
		return seedState{err: err}
	}
	return seedState{dataset: dataset, logID: logID, marker: marker, probes: probes}
}

// sendProbes fires the two ride-along events the ingest-roundtrip tests
// assert on, into the same dataset as the seed batch and BEFORE the
// readiness poll, so one poll covers all of it (see probeState).
//
// Neither failure is fatal here: the error is recorded on probeState and
// pollSeedVisible then stops waiting for that probe, so a probe-specific
// problem (a dead ingestion key, say) fails only the test that owns it.
func sendProbes(ctx context.Context, r *Runner, configDir, dataset, marker string) probeState {
	p := probeState{ingestSkipped: ingestProbeUnavailable()}

	p.oneShotToken = newMarker()
	p.oneShotMessage = "bronto-ci one-shot " + p.oneShotToken
	switch res, err := r.Run(ctx, "", "send", "-d", dataset, "-m", p.oneShotMessage); {
	case err != nil:
		p.oneShotErr = fmt.Errorf("running send -m: %w", err)
	case res.ExitCode != 0:
		p.oneShotErr = fmt.Errorf("send -m exited %d\nstdout: %s\nstderr: %s",
			res.ExitCode, res.Stdout, res.Stderr)
	}

	if p.ingestSkipped {
		return p
	}
	// Same ci_marker as the seed batch so the readiness poll finds this event
	// with the same search; the probe token is what identifies it.
	p.ingestToken = newMarker()
	line := jsonLine(map[string]any{
		"message":   "bronto-ci ingestion-key roundtrip",
		"ci_marker": marker,
		"probe":     p.ingestToken,
		"level":     "info",
	})
	ingestR := newSweepRunner(binPath, os.Getenv("BRONTO_IT_INGEST_KEY"), configDir)
	switch res, err := ingestR.Run(ctx, line, "send", "-d", dataset); {
	case err != nil:
		p.ingestErr = fmt.Errorf("running send with the ingestion key: %w", err)
	case res.ExitCode != 0:
		p.ingestErr = fmt.Errorf("send with the ingestion key exited %d\nstdout: %s\nstderr: %s",
			res.ExitCode, res.Stdout, res.Stderr)
	}
	return p
}

// ingestProbeUnavailable reports whether the ingestion-key probe has to be skipped
// because BRONTO_IT_INGEST_KEY isn't set (the management key is deliberately
// NOT a substitute: the point of that probe is exercising an
// ingestion-scoped key).
func ingestProbeUnavailable() bool { return os.Getenv("BRONTO_IT_INGEST_KEY") == "" }

// pollSeedVisible blocks until everything the fixture sent is visible to
// search, or timeout elapses, and returns the dataset's resolved log_id.
// It has two things to wait for in sequence, both subject to eventual
// consistency: the dataset itself appearing in `datasets list` (ingestion
// auto-creates the log/dataset on first event, but the management-plane
// listing may lag slightly behind), and then the seeded events appearing in
// `search`. Both are re-checked on every tick so the function makes progress
// on whichever is still pending.
//
// "Everything" means the seed batch plus whichever probes sendProbes managed
// to send — matched in ONE search per tick (`ci_marker = <marker> OR message
// = <one-shot>`, which spans all three) rather than a poll loop per event.
// Matching is substring-on-stdout, deliberately: the probe tokens are unique
// random hex, and this stays correct whether the API returns fields at the
// top level or nested under message_kvs (see TestQuery_FieldsCommandListsMarkerKey,
// which accepts both spellings).
//
// Delays back off from interval toward pollMaxInterval: ingest-to-search
// latency runs to tens of seconds, so a fixed short cadence spent most of
// its requests re-asking a question that could not yet be answered.
//
// On timeout, the returned error names what was still missing and carries
// the LAST command's stdout/stderr (one-click triage per the plan) — no
// auto-retry beyond this single bounded window, no flaky test markers.
func pollSeedVisible(ctx context.Context, r *Runner, datasetName, marker string, probes probeState,
	timeout, interval time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)

	where := fmt.Sprintf("ci_marker = '%s'", marker)
	if probes.oneShotErr == nil {
		where += fmt.Sprintf(" OR message = '%s'", probes.oneShotMessage)
	}

	var logID, lastStdout, lastStderr, lastStep string
	var missing []string
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
			res, err := r.Run(ctx, "", "search", where,
				"-d", logID, "--since", "1h", "-o", "json", "-n", "100")
			if err == nil {
				lastStdout, lastStderr, lastStep = res.Stdout, res.Stderr, "search"
				if res.ExitCode == 0 {
					if missing = missingProbes(res.Stdout, marker, probes); len(missing) == 0 {
						return logID, nil
					}
				}
			}
		}
		if time.Now().After(deadline) {
			pending := "the dataset itself (not yet in `datasets list`)"
			if logID != "" {
				pending = strings.Join(missing, ", ")
			}
			return "", fmt.Errorf(
				"seed data for dataset %q (marker %s) not visible to search after %s\n"+
					"still missing: %s (last step: %s)\nlast stdout: %s\nlast stderr: %s",
				datasetName, marker, timeout, pending, lastStep, lastStdout, lastStderr)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("seed poll canceled: %w", ctx.Err())
		case <-time.After(interval):
		}
		interval = backoffInterval(interval)
	}
}

// missingProbes names which of the fixture's events are not yet present in
// a search response body, in the order they were sent. An empty result means
// everything the fixture successfully sent is searchable.
func missingProbes(stdout, marker string, probes probeState) []string {
	var missing []string
	if !strings.Contains(stdout, marker) {
		missing = append(missing, "the seed batch")
	}
	if probes.oneShotErr == nil && !strings.Contains(stdout, probes.oneShotToken) {
		missing = append(missing, "the `send -m` one-shot probe")
	}
	if !probes.ingestSkipped && probes.ingestErr == nil && !strings.Contains(stdout, probes.ingestToken) {
		missing = append(missing, "the ingestion-key probe")
	}
	return missing
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
// resolved dataset log_id (see seededLogID) — never a from_expr guess
// over the dataset's name: the query-syntax reference documents from_expr
// selecting datasets by tag ("tag.env = 'prod'"), not by name, so resolving
// the real log_id via `datasets list` and passing -d is the certain path.

// searchArgs builds ["search", <where>, "-d", logID, "--since", "1h", extra...].
// The query is a POSITIONAL argument on bronto search (clean-slate v2
// grammar) — there is no --where flag on search (exports create has one).
func searchArgs(logID, where string, extra ...string) []string {
	args := []string{"search", where, "-d", logID, "--since", "1h"}
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

// eventField reads a structured field from a search -o json row. The live
// API nests ingested KV fields under message_kvs, and the CLI flattens
// nested maps to dotted keys (bronto.Flatten), so a seeded "level" field
// surfaces as "message_kvs.level". The bare key is checked first in case a
// field ever appears at the top level.
func eventField(row map[string]any, key string) any {
	if v, ok := row[key]; ok {
		return v
	}
	return row["message_kvs."+key]
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
	want := []string{"search", "status >= 500", "-d", logID, "--since", "1h", "-o", "json"}
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
