# bronto-cli v2 — Plan 4: Send (ingestion) + Auth (keychain profiles)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `bronto send` (pipe logs into Bronto: NDJSON ingestion with batching, gzip, routing headers) and the full `bronto auth login|status|switch|logout` flow with OS-keychain credential storage — plus the cleanup items deferred from Plan 3's final review.

**Architecture:** New leaf packages `internal/ingest` (NDJSON sender + batcher, talks to the ingestion host — a DIFFERENT host from the REST API) and `internal/secrets` (keychain wrapper with 0600-file fallback). The `auth` command family wires secrets into config resolution via an injectable seam in `NewApp`.

**Tech Stack:** Two NEW dependencies, both pure Go / CGO-free: `github.com/zalando/go-keyring` (macOS `security`, Linux D-Bus Secret Service, Windows wincred; ships `MockInit()` for tests) and `golang.org/x/term` (hidden password prompt). The Global Constraints dep allowlist is amended accordingly.

**Ingestion facts (binding, from the Bronto docs):**
- Host: `https://ingestion.<region>.bronto.io` (NOT the api. host). OTLP lives under `/v1/logs` (out of scope here).
- Body: NDJSON — one JSON object per line, NOT an array. Per event: `message` (required, string), `timestamp` (recommended, ISO 8601), any other key becomes a queryable field.
- Headers: `X-BRONTO-API-KEY` (transport adds it), `Content-Type: application/json`, `x-bronto-dataset` (name; auto-created), `x-bronto-collection`, `x-bronto-tags` (comma-separated k=v), optional `Content-Encoding: gzip`.
- Limits: 10 MB per request (413 if exceeded), 429 on rate limit, 200 = durably stored.

## Global Constraints

- Module `github.com/bronto-community/bronto-cli`; Go `1.25.0`; `CGO_ENABLED=0 go build ./...`; gofmt clean; golangci-lint 0 issues per commit (`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run`).
- Allowed runtime deps NOW: cobra, pelletier/go-toml/v2, mattn/go-isatty, oapi-codegen/runtime, **zalando/go-keyring, golang.org/x/term**. Test-only: rogpeppe/go-internal.
- stdout = data only; confirmations/summaries/warnings → stderr; `--quiet` silences stderr chatter; never prompt when stdin is not a TTY.
- Exit codes 0/1/2/3/4/5; stable snake_case error codes via clierr. 413 → `payload_too_large` (exit 1); 429 stays `rate_limited` (exit 5, retryable).
- Secrets NEVER written to config files; keychain is the store; the file fallback is `<config-dir>/bronto/credentials` (0600) used only when the keychain is unavailable, with a stderr warning.
- Conventional commits.

**Existing interfaces (do not re-implement):** `cli.App/NewApp` (`.Config .Stdout .Stderr .HTTPClient .StdoutIsTTY .OutputFlag .Quiet .Color`, `.Printer(streaming)`, package seam `stdoutIsTTY func() bool`); `config` (`Load/LoadOptions`, `Value{Val,Source}`, `SetUserValue(dir, profile, key, value)`, sources incl. `SourceDefault`); `api.NewHTTPClient(key, version)` (transport injects `X-BRONTO-API-KEY` + UA; retries GET/HEAD only), `api.ErrorFromStatus`; `clierr`; `output`; `timerange`; `cli.Execute(ctx, cmd)`.

---

### Task 1: Plan-3 deferred cleanups

**Files:**
- Modify: `internal/traces/waterfall.go`, `internal/traces/shape.go`, `internal/traces/aggregate.go`, `internal/traces/query.go`, `internal/cli/traces.go` (+ tests alongside)

**Interfaces:**
- Produces:
  - `traces.padTo(s string, width int) string` and `traces.truncateTo(s string, width int) string` — rune-aware (utf8.RuneCountInString / rune-slice + `…`); used for waterfall label padding and shape label truncation (replaces byte-based `len`/slicing)
  - `traces.KindClause(kind string) (string, error)` — SIGNATURE CHANGE: validates against {SERVER, CLIENT, INTERNAL, PRODUCER, CONSUMER} (bare or SPAN_KIND_-prefixed, case-insensitive); unknown → `clierr.New("usage_invalid_kind", ...)`. Update callers: `Attributes` (returns the error), `internal/cli/traces.go` shape's `shapeSampleWhere` (propagate error)
  - `traces.ErrorsClause` exported (was `errorsClause`); the duplicated literal in `internal/cli/traces.go` replaced with the export
  - `traces.isMissingValue(v string) bool` shared by `hasMissing`/`labelGroupValue`
  - `show` truncation hint: in the show RunE, when `len(spans) == limit`, TTY+!quiet stderr note: `Showing the first N spans — the trace may be larger; raise -n to see more.`
- Consumes: existing code.

- [ ] **Step 1: Failing tests** — add to the respective `_test.go` files:

```go
// internal/traces/waterfall_test.go
func TestRuneAwarePadding(t *testing.T) {
	if got := padTo("héllo", 8); utf8.RuneCountInString(got) != 8 {
		t.Fatalf("padTo rune width = %d (%q)", utf8.RuneCountInString(got), got)
	}
	if got := truncateTo("héllö-wörld", 6); utf8.RuneCountInString(got) != 6 ||
		!strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Fatalf("truncateTo = %q", got)
	}
	if got := truncateTo("ok", 6); got != "ok" {
		t.Fatalf("short strings unchanged: %q", got)
	}
}

// internal/traces/query_test.go
func TestKindClauseValidation(t *testing.T) {
	for _, ok := range []string{"server", "SPAN_KIND_CLIENT", "Internal", "producer", "CONSUMER"} {
		if _, err := KindClause(ok); err != nil {
			t.Errorf("KindClause(%q) errored: %v", ok, err)
		}
	}
	if _, err := KindClause("sideways"); err == nil {
		t.Error("unknown kind must error")
	}
	got, _ := KindClause("server")
	if got != "$span.kind = 'SPAN_KIND_SERVER'" {
		t.Fatalf("clause = %q", got)
	}
}
```

CLI-level: `TestTracesAggregateRejectsBadKind` in `internal/cli/traces_test.go` — `traces aggregate --by x --kind sideways` → exit 2 (no network; the validation must run before queries).

- [ ] **Step 2: Implement** — mechanical per the Interfaces block. `padTo`: append spaces up to rune width. `truncateTo`: rune-slice `[:width-1] + "…"` when longer. Replace in `RenderWaterfall` (`pad` computation and `maxLabel` via rune count) and `RenderShape` (label truncation + `%-*s` → manual padTo since `%-*s` is byte-width). Export `ErrorsClause`. `isMissingValue` shared. KindClause signature change ripples to `Attributes` and shape's clause builder — compile errors guide you. The show hint goes right after the empty-check in the show RunE.

- [ ] **Step 3: Verify + commit**

```bash
go test ./... && CGO_ENABLED=0 go build ./... && gofmt -l internal cmd
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal && git commit -m "refactor: plan-3 deferred cleanups (rune-width, kind validation, shared clauses)"
```

---

### Task 2: Ingestion sender (`internal/ingest`)

**Files:**
- Create: `internal/ingest/ingest.go`, `internal/ingest/ingest_test.go`

**Interfaces:**
- Produces:
  - `ingest.URL(region, override string) string` — override wins; else `https://ingestion.<region>.bronto.io`
  - `ingest.LineToEvent(line string, now func() time.Time) map[string]any` — if the line parses as a JSON object: passthrough, add `timestamp` (RFC3339, from now()) when absent, and add `message` = raw line when absent; otherwise `{"message": line, "timestamp": <now RFC3339>}`. nil now → time.Now.
  - `type Sender struct { HTTP *http.Client; URL string; Dataset, Collection, Tags string; Gzip bool }`
  - `(*Sender).Send(ctx context.Context, events []map[string]any) error` — NDJSON body (compact JSON, `\n`-joined); `Content-Type: application/json`; `x-bronto-dataset`/`x-bronto-collection`/`x-bronto-tags` headers when non-empty; gzip body + `Content-Encoding: gzip` when Gzip; empty events → nil no-op. Status mapping: 413 → `clierr.New("payload_too_large", ...).WithHint("Reduce --batch-size.")`; everything else via `api.ErrorFromStatus`. Reject (usage error `usage_missing_message`) if any event lacks a non-empty `message` — checked before sending.
  - `type Batcher struct { MaxEvents, MaxBytes int; ... }`; `(*Batcher).Add(ev map[string]any) bool` — returns true when the batch is full AFTER adding (caller then Drains); `(*Batcher).Drain() []map[string]any` — returns and resets; `(*Batcher).Len() int`. Size accounting: compact-JSON byte length + 1 per event; MaxBytes default guidance 1 MiB (set by caller), MaxEvents by caller.
- Consumes: `api.ErrorFromStatus`, `clierr`.

- [ ] **Step 1: Failing tests**

`internal/ingest/ingest_test.go`:

```go
package ingest

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

var fixedNow = func() time.Time { return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC) }

func TestURL(t *testing.T) {
	if got := URL("eu", ""); got != "https://ingestion.eu.bronto.io" {
		t.Fatalf("got %q", got)
	}
	if got := URL("eu", "http://localhost:1"); got != "http://localhost:1" {
		t.Fatalf("override: %q", got)
	}
}

func TestLineToEvent(t *testing.T) {
	ev := LineToEvent("plain log line", fixedNow)
	if ev["message"] != "plain log line" || ev["timestamp"] != "2026-07-07T12:00:00Z" {
		t.Fatalf("plain: %v", ev)
	}
	ev2 := LineToEvent(`{"message":"structured","level":"warn","timestamp":"2026-01-01T00:00:00Z"}`, fixedNow)
	if ev2["level"] != "warn" || ev2["timestamp"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("passthrough: %v", ev2)
	}
	ev3 := LineToEvent(`{"level":"info"}`, fixedNow) // JSON object without message
	if ev3["message"] != `{"level":"info"}` || ev3["timestamp"] == nil {
		t.Fatalf("no-message object: %v", ev3)
	}
	ev4 := LineToEvent(`[1,2]`, fixedNow) // JSON but not an object -> plain
	if ev4["message"] != "[1,2]" {
		t.Fatalf("non-object: %v", ev4)
	}
}

func TestSendNDJSONHeadersAndGzip(t *testing.T) {
	var gotBody string
	var gotHdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHdr = r.Header.Clone()
		var rd io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			rd = gz
		}
		b, _ := io.ReadAll(rd)
		gotBody = string(b)
	}))
	defer srv.Close()

	s := &Sender{HTTP: srv.Client(), URL: srv.URL,
		Dataset: "app-logs", Collection: "prod", Tags: "env=prod,team=core", Gzip: true}
	err := s.Send(context.Background(), []map[string]any{
		{"message": "one", "timestamp": "t1"},
		{"message": "two", "timestamp": "t2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(gotBody), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson lines = %d: %q", len(lines), gotBody)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil || first["message"] != "one" {
		t.Fatalf("line0 = %q", lines[0])
	}
	if gotHdr.Get("x-bronto-dataset") != "app-logs" ||
		gotHdr.Get("x-bronto-collection") != "prod" ||
		gotHdr.Get("x-bronto-tags") != "env=prod,team=core" ||
		gotHdr.Get("Content-Type") != "application/json" ||
		gotHdr.Get("Content-Encoding") != "gzip" {
		t.Fatalf("headers = %v", gotHdr)
	}
}

func TestSendRejectsMissingMessage(t *testing.T) {
	s := &Sender{HTTP: http.DefaultClient, URL: "http://127.0.0.1:1"}
	err := s.Send(context.Background(), []map[string]any{{"level": "info"}})
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2 before any network, got %v", err)
	}
}

func TestSendMapsStatuses(t *testing.T) {
	for _, c := range []struct {
		status int
		code   string
		exit   int
	}{
		{413, "payload_too_large", 1},
		{429, "rate_limited", 5},
		{401, "auth_invalid_key", 3},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
		}))
		s := &Sender{HTTP: srv.Client(), URL: srv.URL}
		err := s.Send(context.Background(), []map[string]any{{"message": "x"}})
		srv.Close()
		if err == nil || clierr.ExitCode(err) != c.exit {
			t.Fatalf("status %d: got %v (exit %d), want exit %d", c.status, err, clierr.ExitCode(err), c.exit)
		}
	}
}

func TestSendEmptyIsNoop(t *testing.T) {
	s := &Sender{HTTP: http.DefaultClient, URL: "http://127.0.0.1:1"} // would fail if dialed
	if err := s.Send(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestBatcher(t *testing.T) {
	b := &Batcher{MaxEvents: 2, MaxBytes: 1 << 20}
	if full := b.Add(map[string]any{"message": "a"}); full {
		t.Fatal("not full at 1/2")
	}
	if full := b.Add(map[string]any{"message": "b"}); !full {
		t.Fatal("full at 2/2")
	}
	got := b.Drain()
	if len(got) != 2 || b.Len() != 0 {
		t.Fatalf("drain = %d, len = %d", len(got), b.Len())
	}
	// byte-based flush
	bb := &Batcher{MaxEvents: 1000, MaxBytes: 30}
	bb.Add(map[string]any{"message": "aaaaaaaaaaaaaaa"}) // ~30 bytes encoded
	if full := bb.Add(map[string]any{"message": "b"}); !full {
		t.Fatal("byte cap must trigger")
	}
}
```

Run: `go test ./internal/ingest -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/ingest/ingest.go`:

```go
// Package ingest sends events to Bronto's ingestion host (a separate host
// from the REST API): NDJSON bodies, routing headers, optional gzip.
package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bronto-community/bronto-cli/internal/api"
	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func URL(region, override string) string {
	if override != "" {
		return override
	}
	return fmt.Sprintf("https://ingestion.%s.bronto.io", region)
}

// LineToEvent converts one input line into an ingestion event. JSON objects
// pass through (timestamp added when absent; message backfilled with the
// raw line when absent); anything else becomes {"message": line, "timestamp": now}.
func LineToEvent(line string, now func() time.Time) map[string]any {
	if now == nil {
		now = time.Now
	}
	var obj map[string]any
	if len(line) > 0 && line[0] == '{' && json.Unmarshal([]byte(line), &obj) == nil && obj != nil {
		if _, ok := obj["timestamp"]; !ok {
			obj["timestamp"] = now().UTC().Format(time.RFC3339)
		}
		if _, ok := obj["message"]; !ok {
			obj["message"] = line
		}
		return obj
	}
	return map[string]any{
		"message":   line,
		"timestamp": now().UTC().Format(time.RFC3339),
	}
}

type Sender struct {
	HTTP       *http.Client
	URL        string
	Dataset    string
	Collection string
	Tags       string
	Gzip       bool
}

func (s *Sender) Send(ctx context.Context, events []map[string]any) error {
	if len(events) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf) // Encode appends \n after each object -> NDJSON
	for _, ev := range events {
		msg, ok := ev["message"].(string)
		if !ok || msg == "" {
			return clierr.New("usage_missing_message",
				"every event needs a non-empty \"message\" field")
		}
		if err := enc.Encode(ev); err != nil {
			return err
		}
	}
	body := buf.Bytes()
	if s.Gzip {
		var zbuf bytes.Buffer
		zw := gzip.NewWriter(&zbuf)
		if _, err := zw.Write(body); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		body = zbuf.Bytes()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Gzip {
		req.Header.Set("Content-Encoding", "gzip")
	}
	if s.Dataset != "" {
		req.Header.Set("x-bronto-dataset", s.Dataset)
	}
	if s.Collection != "" {
		req.Header.Set("x-bronto-collection", s.Collection)
	}
	if s.Tags != "" {
		req.Header.Set("x-bronto-tags", s.Tags)
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return clierr.New("network_error", err.Error()).WithRetryable().
			WithHint("Check your network and the ingestion URL / region.")
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		return clierr.New("payload_too_large",
			fmt.Sprintf("ingestion rejected a %d-byte batch (10 MB cap)", len(body))).
			WithHint("Reduce --batch-size.")
	}
	return errOrNil(api.ErrorFromStatus(resp.StatusCode, respBody))
}

// errOrNil avoids returning a non-nil interface holding a nil *clierr.Error.
func errOrNil(e *clierr.Error) error {
	if e == nil {
		return nil
	}
	return e
}

// Batcher accumulates events until an event-count or byte cap is reached.
type Batcher struct {
	MaxEvents int
	MaxBytes  int
	events    []map[string]any
	bytes     int
}

func (b *Batcher) Add(ev map[string]any) bool {
	b.events = append(b.events, ev)
	if enc, err := json.Marshal(ev); err == nil {
		b.bytes += len(enc) + 1
	}
	return len(b.events) >= b.MaxEvents || b.bytes >= b.MaxBytes
}

func (b *Batcher) Drain() []map[string]any {
	out := b.events
	b.events = nil
	b.bytes = 0
	return out
}

func (b *Batcher) Len() int { return len(b.events) }
```

Run: `go test ./internal/ingest -v` — Expected: PASS.

- [ ] **Step 3: Commit**

```bash
gofmt -l internal && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/ingest
git commit -m "feat: NDJSON ingestion sender with batching, gzip, routing headers"
```

---

### Task 3: `bronto send` command

**Files:**
- Create: `internal/cli/send.go`, `internal/cli/send_test.go`
- Modify: `internal/cli/root.go` (register), `internal/config/config.go` (add `ingest_url` to `fileKeys` and `envKeys` as `BRONTO_INGEST_URL`)

**Interfaces:**
- Produces: `newSendCmd() *cobra.Command` — `bronto send`:
  - Flags: `--dataset/-d` (dataset NAME, required unless `-m` with config... keep simply REQUIRED — `MarkFlagRequired`), `--collection`, `--tag` (StringArray `k=v`, joined with commas after validating each contains `=` → else `usage_invalid_tag`), `--message/-m` (one-shot: send exactly one event, ignore stdin), `--ingest-url` (override; config key `ingest_url` / env `BRONTO_INGEST_URL` resolve through the normal chain), `--batch-size` (default 500, validatePositive), `--batch-bytes` (default 1<<20), `--flush-interval` (duration, default 1s, min 100ms → `usage_invalid_interval`), `--no-gzip` (gzip ON by default), `--timestamp-field`? NO — YAGNI.
  - Sender URL: flag > config `ingest_url` > `ingest.URL(region, "")`.
  - One-shot: `-m "text"` → single `LineToEvent`-equivalent event, send, print `Sent 1 event.` to stderr (unless quiet), exit.
  - Stream mode: read stdin lines (bufio.Scanner, 1 MiB max token via `Buffer`), each → `ingest.LineToEvent(line, nil)` (skip empty lines) → `Batcher{MaxEvents: batchSize, MaxBytes: batchBytes}`; flush when Add reports full; ALSO flush on a `--flush-interval` ticker (so `tail -f | bronto send` ships promptly); on stdin EOF flush the remainder; on ctx cancellation flush best-effort then return nil (clean Ctrl-C).
  - Concurrency shape: scanner in a goroutine feeding a `chan string`; main loop `select` on lines / ticker.C / ctx.Done. Sends happen on the main loop (serialized).
  - Summary to stderr unless quiet: `Sent %d event(s) in %d batch(es).`
  - Errors: first failed Send aborts with its typed error (events in flight are lost — document in Long).
  - Uses `app.HTTPClient` (transport injects the API key; POSTs are not retried by the transport — acceptable, ingestion consumers handle 429 manually? NO: keep it simple, a failed batch aborts; the typed 429 is retryable so scripts can rerun).
- Consumes: Task 2, `App`.

- [ ] **Step 1: Failing tests**

`internal/cli/send_test.go` (style: existing httptest + root.Execute patterns):

```go
package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func TestSendOneShotMessage(t *testing.T) {
	var body string
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	defer srv.Close()

	root := NewRootCmd()
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	root.SetArgs([]string{"send", "-d", "app", "-m", "hello world", "--no-gzip",
		"--ingest-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &ev); err != nil || ev["message"] != "hello world" {
		t.Fatalf("body = %q", body)
	}
	if hdr.Get("x-bronto-dataset") != "app" || hdr.Get("X-Bronto-Api-Key") == "" {
		t.Fatalf("headers = %v", hdr)
	}
	if !strings.Contains(errBuf.String(), "Sent 1 event") {
		t.Fatalf("summary = %q", errBuf.String())
	}
}

func TestSendStdinBatches(t *testing.T) {
	var batches atomic.Int32
	var lines atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batches.Add(1)
		b, _ := io.ReadAll(r.Body)
		lines.Add(int32(strings.Count(strings.TrimSpace(string(b)), "\n") + 1))
	}))
	defer srv.Close()

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader("l1\nl2\nl3\n"))
	root.SetArgs([]string{"send", "-d", "app", "--batch-size", "2", "--no-gzip",
		"--ingest-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if batches.Load() != 2 || lines.Load() != 3 { // 2+1
		t.Fatalf("batches=%d lines=%d, want 2/3", batches.Load(), lines.Load())
	}
}

func TestSendStructuredPassthrough(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	defer srv.Close()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader(`{"message":"m","level":"warn"}` + "\n"))
	root.SetArgs([]string{"send", "-d", "app", "--no-gzip", "--ingest-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	_ = json.Unmarshal([]byte(strings.TrimSpace(body)), &ev)
	if ev["level"] != "warn" {
		t.Fatalf("passthrough lost fields: %q", body)
	}
}

func TestSendInvalidTag(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"send", "-d", "app", "-m", "x", "--tag", "noequals", "--api-key", "k"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v", err)
	}
}

func TestSendAuthErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"send", "-d", "app", "-m", "x", "--ingest-url", srv.URL, "--api-key", "bad"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 3 {
		t.Fatalf("want exit 3, got %v", err)
	}
}
```

Run: `go test ./internal/cli -run TestSend -v` — Expected: FAIL.

- [ ] **Step 2: Implement** `internal/cli/send.go` per the Interfaces block (full command; follow tail.go's loop patterns: scanner goroutine → `lines` channel closed on EOF, `select` on lines/ticker/ctx; drain-and-send helper closure sharing the Sender; counters for the summary). Config change: add `"ingest_url": "BRONTO_INGEST_URL"` to `envKeys` and `ingest_url` to `fileKeys`. Sender: `&ingest.Sender{HTTP: app.HTTPClient, URL: <resolved>, Dataset: dataset, Collection: collection, Tags: strings.Join(tags, ","), Gzip: !noGzip}` — validate each `--tag` contains "=" first. Register `newSendCmd()` in root.go.

- [ ] **Step 3: Verify + commit**

```bash
go test ./internal/cli -run TestSend -v && go test ./...
CGO_ENABLED=0 go build ./... && gofmt -l internal cmd
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/cli internal/config
git commit -m "feat: bronto send — pipe logs into Bronto with batching and gzip"
```

---

### Task 4: Secrets store (`internal/secrets`) + config integration

**Files:**
- Create: `internal/secrets/secrets.go`, `internal/secrets/secrets_test.go`
- Modify: `internal/config/config.go` (add `Inject` method + `SourceKeychain`), `internal/cli/context.go` (keychain lookup seam in NewApp), `go.mod` (new dep)

**Interfaces:**
- Produces:
  - `secrets.Store(profile, key string) (fallback bool, err error)`; `secrets.Get(profile string) (key string, fallback bool, err error)`; `secrets.Delete(profile string) error` — keyring service name `"bronto-cli"`, account = profile (empty profile → `"default"`). `Get` on a missing entry returns `("", false, secrets.ErrNotFound)`.
  - Fallback: when the keyring returns any error other than not-found (headless Linux without D-Bus, containers), read/write `<config-dir>/bronto/credentials` (TOML `map[string]string` profile→key, 0600; `BRONTO_CONFIG_DIR` honored via the same resolution as config). `fallback=true` signals callers to print a one-line stderr warning.
  - `config.SourceKeychain Source = "keychain"`; `(*Config).Inject(key, val string, src Source)` — sets a value+source ONLY if the key is currently absent (preserves precedence).
  - `config.SetDefaultProfile(dir, name string) error` — updates `default_profile` in the user config file (read-modify-write like SetUserValue).
  - NewApp integration: after `config.Load`, `if cfg.APIKey() == "" { if key, fb, err := secretLookup(profileOrDefault(cfg.Profile())); err == nil { cfg.Inject("api_key", key, config.SourceKeychain); warn on fb } }` — with `var secretLookup = secrets.Get` as the package-level test seam in `internal/cli`.
- Consumes: `zalando/go-keyring` (`keyring.Set/Get/Delete`, `keyring.ErrNotFound`, `keyring.MockInit()`, `keyring.MockInitWithError(err)` for tests), `go-toml/v2`, `clierr`.

- [ ] **Step 1: Deps + failing tests**

```bash
go get github.com/zalando/go-keyring@latest
go get golang.org/x/term@latest   # used in Task 5; added now so go.mod changes once
```

`internal/secrets/secrets_test.go`:

```go
package secrets

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestStoreGetDeleteRoundTrip(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir()) // isolate the fallback file path too
	fb, err := Store("prod", "sekret-key")
	if err != nil || fb {
		t.Fatalf("store: fb=%v err=%v", fb, err)
	}
	key, fb, err := Get("prod")
	if err != nil || fb || key != "sekret-key" {
		t.Fatalf("get: %q fb=%v err=%v", key, fb, err)
	}
	if err := Delete("prod"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Get("prod"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestEmptyProfileMapsToDefault(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	if _, err := Store("", "k1"); err != nil {
		t.Fatal(err)
	}
	key, _, err := Get("default")
	if err != nil || key != "k1" {
		t.Fatalf("got %q, %v", key, err)
	}
}

func TestFileFallbackWhenKeyringUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New("no dbus"))
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	fb, err := Store("prod", "file-key")
	if err != nil || !fb {
		t.Fatalf("store fallback: fb=%v err=%v", fb, err)
	}
	key, fb, err := Get("prod")
	if err != nil || !fb || key != "file-key" {
		t.Fatalf("get fallback: %q fb=%v err=%v", key, fb, err)
	}
	if err := Delete("prod"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Get("prod"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}
```

Config side (`internal/config/config_test.go` additions):

```go
func TestInjectOnlyWhenAbsent(t *testing.T) {
	cfg, err := Load(LoadOptions{Getenv: env(map[string]string{"BRONTO_API_KEY": "envkey"}),
		WorkDir: t.TempDir(), UserConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Inject("api_key", "keychain-key", SourceKeychain)
	if cfg.APIKey() != "envkey" { // env wins; Inject must not override
		t.Fatalf("APIKey = %q", cfg.APIKey())
	}
	cfg2, _ := Load(LoadOptions{Getenv: env(nil), WorkDir: t.TempDir(), UserConfigDir: t.TempDir()})
	cfg2.Inject("api_key", "keychain-key", SourceKeychain)
	v, _ := cfg2.Get("api_key")
	if v.Val != "keychain-key" || v.Source != SourceKeychain {
		t.Fatalf("injected: %+v", v)
	}
}

func TestSetDefaultProfile(t *testing.T) {
	dir := t.TempDir()
	if err := SetUserValue(dir, "prod", "region", "us"); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultProfile(dir, "prod"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{Getenv: func(string) string { return "" },
		WorkDir: t.TempDir(), UserConfigDir: dir})
	if err != nil || cfg.Profile() != "prod" {
		t.Fatalf("profile = %q, %v", cfg.Profile(), err)
	}
}
```

NewApp seam test (`internal/cli/`, new or existing test file):

```go
func TestNewAppFallsBackToKeychain(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	old := secretLookup
	secretLookup = func(profile string) (string, bool, error) { return "kc-key", false, nil }
	t.Cleanup(func() { secretLookup = old })

	cmd := NewRootCmd()
	pingCmd, _, _ := cmd.Find([]string{"ping"})
	app, err := NewApp(pingCmd)
	if err != nil {
		t.Fatal(err)
	}
	if app.Config.APIKey() != "kc-key" {
		t.Fatalf("APIKey = %q", app.Config.APIKey())
	}
	v, _ := app.Config.Get("api_key")
	if string(v.Source) != "keychain" {
		t.Fatalf("source = %s", v.Source)
	}
}
```

(Ensure this test isolates `BRONTO_API_KEY` — t.Setenv it to "" if the host env may carry one.)

Run: `go test ./internal/secrets ./internal/config ./internal/cli -run 'TestStore|TestEmpty|TestFileFallback|TestInject|TestSetDefault|TestNewAppFalls' -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/secrets/secrets.go`:

```go
// Package secrets stores API keys in the OS keychain (macOS Keychain,
// Linux Secret Service, Windows Credential Manager) with a 0600
// credentials-file fallback for headless environments (spec §6).
package secrets

import (
	"errors"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/zalando/go-keyring"
)

const service = "bronto-cli"

var ErrNotFound = errors.New("no stored credential")

func account(profile string) string {
	if profile == "" {
		return "default"
	}
	return profile
}

func Store(profile, key string) (bool, error) {
	if err := keyring.Set(service, account(profile), key); err != nil {
		return true, fileStore(account(profile), key)
	}
	return false, nil
}

func Get(profile string) (string, bool, error) {
	key, err := keyring.Get(service, account(profile))
	if err == nil {
		return key, false, nil
	}
	if errors.Is(err, keyring.ErrNotFound) {
		// keychain works but has no entry; still consult the fallback file
		// (a credential stored under fallback earlier must stay readable).
		key, ferr := fileGet(account(profile))
		if ferr != nil {
			return "", false, ErrNotFound
		}
		return key, true, nil
	}
	key, ferr := fileGet(account(profile))
	if ferr != nil {
		return "", true, ErrNotFound
	}
	return key, true, nil
}

func Delete(profile string) error {
	kerr := keyring.Delete(service, account(profile))
	ferr := fileDelete(account(profile))
	if kerr == nil || ferr == nil {
		return nil
	}
	if errors.Is(kerr, keyring.ErrNotFound) && errors.Is(ferr, ErrNotFound) {
		return nil // nothing stored anywhere: deleting is idempotent
	}
	return kerr
}

func credentialsPath() (string, error) {
	dir := os.Getenv("BRONTO_CONFIG_DIR")
	if dir == "" {
		d, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = d
	}
	return filepath.Join(dir, "bronto", "credentials"), nil
}

func readFileMap() (map[string]string, string, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, "", err
	}
	m := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		_ = toml.Unmarshal(b, &m)
	}
	return m, path, nil
}

func fileStore(account, key string) error {
	m, path, err := readFileMap()
	if err != nil {
		return err
	}
	m[account] = key
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := toml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func fileGet(account string) (string, error) {
	m, _, err := readFileMap()
	if err != nil {
		return "", err
	}
	key, ok := m[account]
	if !ok || key == "" {
		return "", ErrNotFound
	}
	return key, nil
}

func fileDelete(account string) error {
	m, path, err := readFileMap()
	if err != nil {
		return err
	}
	if _, ok := m[account]; !ok {
		return ErrNotFound
	}
	delete(m, account)
	b, err := toml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
```

`internal/config` additions: `SourceKeychain Source = "keychain"`;

```go
// Inject adds a resolved value from an out-of-band source (keychain)
// without disturbing precedence: no-op when the key is already set.
func (c *Config) Inject(key, val string, src Source) {
	if val == "" {
		return
	}
	if _, exists := c.values[key]; exists {
		return
	}
	c.values[key] = Value{Val: val, Source: src}
}

// SetDefaultProfile persists default_profile in the user config file.
func SetDefaultProfile(dir, name string) error {
	path := filepath.Join(dir, "bronto", "config.toml")
	uf := userFile{Profiles: map[string]map[string]string{}}
	if b, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(b, &uf); err != nil {
			return clierr.New("config_parse_error", "cannot parse "+path+": "+err.Error())
		}
		if uf.Profiles == nil {
			uf.Profiles = map[string]map[string]string{}
		}
	}
	uf.DefaultProfile = name
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := toml.Marshal(uf)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
```

`internal/cli/context.go` — in `NewApp` after config load and timeout handling:

```go
	if cfg.APIKey() == "" {
		if key, fb, err := secretLookup(profileOrDefault(cfg.Profile())); err == nil {
			cfg.Inject("api_key", key, config.SourceKeychain)
			if fb {
				quiet, _ := cmd.Flags().GetBool("quiet")
				if !quiet {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
						"Warning: OS keychain unavailable — using the credentials file fallback.")
				}
			}
		}
	}
```

with `var secretLookup = secrets.Get` at package level. NOTE: NewApp builds the HTTP client from `cfg.APIKey()` — perform the injection BEFORE constructing the client so the key reaches the transport.

- [ ] **Step 3: Verify + commit**

```bash
go test ./... && CGO_ENABLED=0 go build ./... && gofmt -l internal cmd
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/secrets internal/config internal/cli go.mod go.sum
git commit -m "feat: keychain credential store with file fallback, wired into config"
```

---

### Task 5: `bronto auth` command family

**Files:**
- Create: `internal/cli/auth.go`, `internal/cli/auth_test.go`
- Modify: `internal/cli/root.go` (register `newAuthCmd()`; also alias top-level `bronto login` → same RunE as `auth login`)

**Interfaces:**
- Produces: `newAuthCmd()` with subcommands:
  - `auth login [--profile p] [--key-stdin] [--region eu|us] [--base-url u]`:
    1. Obtain key: `--key-stdin` → read all stdin, TrimSpace; else if `stdoutIsTTY()` → hidden prompt `Bronto management API key: ` on stderr via `term.ReadPassword(int(os.Stdin.Fd()))` (package seam `var readPassword = term.ReadPassword` for tests); else → `clierr.New("usage_key_required", ...)` hinting `--key-stdin`. Empty key → same usage error.
    2. Validate + region detect: candidate base URLs = `--base-url` if set (with region = `--region` or resolved config region), else `https://api.<r>.bronto.io` for r in [--region if set, else "eu","us"]. For each: GET `/logs` with `api.NewHTTPClient(key, version.Version)`; 200 → success with that region; 401/403 → try next; network error → try next. All fail → `clierr.New("auth_invalid_key", "the key was not accepted in any region")` with the ingestion-key hint.
    3. Store: `secrets.Store(profile, key)` (fallback → stderr warning); `config.SetUserValue(dir, profile, "region", detectedRegion)`; `config.SetDefaultProfile(dir, profile)` — profile default "default".
    4. Confirmation to stderr: `Logged in — profile %q, region %s. Key stored in the OS keychain.` (or `credentials file` when fallback).
  - `auth status`: resolved profile, api_key source + first-8 mask (from `app.Config.Get("api_key")`), region, base URL, then a live GET `/logs`: OK / typed error message (reuse `api.ErrorFromStatus` semantics via the same call shape as ping). Output through the printer as ONE row (`profile,key_source,key,region,base_url,status`) so `-o json` is stable; human TTY table fine.
  - `auth switch <profile>`: error `profile_not_found` (exit 4) unless the profile has a keychain/file entry (secrets.Get) OR a section in the user config; then `config.SetDefaultProfile`. Confirmation to stderr.
  - `auth logout [--profile p]`: `secrets.Delete(profileOrDefault)`; confirmation to stderr; idempotent (no error when nothing stored).
  - Top-level alias: `bronto login` registered as a hidden-or-visible convenience command reusing the same RunE (spec §3 lists it).
- Consumes: Tasks 4, existing plumbing.

- [ ] **Step 1: Failing tests**

`internal/cli/auth_test.go` (keyring.MockInit in each test; BRONTO_CONFIG_DIR isolated):

```go
func TestAuthLoginKeyStdinStoresAndDetectsRegion(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-BRONTO-API-KEY") != "the-key" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader("the-key\n"))
	root.SetArgs([]string{"auth", "login", "--key-stdin", "--profile", "prod",
		"--region", "eu", "--base-url", srv.URL})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	key, _, err := secrets.Get("prod")
	if err != nil || key != "the-key" {
		t.Fatalf("stored key = %q, %v", key, err)
	}
	if !strings.Contains(errBuf.String(), `profile "prod"`) {
		t.Fatalf("confirmation = %q", errBuf.String())
	}
}

func TestAuthLoginRejectsBadKey(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader("bad\n"))
	root.SetArgs([]string{"auth", "login", "--key-stdin", "--region", "eu", "--base-url", srv.URL})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 3 {
		t.Fatalf("want exit 3, got %v", err)
	}
}

func TestAuthLoginNonTTYWithoutKeyStdinIsUsageError(t *testing.T) {
	keyring.MockInit()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "login"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v", err)
	}
}

func TestAuthSwitchAndLogout(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	t.Setenv("BRONTO_CONFIG_DIR", dir)
	if _, err := secrets.Store("stage", "k1"); err != nil {
		t.Fatal(err)
	}
	root := NewRootCmd()
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	root.SetArgs([]string{"auth", "switch", "stage"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(config.LoadOptions{Getenv: os.Getenv, WorkDir: t.TempDir(), UserConfigDir: dir})
	if cfg.Profile() != "stage" {
		t.Fatalf("profile = %q", cfg.Profile())
	}
	// unknown profile -> exit 4
	root2 := NewRootCmd()
	root2.SetOut(&bytes.Buffer{})
	root2.SetErr(&bytes.Buffer{})
	root2.SetArgs([]string{"auth", "switch", "ghost"})
	if err := root2.Execute(); clierr.ExitCode(err) != 4 {
		t.Fatalf("want 4, got %v", err)
	}
	// logout removes the key
	root3 := NewRootCmd()
	root3.SetOut(&bytes.Buffer{})
	root3.SetErr(&bytes.Buffer{})
	root3.SetArgs([]string{"auth", "logout", "--profile", "stage"})
	if err := root3.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := secrets.Get("stage"); err == nil {
		t.Fatal("key must be gone after logout")
	}
}

func TestAuthStatusJSON(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "status", "--api-key", "abcdefgh12345", "--base-url", srv.URL, "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("out = %q", out.String())
	}
	if rows[0]["status"] != "ok" || rows[0]["key"] != "abcdefgh…" || rows[0]["key_source"] != "flag" {
		t.Fatalf("row = %v", rows[0])
	}
}
```

(Imports: keyring, secrets, config, os as needed. Add BRONTO_API_KEY/BRONTO_PROFILE isolation via t.Setenv where host env could interfere.)

Run: `go test ./internal/cli -run TestAuth -v` — Expected: FAIL.

- [ ] **Step 2: Implement** `internal/cli/auth.go` per the Interfaces block. Details that matter:
- `var readPassword = term.ReadPassword` package seam; prompt text to stderr, newline after read.
- Region detect loop returns (region, baseURL) of the first success; when `--base-url` is set there is exactly one candidate.
- The validate call: build `http.NewRequestWithContext(ctx, GET, baseURL+"/logs", nil)` with `api.NewHTTPClient(key, version.Version)`; 2xx → ok; else map via `api.ErrorFromStatus` but only PROPAGATE after the last candidate fails (keep the last auth error).
- `auth status` row: `key_source` = Value.Source string; mask = first 8 runes + `…` (empty → `""` and status `no key`).
- Register `newAuthCmd()` AND `newLoginAliasCmd()` (Use: "login", Short: "Alias for auth login", RunE delegating) in root.go.

- [ ] **Step 3: Verify + commit**

```bash
go test ./internal/cli -run TestAuth -v && go test ./...
CGO_ENABLED=0 go build ./... && gofmt -l internal cmd
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/cli
git commit -m "feat: bronto auth login/status/switch/logout with keychain storage"
```

---

### Task 6: End-of-plan verification

- [ ] `go test ./...` all packages; `make build`; `gofmt -l` empty; lint 0 issues.
- [ ] Live smoke (binary, no network needed): `./bronto send --help`, `./bronto auth --help`, `echo x | ./bronto send -d t --api-key k --ingest-url http://127.0.0.1:9 2>&1; echo $?` → typed `network_error`, exit 1; `./bronto auth login </dev/null 2>&1; echo $?` → usage exit 2.
- [ ] Whole-branch review (controller dispatches), fix wave, merge.

## Verification (end of plan)

```bash
tail -f /var/log/system.log | ./bronto send -d laptop-logs --api-key <ingestion-key>   # manual
echo '{"message":"hi","level":"info"}' | ./bronto send -d test --api-key <key>
./bronto auth login   # interactive: hidden prompt, region detect, keychain store
./bronto auth status -o json
./bronto auth switch prod && ./bronto config list
```

Manual acceptance: send ships batches with gzip and prints a summary; structured stdin passes through; auth login round-trips a real key into the OS keychain and `bronto ping` works with no env vars set; `auth logout` removes it.
