# bronto-cli v2 — Plan 2: Search Family

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The daily-driver commands: `bronto search` (one-shot queries, streaming output), `bronto tail` (live follow with regex filters and colors), `bronto fields` (field discovery), `bronto context` (events around a log line) — plus time-range parsing and the output-engine fixes deferred from Plan 1's final review.

**Architecture:** Commands stay thin (Cobra layer) over a new service layer `internal/bronto` (search request/response model, tail session logic) which calls `POST /search`, `GET /top-keys`, `GET /context` through the existing transport (`App.HTTPClient`). New leaf package `internal/timerange` converts `--since/--from/--to` into Bronto's `time_range` or `from_ts`/`to_ts`.

**Tech Stack:** Existing stack only — no new dependencies (stdlib `regexp`, `os/signal` for Ctrl-C).

**API facts (verified against the Bronto spec and skill reference):**
- `POST /search` body params: `from` ([]string dataset UUIDs) or `from_expr` (string); `time_range` ("Last 15 minutes") XOR `from_ts`/`to_ts` (unix ms); `where`, `select` ([]string), `groups` ([]string), `limit` (≤10000), `num_of_slices`, `most_recent_first` (bool), `order_by`, `explain_only`.
- Response: `{explain, result: [...], events: [...], groups: [...], groups_series: [...], totals, pagination:{next_page_url}, metadata}` — event objects carry `@time`, `@sequence`, `@raw`, `@status`, `@origin` plus data fields; events may appear under `events` OR `result`.
- `GET /top-keys?time_range=&log_id=&limit=` — response list may appear under `top_keys`, `keys`, or `data`.
- `GET /context?sequence=&from=&timestamp=&direction=&limit=` — direction `before|after|both`; events under `events`/`result`/`data`.

## Global Constraints

(Same as Plan 1, restated where binding here.)
- Module `github.com/svrnm/bronto-cli`; Go directive `1.25.0`; `CGO_ENABLED=0 go build ./...` must succeed; gofmt-clean; golangci-lint (v2 config in repo) must report 0 issues before each commit (`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run` — binary is in the module cache, fast).
- NO new dependencies. stdlib only for new code.
- stdout = data only; stats/progress/messages → stderr; `--quiet` silences stderr chatter.
- Exit codes 0/1/2/3/4/5; error codes stable snake_case via `internal/clierr`.
- Machine formats (json/jsonl) are stable contracts; `jsonl` is the piped default for STREAMING commands (search event output, tail), `json` for the rest — `output.DetectFormat(flag, tty, streaming)` already implements this; pass `streaming` correctly.
- Every prompt-free command; no TTY-only functionality without a scriptable equivalent.
- Generated `internal/api/gen.go` and `api/openapi.yaml` must not be edited.
- Conventional commits.

**Existing interfaces you build on (do not re-implement):**
- `cli.App` / `cli.NewApp(cmd)`: `.Config` (`.APIKey() .BaseURL() .Get(key) (config.Value, bool)`), `.Stdout .Stderr`, `.HTTPClient *http.Client`, `.StdoutIsTTY bool`, `.OutputFlag string`, `.Quiet bool`, `.Color bool`, `.Printer(streaming bool) (*output.Printer, error)`. Package var `stdoutIsTTY func() bool` is the TTY test seam.
- `output.Printer`: `.PrintRows(columns []string, rows []map[string]any) error`, `.PrintRow(columns, row) error` (jsonl/raw), `.PrintJSON(v any) error`. `output.Format` constants; raw prints the `@raw` key.
- `api.ErrorFromStatus(status int, body []byte) *clierr.Error`; transport already adds auth header + retries.
- `clierr.New(code, msg).WithHint(h)`; `clierr.ExitCode(err)`.
- Root persistent flags already exist: `--api-key --profile --region --base-url -o/--output --no-color --quiet`.
- Config keys: `default_dataset` resolves through the precedence chain (`app.Config.Get("default_dataset")`).

---

### Task 1: Output-engine hardening + timeout wiring (deferred fixes)

**Files:**
- Modify: `internal/output/output.go`, `internal/output/output_test.go`
- Modify: `internal/config/config.go` (envKeys), `internal/cli/context.go` (timeout), `internal/cli/context_test.go` or `internal/cli/ping_test.go` (timeout test)

**Interfaces:**
- Produces: table/csv render missing/nil values as `""` (not `<nil>`); `PrintRows(FormatJSON)` with nil/empty rows emits `[]`; `PrintRow` on formats other than jsonl/raw returns `clierr.New("internal_output_misuse", ...)`; env var `BRONTO_TIMEOUT` (seconds) and config key `timeout` set `App.HTTPClient.Timeout`.
- Consumes: everything existing.

- [ ] **Step 1: Failing tests**

Append to `internal/output/output_test.go`:

```go
func TestMissingColumnValuesRenderEmpty(t *testing.T) {
	rows := []map[string]any{{"name": "web"}} // no "count" key
	var tbl, csvBuf bytes.Buffer
	if err := NewPrinter(&tbl, FormatTable).PrintRows([]string{"name", "count"}, rows); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tbl.String(), "<nil>") {
		t.Fatalf("table renders <nil>: %q", tbl.String())
	}
	if err := NewPrinter(&csvBuf, FormatCSV).PrintRows([]string{"name", "count"}, rows); err != nil {
		t.Fatal(err)
	}
	if csvBuf.String() != "name,count\nweb,\n" {
		t.Fatalf("csv = %q", csvBuf.String())
	}
}

func TestJSONEmptyRowsIsEmptyArray(t *testing.T) {
	var buf bytes.Buffer
	if err := NewPrinter(&buf, FormatJSON).PrintRows([]string{"a"}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "[]" {
		t.Fatalf("got %q, want []", buf.String())
	}
}

func TestPrintRowRejectsNonStreamingFormats(t *testing.T) {
	for _, f := range []Format{FormatTable, FormatJSON, FormatCSV} {
		if err := NewPrinter(&bytes.Buffer{}, f).PrintRow(nil, map[string]any{"x": 1}); err == nil {
			t.Errorf("PrintRow(%s) must error", f)
		}
	}
	for _, f := range []Format{FormatJSONL, FormatRaw} {
		if err := NewPrinter(&bytes.Buffer{}, f).PrintRow(nil, map[string]any{"@raw": "x"}); err != nil {
			t.Errorf("PrintRow(%s) errored: %v", f, err)
		}
	}
}
```

Timeout test (in `internal/cli/ping_test.go` style, new func):

```go
func TestTimeoutConfigAppliesToHTTPClient(t *testing.T) {
	t.Setenv("BRONTO_TIMEOUT", "5")
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	cmd := NewRootCmd()
	pingCmd, _, err := cmd.Find([]string{"ping"})
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(pingCmd)
	if err != nil {
		t.Fatal(err)
	}
	if app.HTTPClient.Timeout != 5*time.Second {
		t.Fatalf("timeout = %v, want 5s", app.HTTPClient.Timeout)
	}
}
```

Run: `go test ./internal/output ./internal/cli -run 'TestMissing|TestJSONEmpty|TestPrintRowRejects|TestTimeoutConfig' -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/output/output.go` changes:
- Add helper and use it in both table and CSV loops:

```go
func cell(row map[string]any, col string) string {
	v, ok := row[col]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
```

- In `PrintRows` FormatJSON branch, before encoding: `if rows == nil { rows = []map[string]any{} }`.
- Rewrite `PrintRow`:

```go
func (p *Printer) PrintRow(columns []string, row map[string]any) error {
	switch p.format {
	case FormatRaw:
		if raw, ok := row["@raw"]; ok {
			_, err := fmt.Fprintln(p.w, raw)
			return err
		}
		return json.NewEncoder(p.w).Encode(row)
	case FormatJSONL:
		return json.NewEncoder(p.w).Encode(row)
	default:
		return clierr.New("internal_output_misuse",
			fmt.Sprintf("PrintRow requires a streaming format, got %q", p.format))
	}
}
```

(Note: `PrintRows` jsonl/raw branch calls `PrintRow` — that stays correct.)

`internal/config/config.go`: add `"timeout": "BRONTO_TIMEOUT",` to `envKeys`.

`internal/cli/context.go` in `NewApp`, after building the App's HTTPClient:

```go
	if v, ok := cfg.Get("timeout"); ok {
		secs, err := strconv.Atoi(v.Val)
		if err != nil || secs <= 0 {
			return nil, clierr.New("config_invalid_timeout",
				fmt.Sprintf("timeout must be a positive integer (seconds), got %q", v.Val))
		}
		httpClient.Timeout = time.Duration(secs) * time.Second
	}
```

(Restructure so the client is built into a local `httpClient` before the App literal; add `strconv`, `time`, `fmt` imports.)

- [ ] **Step 3: Verify and commit**

```bash
go test ./... && CGO_ENABLED=0 go build ./... && gofmt -l internal cmd
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/output internal/config internal/cli
git commit -m "fix: output nil-safety, PrintRow guard, timeout config wiring"
```

---

### Task 2: Time-range parsing (`internal/timerange`)

**Files:**
- Create: `internal/timerange/timerange.go`, `internal/timerange/timerange_test.go`

**Interfaces:**
- Produces:
  - `type Spec struct { TimeRange string; FromTs, ToTs int64 }` — exactly one representation set; zero Spec = "caller applies default"
  - `(Spec).IsZero() bool`
  - `timerange.Resolve(since, from, to string, now func() time.Time) (Spec, error)`
  - Rules: `since` accepts `15m`, `1h`, `90s`, `2d`, `1w`, and compounds `1h30m`; single-unit → `TimeRange` (`"Last 15 minutes"`, singular unit name when n==1); compound → `FromTs=now-dur, ToTs=now` (unix ms). `from`/`to` are RFC3339; `from` alone → `to=now`; `to` alone → error `usage_invalid_time_flags`; `since`+(`from`|`to`) → error `usage_conflicting_time_flags`; unparseable → `usage_invalid_since` / `usage_invalid_time_flags`. All empty → zero Spec, nil error.
- Consumes: `clierr`.

- [ ] **Step 1: Failing tests**

`internal/timerange/timerange_test.go`:

```go
package timerange

import (
	"testing"
	"time"
)

var testNow = func() time.Time {
	return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
}

func TestSinceSingleUnit(t *testing.T) {
	cases := []struct{ in, want string }{
		{"15m", "Last 15 minutes"},
		{"1h", "Last 1 hour"},
		{"90s", "Last 90 seconds"},
		{"2d", "Last 2 days"},
		{"1w", "Last 1 week"},
	}
	for _, c := range cases {
		got, err := Resolve(c.in, "", "", testNow)
		if err != nil || got.TimeRange != c.want || got.FromTs != 0 {
			t.Errorf("Resolve(%q) = %+v, %v; want TimeRange=%q", c.in, got, err, c.want)
		}
	}
}

func TestSinceCompoundBecomesAbsolute(t *testing.T) {
	got, err := Resolve("1h30m", "", "", testNow)
	if err != nil {
		t.Fatal(err)
	}
	wantTo := testNow().UnixMilli()
	wantFrom := testNow().Add(-90 * time.Minute).UnixMilli()
	if got.TimeRange != "" || got.FromTs != wantFrom || got.ToTs != wantTo {
		t.Fatalf("got %+v, want from=%d to=%d", got, wantFrom, wantTo)
	}
}

func TestAbsoluteFromTo(t *testing.T) {
	got, err := Resolve("", "2026-07-07T10:00:00Z", "2026-07-07T11:00:00Z", testNow)
	if err != nil {
		t.Fatal(err)
	}
	from, _ := time.Parse(time.RFC3339, "2026-07-07T10:00:00Z")
	to, _ := time.Parse(time.RFC3339, "2026-07-07T11:00:00Z")
	if got.FromTs != from.UnixMilli() || got.ToTs != to.UnixMilli() || got.TimeRange != "" {
		t.Fatalf("got %+v", got)
	}
	// from alone -> to = now
	got2, err := Resolve("", "2026-07-07T10:00:00Z", "", testNow)
	if err != nil || got2.ToTs != testNow().UnixMilli() {
		t.Fatalf("from-alone: %+v, %v", got2, err)
	}
}

func TestNilNowDefaults(t *testing.T) {
	got, err := Resolve("1h30m", "", "", nil)
	if err != nil || got.ToTs == 0 {
		t.Fatalf("nil now must default to time.Now: %+v, %v", got, err)
	}
}

func TestErrors(t *testing.T) {
	if _, err := Resolve("15m", "2026-07-07T10:00:00Z", "", testNow); err == nil {
		t.Error("since+from must conflict")
	}
	if _, err := Resolve("", "", "2026-07-07T10:00:00Z", testNow); err == nil {
		t.Error("to alone must error")
	}
	for _, bad := range []string{"xyz", "m5", "5x", "", "h"} {
		if bad == "" {
			continue
		}
		if _, err := Resolve(bad, "", "", testNow); err == nil {
			t.Errorf("Resolve(%q) must error", bad)
		}
	}
	if _, err := Resolve("", "not-a-date", "", testNow); err == nil {
		t.Error("bad RFC3339 must error")
	}
}

func TestZeroSpec(t *testing.T) {
	got, err := Resolve("", "", "", testNow)
	if err != nil || !got.IsZero() {
		t.Fatalf("got %+v, %v; want zero", got, err)
	}
}
```

Run: `go test ./internal/timerange -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/timerange/timerange.go`:

```go
// Package timerange converts CLI time flags (--since / --from / --to) into
// the Bronto search API's time parameters: a relative time_range string
// ("Last 15 minutes") or absolute from_ts/to_ts unix-millisecond bounds.
// The API treats the two as mutually exclusive.
package timerange

import (
	"fmt"
	"regexp"
	"time"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

type Spec struct {
	TimeRange string
	FromTs    int64
	ToTs      int64
}

func (s Spec) IsZero() bool { return s.TimeRange == "" && s.FromTs == 0 && s.ToTs == 0 }

var tokenRe = regexp.MustCompile(`([0-9]+)([smhdw])`)

var unitDur = map[string]time.Duration{
	"s": time.Second, "m": time.Minute, "h": time.Hour,
	"d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
}

var unitName = map[string]string{
	"s": "second", "m": "minute", "h": "hour", "d": "day", "w": "week",
}

func Resolve(since, from, to string, now func() time.Time) (Spec, error) {
	if now == nil {
		now = time.Now
	}
	if since != "" && (from != "" || to != "") {
		return Spec{}, clierr.New("usage_conflicting_time_flags",
			"--since cannot be combined with --from/--to")
	}
	if since != "" {
		return resolveSince(since, now)
	}
	if to != "" && from == "" {
		return Spec{}, clierr.New("usage_invalid_time_flags",
			"--to requires --from").WithHint("Provide both bounds, or use --since for a relative range.")
	}
	if from != "" {
		fromT, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return Spec{}, clierr.New("usage_invalid_time_flags",
				fmt.Sprintf("--from is not RFC3339: %q", from))
		}
		toT := now()
		if to != "" {
			toT, err = time.Parse(time.RFC3339, to)
			if err != nil {
				return Spec{}, clierr.New("usage_invalid_time_flags",
					fmt.Sprintf("--to is not RFC3339: %q", to))
			}
		}
		return Spec{FromTs: fromT.UnixMilli(), ToTs: toT.UnixMilli()}, nil
	}
	return Spec{}, nil
}

func resolveSince(since string, now func() time.Time) (Spec, error) {
	tokens := tokenRe.FindAllStringSubmatch(since, -1)
	consumed := 0
	for _, tok := range tokens {
		consumed += len(tok[0])
	}
	if len(tokens) == 0 || consumed != len(since) {
		return Spec{}, clierr.New("usage_invalid_since",
			fmt.Sprintf("cannot parse --since %q", since)).
			WithHint("Use forms like 30s, 15m, 1h, 2d, 1w, or compounds like 1h30m.")
	}
	if len(tokens) == 1 {
		n := tokens[0][1]
		unit := unitName[tokens[0][2]]
		if n != "1" {
			unit += "s"
		}
		return Spec{TimeRange: fmt.Sprintf("Last %s %s", n, unit)}, nil
	}
	var total time.Duration
	for _, tok := range tokens {
		var n int64
		_, _ = fmt.Sscan(tok[1], &n)
		total += time.Duration(n) * unitDur[tok[2]]
	}
	end := now()
	return Spec{FromTs: end.Add(-total).UnixMilli(), ToTs: end.UnixMilli()}, nil
}
```

Run: `go test ./internal/timerange -v` — Expected: PASS.

- [ ] **Step 3: Commit**

```bash
gofmt -l internal && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/timerange
git commit -m "feat: time-range parsing for --since/--from/--to"
```

---

### Task 3: Search service layer (`internal/bronto`)

**Files:**
- Create: `internal/bronto/client.go`, `internal/bronto/client_test.go`
- Create: `internal/bronto/search.go`, `internal/bronto/search_test.go`

**Interfaces:**
- Produces:
  - `bronto.NewClient(h *http.Client, baseURL string) *Client`
  - `(*Client).Search(ctx context.Context, req SearchRequest) (*SearchResponse, error)` — POST /search, non-2xx via `api.ErrorFromStatus`
  - `(*Client).GetJSON(ctx context.Context, path string, params url.Values, out any) error` — GET with query params, same error mapping
  - `type SearchRequest struct { From []string; FromExpr string; Time timerange.Spec; Where string; Select []string; Groups []string; Limit int; Slices int; MostRecentFirst *bool; OrderBy string; ExplainOnly bool }`
  - `(SearchRequest).Body() map[string]any` — omits zero values; Time.TimeRange → `time_range`, else `from_ts`+`to_ts` when nonzero
  - `type SearchResponse struct { Explain map[string]any; Result, Events, Groups, GroupsSeries []map[string]any; Totals map[string]any; Pagination struct{ NextPageURL string } }` (JSON tags: `explain`, `result`, `events`, `groups`, `groups_series`, `totals`, `pagination`→`next_page_url`)
  - `(*SearchResponse).EventRows() []map[string]any` — `Events` if non-empty else `Result`
  - `bronto.Flatten(m map[string]any) map[string]any` — nested maps → dotted keys (`{"a":{"b":1}}` → `{"a.b":1}`)
  - `bronto.EventColumns(rows []map[string]any, max int) []string` — priority `@time`, `@status`, `@raw` first (when present), then first-seen order; `max<=0` = no cap
  - `(*SearchResponse).GroupRows() []map[string]any` — each group entry: if `group` is an object, merge its keys into the row; other keys (`count`, `stat`, `value`) kept
- Consumes: `api.ErrorFromStatus`, `timerange.Spec`.

- [ ] **Step 1: Failing tests**

`internal/bronto/search_test.go`:

```go
package bronto

import (
	"reflect"
	"testing"

	"github.com/svrnm/bronto-cli/internal/timerange"
)

func TestBodyOmitsZeroValues(t *testing.T) {
	b := SearchRequest{FromExpr: "log_id = 'x'", Where: "status >= 500", Limit: 100,
		Time: timerange.Spec{TimeRange: "Last 15 minutes"}}.Body()
	want := map[string]any{
		"from_expr": "log_id = 'x'", "where": "status >= 500",
		"limit": 100, "time_range": "Last 15 minutes",
	}
	if !reflect.DeepEqual(b, want) {
		t.Fatalf("got %v want %v", b, want)
	}
}

func TestBodyAbsoluteTimeAndFlags(t *testing.T) {
	mrf := false
	b := SearchRequest{From: []string{"id1"}, Time: timerange.Spec{FromTs: 1, ToTs: 2},
		Select: []string{"count()"}, Groups: []string{"host"}, Slices: 60,
		MostRecentFirst: &mrf, OrderBy: "x DESC", ExplainOnly: true}.Body()
	for k, want := range map[string]any{
		"from_ts": int64(1), "to_ts": int64(2), "num_of_slices": 60,
		"most_recent_first": false, "order_by": "x DESC", "explain_only": true,
	} {
		if got := b[k]; !reflect.DeepEqual(got, want) {
			t.Errorf("body[%q] = %v (%T), want %v", k, got, got, want)
		}
	}
	if _, has := b["time_range"]; has {
		t.Error("time_range must be absent with from_ts/to_ts")
	}
}

func TestEventRowsPrefersEvents(t *testing.T) {
	r := &SearchResponse{Events: []map[string]any{{"a": 1}}, Result: []map[string]any{{"b": 2}}}
	if r.EventRows()[0]["a"] != 1 {
		t.Fatal("must prefer events")
	}
	r2 := &SearchResponse{Result: []map[string]any{{"b": 2}}}
	if r2.EventRows()[0]["b"] != 2 {
		t.Fatal("must fall back to result")
	}
}

func TestFlatten(t *testing.T) {
	got := Flatten(map[string]any{"a": map[string]any{"b": 1, "c": map[string]any{"d": "x"}}, "e": 2})
	want := map[string]any{"a.b": 1, "a.c.d": "x", "e": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestEventColumnsPriorityAndCap(t *testing.T) {
	rows := []map[string]any{
		{"zebra": 1, "@raw": "x", "@time": "t"},
		{"alpha": 2, "@status": "info"},
	}
	got := EventColumns(rows, 0)
	if got[0] != "@time" || got[1] != "@status" || got[2] != "@raw" {
		t.Fatalf("priority order wrong: %v", got)
	}
	capped := EventColumns(rows, 4)
	if len(capped) != 4 {
		t.Fatalf("cap: %v", capped)
	}
}

func TestGroupRowsMergesGroupObject(t *testing.T) {
	r := &SearchResponse{Groups: []map[string]any{
		{"group": map[string]any{"host": "web-1"}, "count": float64(3)},
		{"group": "plain", "value": float64(1)},
	}}
	rows := r.GroupRows()
	if rows[0]["host"] != "web-1" || rows[0]["count"] != float64(3) {
		t.Fatalf("row0 = %v", rows[0])
	}
	if rows[1]["group"] != "plain" {
		t.Fatalf("row1 = %v", rows[1])
	}
}
```

`internal/bronto/client_test.go`:

```go
package bronto

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

func TestSearchPostsBodyAndParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/search" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil || body["where"] != "x" {
			t.Errorf("body = %s", b)
		}
		w.Write([]byte(`{"events":[{"@raw":"hello","@time":"t1"}],"explain":{"Execution time (millis)":"12"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.Client(), srv.URL)
	resp, err := c.Search(context.Background(), SearchRequest{Where: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Events) != 1 || resp.Events[0]["@raw"] != "hello" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestSearchMapsAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"message":"nope"}`))
	}))
	defer srv.Close()
	_, err := NewClient(srv.Client(), srv.URL).Search(context.Background(), SearchRequest{})
	if clierr.ExitCode(err) != 3 {
		t.Fatalf("exit = %d, want 3", clierr.ExitCode(err))
	}
}

func TestGetJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/top-keys" || r.URL.Query().Get("limit") != "5" {
			t.Errorf("got %s", r.URL)
		}
		w.Write([]byte(`{"top_keys":[{"key":"a"}]}`))
	}))
	defer srv.Close()
	var out map[string]any
	err := NewClient(srv.Client(), srv.URL).GetJSON(context.Background(), "/top-keys",
		url.Values{"limit": []string{"5"}}, &out)
	if err != nil || out["top_keys"] == nil {
		t.Fatalf("out=%v err=%v", out, err)
	}
}
```

Run: `go test ./internal/bronto -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/bronto/search.go`:

```go
// Package bronto is the service layer: request/response models and
// cross-endpoint workflows over the Bronto REST API (spec §4).
package bronto

import (
	"sort"

	"github.com/svrnm/bronto-cli/internal/timerange"
)

type SearchRequest struct {
	From            []string
	FromExpr        string
	Time            timerange.Spec
	Where           string
	Select          []string
	Groups          []string
	Limit           int
	Slices          int
	MostRecentFirst *bool
	OrderBy         string
	ExplainOnly     bool
}

func (r SearchRequest) Body() map[string]any {
	b := map[string]any{}
	if len(r.From) > 0 {
		b["from"] = r.From
	}
	if r.FromExpr != "" {
		b["from_expr"] = r.FromExpr
	}
	if r.Time.TimeRange != "" {
		b["time_range"] = r.Time.TimeRange
	} else if r.Time.FromTs != 0 || r.Time.ToTs != 0 {
		b["from_ts"] = r.Time.FromTs
		b["to_ts"] = r.Time.ToTs
	}
	if r.Where != "" {
		b["where"] = r.Where
	}
	if len(r.Select) > 0 {
		b["select"] = r.Select
	}
	if len(r.Groups) > 0 {
		b["groups"] = r.Groups
	}
	if r.Limit > 0 {
		b["limit"] = r.Limit
	}
	if r.Slices > 0 {
		b["num_of_slices"] = r.Slices
	}
	if r.MostRecentFirst != nil {
		b["most_recent_first"] = *r.MostRecentFirst
	}
	if r.OrderBy != "" {
		b["order_by"] = r.OrderBy
	}
	if r.ExplainOnly {
		b["explain_only"] = true
	}
	return b
}

type SearchResponse struct {
	Explain      map[string]any   `json:"explain"`
	Result       []map[string]any `json:"result"`
	Events       []map[string]any `json:"events"`
	Groups       []map[string]any `json:"groups"`
	GroupsSeries []map[string]any `json:"groups_series"`
	Totals       map[string]any   `json:"totals"`
	Pagination   struct {
		NextPageURL string `json:"next_page_url"`
	} `json:"pagination"`
}

func (r *SearchResponse) EventRows() []map[string]any {
	if len(r.Events) > 0 {
		return r.Events
	}
	return r.Result
}

func (r *SearchResponse) GroupRows() []map[string]any {
	rows := make([]map[string]any, 0, len(r.Groups))
	for _, g := range r.Groups {
		row := map[string]any{}
		for k, v := range g {
			if k == "group" {
				if obj, ok := v.(map[string]any); ok {
					for gk, gv := range obj {
						row[gk] = gv
					}
					continue
				}
			}
			row[k] = v
		}
		rows = append(rows, row)
	}
	return rows
}

// Flatten converts nested maps to dotted keys: {"a":{"b":1}} -> {"a.b":1}.
func Flatten(m map[string]any) map[string]any {
	out := map[string]any{}
	flattenInto(out, "", m)
	return out
}

func flattenInto(out map[string]any, prefix string, m map[string]any) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if nested, ok := v.(map[string]any); ok {
			flattenInto(out, key, nested)
			continue
		}
		out[key] = v
	}
}

var priorityColumns = []string{"@time", "@status", "@raw"}

func EventColumns(rows []map[string]any, max int) []string {
	seen := map[string]bool{}
	var discovered []string
	for _, r := range rows {
		keys := make([]string, 0, len(r))
		for k := range r {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic within a row
		for _, k := range keys {
			if !seen[k] {
				seen[k] = true
				discovered = append(discovered, k)
			}
		}
	}
	var cols []string
	for _, p := range priorityColumns {
		if seen[p] {
			cols = append(cols, p)
		}
	}
	for _, k := range discovered {
		isPriority := false
		for _, p := range priorityColumns {
			if k == p {
				isPriority = true
			}
		}
		if !isPriority {
			cols = append(cols, k)
		}
	}
	if max > 0 && len(cols) > max {
		cols = cols[:max]
	}
	return cols
}
```

`internal/bronto/client.go`:

```go
package bronto

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/svrnm/bronto-cli/internal/api"
)

type Client struct {
	http    *http.Client
	baseURL string
}

func NewClient(h *http.Client, baseURL string) *Client {
	return &Client{http: h, baseURL: baseURL}
}

func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	payload, err := json.Marshal(req.Body())
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/search",
		bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	var out SearchResponse
	if err := c.do(httpReq, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetJSON(ctx context.Context, path string, params url.Values, out any) error {
	u := c.baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if apiErr := api.ErrorFromStatus(resp.StatusCode, body); apiErr != nil {
		return apiErr
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}
```

Run: `go test ./internal/bronto -v` — Expected: PASS.

- [ ] **Step 3: Commit**

```bash
gofmt -l internal && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/bronto
git commit -m "feat: search service layer with request/response model"
```

---

### Task 4: `bronto search` command

**Files:**
- Create: `internal/cli/dataset.go` (shared dataset resolution), `internal/cli/search.go`, `internal/cli/search_test.go`
- Modify: `internal/cli/root.go` (register)

**Interfaces:**
- Produces:
  - `resolveDataset(app *App, datasets []string, fromExpr string) (ids []string, expr string, err error)` in dataset.go — flags win; else config `default_dataset` (value containing `=` or space → expr, else UUID id); neither → `clierr.New("usage_missing_dataset", ...)` with hint naming `--dataset`, `--from-expr`, and the `default_dataset` config key.
  - `newSearchCmd() *cobra.Command` — `bronto search [query]`:
    - positional query = the `where` expression; `-` reads it from stdin; omitted = no filter
    - flags: `--dataset/-d` (repeatable UUID), `--from-expr`, `--since`, `--from`, `--to`, `--select`, `--group-by/-g`, `--slices`, `--limit/-n` (default 100), `--order-by`, `--oldest-first` (bool; sets MostRecentFirst=false), `--explain-only`
    - default time when all time flags empty: `Last 15 minutes`
    - default select for event queries (no --select, no --group-by, not explain-only): `@time`, `@raw`
    - rendering: explain-only → `PrintJSON(resp.Explain)`; groups present → `PrintRows` of `GroupRows()` (streaming=false); otherwise events: rows = `Flatten` each of `EventRows()`; TTY table → columns via `EventColumns(rows, 8)`; jsonl/raw (piped default) → `PrintRow` per event
    - execution-time stat from `resp.Explain["Execution time (millis)"]` → stderr, only when TTY and not `--quiet`
- Consumes: `bronto.NewClient/SearchRequest/...` (Task 3), `timerange.Resolve` (Task 2), `App` (Plan 1).

- [ ] **Step 1: Failing tests**

`internal/cli/search_test.go`:

```go
package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

func searchServer(t *testing.T, respond string, capture *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if capture != nil {
			_ = json.Unmarshal(b, capture)
		}
		w.Write([]byte(respond))
	}))
}

func TestSearchEventsJSONLWhenPiped(t *testing.T) {
	var body map[string]any
	srv := searchServer(t, `{"events":[{"@raw":"e1","@time":"t1"},{"@raw":"e2","@time":"t2"}]}`, &body)
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "status >= 500", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 jsonl lines, got %d: %q", len(lines), out.String())
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil || ev["@raw"] != "e1" {
		t.Fatalf("line0 = %q", lines[0])
	}
	// request body assertions
	if body["where"] != "status >= 500" || body["time_range"] != "Last 15 minutes" {
		t.Fatalf("body = %v", body)
	}
	sel, _ := body["select"].([]any)
	if len(sel) != 2 || sel[0] != "@time" || sel[1] != "@raw" {
		t.Fatalf("default select = %v", sel)
	}
	from, _ := body["from"].([]any)
	if len(from) != 1 {
		t.Fatalf("from = %v", body["from"])
	}
}

func TestSearchGroupsRenderAsRows(t *testing.T) {
	srv := searchServer(t, `{"groups":[{"group":{"host":"web-1"},"count":3}]}`, nil)
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "-d", "11111111-1111-1111-1111-111111111111",
		"--select", "count()", "-g", "host",
		"--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || rows[0]["host"] != "web-1" {
		t.Fatalf("out = %q err=%v", out.String(), err)
	}
}

func TestSearchExplainOnly(t *testing.T) {
	srv := searchServer(t, `{"explain":{"Execution time (millis)":"7"}}`, nil)
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "--explain-only", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["Execution time (millis)"] != "7" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestSearchQueryFromStdin(t *testing.T) {
	var body map[string]any
	srv := searchServer(t, `{"events":[]}`, &body)
	defer srv.Close()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader("level = 'error'\n"))
	root.SetArgs([]string{"search", "-", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if body["where"] != "level = 'error'" {
		t.Fatalf("where = %v", body["where"])
	}
}

func TestSearchMissingDatasetIsUsageError(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "x", "--api-key", "k"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v (%d)", err, clierr.ExitCode(err))
	}
}
```

Run: `go test ./internal/cli -run TestSearch -v` — Expected: FAIL.

- [ ] **Step 2: Implement dataset.go**

`internal/cli/dataset.go`:

```go
package cli

import (
	"strings"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

// resolveDataset picks the dataset scope: explicit flags win, then the
// resolved default_dataset config value (an expression when it contains
// '=' or a space, a dataset UUID otherwise).
func resolveDataset(app *App, datasets []string, fromExpr string) ([]string, string, error) {
	if len(datasets) > 0 || fromExpr != "" {
		return datasets, fromExpr, nil
	}
	if v, ok := app.Config.Get("default_dataset"); ok && v.Val != "" {
		if strings.ContainsAny(v.Val, "= ") {
			return nil, v.Val, nil
		}
		return []string{v.Val}, "", nil
	}
	return nil, "", clierr.New("usage_missing_dataset", "no dataset selected").
		WithHint("Pass --dataset <uuid> or --from-expr \"...\", or set default_dataset via 'bronto config set default_dataset <uuid>'.")
}
```

- [ ] **Step 3: Implement search.go**

`internal/cli/search.go`:

```go
package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/bronto"
	"github.com/svrnm/bronto-cli/internal/output"
	"github.com/svrnm/bronto-cli/internal/timerange"
)

func newSearchCmd() *cobra.Command {
	var (
		datasets    []string
		fromExpr    string
		since, from, to string
		selects     []string
		groups      []string
		slices      int
		limit       int
		orderBy     string
		oldestFirst bool
		explainOnly bool
	)
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Run a one-shot query against Bronto",
		Long: "Runs a query (a Bronto WHERE expression) against one or more datasets.\n" +
			"Pass '-' as the query to read it from stdin.",
		Example: "  bronto search \"status >= 500\" --since 1h\n" +
			"  bronto search \"level = 'error'\" -d <dataset-uuid> --limit 50\n" +
			"  bronto search --select \"count()\" -g host --since 15m\n" +
			"  bronto search --explain-only --since 1d",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			where := ""
			if len(args) == 1 {
				where = args[0]
				if where == "-" {
					b, err := io.ReadAll(cmd.InOrStdin())
					if err != nil {
						return err
					}
					where = strings.TrimSpace(string(b))
				}
			}
			ids, expr, err := resolveDataset(app, datasets, fromExpr)
			if err != nil {
				return err
			}
			spec, err := timerange.Resolve(since, from, to, nil)
			if err != nil {
				return err
			}
			if spec.IsZero() {
				spec.TimeRange = "Last 15 minutes"
			}
			effSelect := selects
			if len(effSelect) == 0 && len(groups) == 0 && !explainOnly {
				effSelect = []string{"@time", "@raw"}
			}
			req := bronto.SearchRequest{
				From: ids, FromExpr: expr, Time: spec, Where: where,
				Select: effSelect, Groups: groups, Limit: limit, Slices: slices,
				OrderBy: orderBy, ExplainOnly: explainOnly,
			}
			if oldestFirst {
				mrf := false
				req.MostRecentFirst = &mrf
			}
			client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
			resp, err := client.Search(cmd.Context(), req)
			if err != nil {
				return err
			}
			if !app.Quiet && app.StdoutIsTTY {
				if ms, ok := resp.Explain["Execution time (millis)"]; ok {
					fmt.Fprintf(app.Stderr, "Execution time: %v ms\n", ms)
				}
			}
			switch {
			case explainOnly:
				p, err := app.Printer(false)
				if err != nil {
					return err
				}
				return p.PrintJSON(resp.Explain)
			case len(resp.Groups) > 0 || len(groups) > 0:
				p, err := app.Printer(false)
				if err != nil {
					return err
				}
				rows := resp.GroupRows()
				return p.PrintRows(bronto.EventColumns(rows, 0), rows)
			default:
				return printEvents(app, resp.EventRows())
			}
		},
	}
	f := cmd.Flags()
	f.StringArrayVarP(&datasets, "dataset", "d", nil, "dataset UUID to search (repeatable)")
	f.StringVar(&fromExpr, "from-expr", "", "dataset selector expression, e.g. \"log_id = '<uuid>'\"")
	f.StringVar(&since, "since", "", "relative lookback: 30s, 15m, 1h, 2d, 1w, 1h30m")
	f.StringVar(&from, "from", "", "absolute start (RFC3339)")
	f.StringVar(&to, "to", "", "absolute end (RFC3339), requires --from")
	f.StringArrayVar(&selects, "select", nil, "column or aggregate to select (repeatable)")
	f.StringArrayVarP(&groups, "group-by", "g", nil, "group-by key (repeatable)")
	f.IntVar(&slices, "slices", 0, "timeseries buckets for aggregate queries")
	f.IntVarP(&limit, "limit", "n", 100, "maximum events to return (1-10000)")
	f.StringVar(&orderBy, "order-by", "", "SQL-style order, e.g. 'duration_ms DESC'")
	f.BoolVar(&oldestFirst, "oldest-first", false, "return oldest events first")
	f.BoolVar(&explainOnly, "explain-only", false, "return only the query plan / cost estimate")
	return cmd
}

// printEvents renders event rows: streaming row-by-row for jsonl/raw,
// a capped-column table or full rows otherwise.
func printEvents(app *App, events []map[string]any) error {
	rows := make([]map[string]any, 0, len(events))
	for _, e := range events {
		rows = append(rows, bronto.Flatten(e))
	}
	p, err := app.Printer(true)
	if err != nil {
		return err
	}
	f, err := output.DetectFormat(app.OutputFlag, app.StdoutIsTTY, true)
	if err != nil {
		return err
	}
	if f == output.FormatJSONL || f == output.FormatRaw {
		for _, r := range rows {
			if err := p.PrintRow(nil, r); err != nil {
				return err
			}
		}
		return nil
	}
	max := 0
	if f == output.FormatTable {
		max = 8
	}
	return p.PrintRows(bronto.EventColumns(rows, max), rows)
}
```

Register in `NewRootCmd()`: `cmd.AddCommand(newSearchCmd())`.

- [ ] **Step 4: Verify and commit**

```bash
go test ./internal/cli -run TestSearch -v && go test ./...
CGO_ENABLED=0 go build ./... && gofmt -l internal cmd
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/cli internal/timerange
git commit -m "feat: bronto search with streaming output and dataset resolution"
```

---

### Task 5: `bronto fields` command

**Files:**
- Create: `internal/cli/fields.go`, `internal/cli/fields_test.go`
- Modify: `internal/cli/root.go` (register)

**Interfaces:**
- Produces: `newFieldsCmd() *cobra.Command` — `bronto fields`: flags `--dataset/-d` (single, optional — omitting queries all datasets), `--since` (default `1h`), `--limit/-n`. GET `/top-keys` with `time_range`, optional `log_id`, optional `limit`. Response normalization: rows from `top_keys` | `keys` | `data` array; else a flat object of numeric values becomes `{key, count}` rows. Non-dict entries become `{"value": entry}`.
- Consumes: `bronto.Client.GetJSON` (Task 3), `timerange` (Task 2), `resolveDataset` NOT used (fields allows no dataset = all datasets).

- [ ] **Step 1: Failing tests**

`internal/cli/fields_test.go`:

```go
package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFieldsListsTopKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/top-keys" {
			t.Errorf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("time_range") != "Last 1 hour" || q.Get("log_id") != "ds-1" || q.Get("limit") != "10" {
			t.Errorf("query = %v", q)
		}
		w.Write([]byte(`{"top_keys":[{"key":"status","count":42},{"key":"host","count":7}]}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "-d", "ds-1", "--since", "1h", "-n", "10",
		"--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 2 || rows[0]["key"] != "status" {
		t.Fatalf("out = %q err=%v", out.String(), err)
	}
}

func TestFieldsNormalizesNumericMap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":42,"host":7}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("out = %q", out.String())
	}
	for _, r := range rows {
		if r["key"] == "" || r["count"] == nil {
			t.Fatalf("row = %v", r)
		}
	}
}
```

Run: `go test ./internal/cli -run TestFields -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/cli/fields.go`:

```go
package cli

import (
	"net/url"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/bronto"
	"github.com/svrnm/bronto-cli/internal/clierr"
	"github.com/svrnm/bronto-cli/internal/timerange"
)

func newFieldsCmd() *cobra.Command {
	var dataset, since string
	var limit int
	cmd := &cobra.Command{
		Use:   "fields",
		Short: "Discover field names (top keys) in a dataset",
		Example: "  bronto fields -d <dataset-uuid> --since 1h\n" +
			"  bronto fields --since 15m -n 20",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			spec, err := timerange.Resolve(since, "", "", nil)
			if err != nil {
				return err
			}
			if spec.IsZero() {
				spec.TimeRange = "Last 1 hour"
			}
			if spec.TimeRange == "" { // compound --since resolved to absolute bounds
				return clierr.New("usage_invalid_since",
					"fields supports only single-unit --since values (e.g. 90m, 2h)").
					WithHint("The /top-keys endpoint accepts relative ranges only.")
			}
			params := url.Values{"time_range": []string{spec.TimeRange}}
			if dataset != "" {
				params.Set("log_id", dataset)
			}
			if limit > 0 {
				params.Set("limit", strconv.Itoa(limit))
			}
			var payload map[string]any
			client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
			if err := client.GetJSON(cmd.Context(), "/top-keys", params, &payload); err != nil {
				return err
			}
			rows := normalizeTopKeys(payload)
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows([]string{"key", "count"}, rows)
		},
	}
	cmd.Flags().StringVarP(&dataset, "dataset", "d", "", "dataset UUID (omit for all datasets)")
	cmd.Flags().StringVar(&since, "since", "1h", "relative lookback (single unit: 30s, 15m, 1h, 2d)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "maximum keys to return")
	return cmd
}

func normalizeTopKeys(payload map[string]any) []map[string]any {
	for _, field := range []string{"top_keys", "keys", "data"} {
		if list, ok := payload[field].([]any); ok {
			rows := make([]map[string]any, 0, len(list))
			for _, item := range list {
				if m, ok := item.(map[string]any); ok {
					rows = append(rows, m)
				} else {
					rows = append(rows, map[string]any{"value": item})
				}
			}
			return rows
		}
	}
	// flat {key: numericCount} object
	var rows []map[string]any
	for k, v := range payload {
		if n, ok := v.(float64); ok {
			rows = append(rows, map[string]any{"key": k, "count": n})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i]["count"].(float64) > rows[j]["count"].(float64)
	})
	return rows
}
```

(Imports for fields.go: `net/url`, `sort`, `strconv`, cobra, `bronto`, `clierr`, `timerange`.)

Register in `NewRootCmd()`: `cmd.AddCommand(newFieldsCmd())`.

Additional test for the compound-`--since` rejection:

```go
func TestFieldsRejectsCompoundSince(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "--since", "1h30m", "--api-key", "k"})
	if err := root.Execute(); err == nil {
		t.Fatal("compound since must error")
	}
}
```

- [ ] **Step 3: Verify and commit**

```bash
go test ./internal/cli -run TestFields -v && go test ./...
gofmt -l internal cmd && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/cli
git commit -m "feat: bronto fields for dataset field discovery"
```

---

### Task 6: `bronto context` command

**Files:**
- Create: `internal/cli/contextcmd.go`, `internal/cli/contextcmd_test.go`
- Modify: `internal/cli/root.go` (register)

**Interfaces:**
- Produces: `newContextCmd() *cobra.Command` — `bronto context`: required flags `--sequence` (int64), `--dataset/-d` (string), `--timestamp` (int64, unix ms); optional `--direction` (`before|after|both`, default `both`; anything else → `usage_invalid_direction`), `--limit/-n` (default 50). GET `/context` with params `sequence`, `from`, `timestamp`, `direction`, `limit`. Events found under `events` | `result` | `data`; rendered via the same `printEvents(app, events)` helper as search (streaming).
- Consumes: `bronto.Client.GetJSON` (Task 3), `printEvents` (Task 4).

- [ ] **Step 1: Failing tests**

`internal/cli/contextcmd_test.go`:

```go
package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

func TestContextFetchesAndStreams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/context" {
			t.Errorf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("sequence") != "12345" || q.Get("from") != "ds-1" ||
			q.Get("timestamp") != "1700000000000" || q.Get("direction") != "both" || q.Get("limit") != "20" {
			t.Errorf("query = %v", q)
		}
		w.Write([]byte(`{"events":[{"@raw":"before-line"},{"@raw":"anchor"},{"@raw":"after-line"}]}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"context", "--sequence", "12345", "-d", "ds-1",
		"--timestamp", "1700000000000", "-n", "20",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 jsonl lines, got %q", out.String())
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &ev); err != nil || ev["@raw"] != "anchor" {
		t.Fatalf("line1 = %q", lines[1])
	}
}

func TestContextRejectsBadDirection(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"context", "--sequence", "1", "-d", "x", "--timestamp", "1",
		"--direction", "sideways", "--api-key", "k"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v", err)
	}
}
```

Run: `go test ./internal/cli -run TestContext -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/cli/contextcmd.go`:

```go
package cli

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/bronto"
	"github.com/svrnm/bronto-cli/internal/clierr"
)

func newContextCmd() *cobra.Command {
	var sequence, timestamp int64
	var dataset, direction string
	var limit int
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Show events around a specific log event",
		Example: "  bronto context --sequence 111721913 -d <dataset-uuid> --timestamp 1711535140632\n" +
			"  bronto context --sequence 42 -d <dataset-uuid> --timestamp 1711535140632 --direction before",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch direction {
			case "before", "after", "both":
			default:
				return clierr.New("usage_invalid_direction",
					fmt.Sprintf("direction must be before, after, or both; got %q", direction))
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			params := url.Values{
				"sequence":  []string{strconv.FormatInt(sequence, 10)},
				"from":      []string{dataset},
				"timestamp": []string{strconv.FormatInt(timestamp, 10)},
				"direction": []string{direction},
				"limit":     []string{strconv.Itoa(limit)},
			}
			var payload map[string]any
			client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
			if err := client.GetJSON(cmd.Context(), "/context", params, &payload); err != nil {
				return err
			}
			var events []map[string]any
			for _, field := range []string{"events", "result", "data"} {
				if list, ok := payload[field].([]any); ok {
					for _, item := range list {
						if m, ok := item.(map[string]any); ok {
							events = append(events, m)
						}
					}
					break
				}
			}
			return printEvents(app, events)
		},
	}
	f := cmd.Flags()
	f.Int64Var(&sequence, "sequence", 0, "sequence number of the anchor event (required)")
	f.StringVarP(&dataset, "dataset", "d", "", "dataset UUID the event belongs to (required)")
	f.Int64Var(&timestamp, "timestamp", 0, "unix-ms timestamp of the anchor event (required)")
	f.StringVar(&direction, "direction", "both", "before | after | both")
	f.IntVarP(&limit, "limit", "n", 50, "events per direction")
	_ = cmd.MarkFlagRequired("sequence")
	_ = cmd.MarkFlagRequired("dataset")
	_ = cmd.MarkFlagRequired("timestamp")
	return cmd
}
```

Note: cobra's missing-required-flag error is a flag-stage error routed through `SetFlagErrorFunc`? It is NOT — required-flag errors surface from `Execute` as plain errors. Check behavior in the test run; if `bronto context` (no flags) exits 1, extend the `wrapArgsValidators`/flag-error path in `internal/cli/root.go` to also wrap errors whose text starts with `required flag(s)` into `clierr.New("usage_missing_flag", ...)`, with a unit test pinning exit 2. Implement this in this task if observed.

Register in `NewRootCmd()`: `cmd.AddCommand(newContextCmd())`.

- [ ] **Step 3: Verify and commit**

```bash
go test ./internal/cli -run TestContext -v && go test ./...
gofmt -l internal cmd && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/cli
git commit -m "feat: bronto context for events around a log line"
```

---

### Task 7: Tail session logic (`internal/bronto/tail.go`)

**Files:**
- Create: `internal/bronto/tail.go`, `internal/bronto/tail_test.go`

**Interfaces:**
- Produces:
  - `type TailFilter struct { Include, Exclude []*regexp.Regexp }`; `(TailFilter).Match(raw string) bool` — all Include must match (empty = pass), any Exclude match rejects
  - `bronto.NewDedup(capacity int) *Dedup`; `(*Dedup).Key(ev map[string]any) string` — `@sequence` as string if present, else `@time|@raw`; `(*Dedup).Admit(key string) bool` — false if seen; at capacity, evicts the oldest half (insertion order)
  - `bronto.SortEvents(evs []map[string]any)` — stable sort by numeric `@sequence` when both present, else by `@time` string
- Consumes: nothing new.

- [ ] **Step 1: Failing tests**

`internal/bronto/tail_test.go`:

```go
package bronto

import (
	"fmt"
	"regexp"
	"testing"
)

func TestTailFilter(t *testing.T) {
	f := TailFilter{
		Include: []*regexp.Regexp{regexp.MustCompile(`error`)},
		Exclude: []*regexp.Regexp{regexp.MustCompile(`healthz`)},
	}
	cases := []struct {
		raw  string
		want bool
	}{
		{"an error occurred", true},
		{"error in healthz probe", false},
		{"all fine", false},
	}
	for _, c := range cases {
		if got := f.Match(c.raw); got != c.want {
			t.Errorf("Match(%q) = %v", c.raw, got)
		}
	}
	if !(TailFilter{}).Match("anything") {
		t.Error("empty filter must pass everything")
	}
}

func TestDedupAdmitOnce(t *testing.T) {
	d := NewDedup(100)
	ev := map[string]any{"@sequence": float64(42), "@raw": "x"}
	k := d.Key(ev)
	if !d.Admit(k) {
		t.Fatal("first admit must succeed")
	}
	if d.Admit(k) {
		t.Fatal("second admit must fail")
	}
	// fallback key without sequence
	k2 := d.Key(map[string]any{"@time": "t1", "@raw": "y"})
	if k2 == "" || k2 == k {
		t.Fatalf("fallback key = %q", k2)
	}
}

func TestDedupEvictsOldestHalfAtCapacity(t *testing.T) {
	d := NewDedup(4)
	for i := 0; i < 4; i++ {
		d.Admit(fmt.Sprint(i))
	}
	d.Admit("4") // triggers eviction of "0","1"
	if !d.Admit("0") {
		t.Error("evicted key 0 must be admittable again")
	}
	if d.Admit("3") {
		t.Error("key 3 must still be remembered")
	}
}

func TestSortEventsBySequenceThenTime(t *testing.T) {
	evs := []map[string]any{
		{"@sequence": float64(3), "@raw": "c"},
		{"@sequence": float64(1), "@raw": "a"},
		{"@sequence": float64(2), "@raw": "b"},
	}
	SortEvents(evs)
	if evs[0]["@raw"] != "a" || evs[2]["@raw"] != "c" {
		t.Fatalf("sorted = %v", evs)
	}
	byTime := []map[string]any{
		{"@time": "2026-07-07T12:00:02Z"},
		{"@time": "2026-07-07T12:00:01Z"},
	}
	SortEvents(byTime)
	if byTime[0]["@time"] != "2026-07-07T12:00:01Z" {
		t.Fatalf("time sort = %v", byTime)
	}
}
```

Run: `go test ./internal/bronto -run 'TestTail|TestDedup|TestSortEvents' -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/bronto/tail.go`:

```go
package bronto

import (
	"fmt"
	"regexp"
	"sort"
)

// TailFilter applies client-side include/exclude regexes to raw lines.
type TailFilter struct {
	Include []*regexp.Regexp
	Exclude []*regexp.Regexp
}

func (f TailFilter) Match(raw string) bool {
	for _, re := range f.Include {
		if !re.MatchString(raw) {
			return false
		}
	}
	for _, re := range f.Exclude {
		if re.MatchString(raw) {
			return false
		}
	}
	return true
}

// Dedup remembers event keys across poll cycles with bounded memory:
// at capacity the oldest half (by insertion order) is evicted.
type Dedup struct {
	seen     map[string]struct{}
	order    []string
	capacity int
}

func NewDedup(capacity int) *Dedup {
	return &Dedup{seen: map[string]struct{}{}, capacity: capacity}
}

func (d *Dedup) Key(ev map[string]any) string {
	if seq, ok := ev["@sequence"]; ok {
		return fmt.Sprint(seq)
	}
	return fmt.Sprint(ev["@time"], "|", ev["@raw"])
}

func (d *Dedup) Admit(key string) bool {
	if _, dup := d.seen[key]; dup {
		return false
	}
	if len(d.order) >= d.capacity {
		half := len(d.order) / 2
		for _, old := range d.order[:half] {
			delete(d.seen, old)
		}
		d.order = append([]string(nil), d.order[half:]...)
	}
	d.seen[key] = struct{}{}
	d.order = append(d.order, key)
	return true
}

// SortEvents orders a poll batch: numeric @sequence when present on both
// events, @time string otherwise.
func SortEvents(evs []map[string]any) {
	sort.SliceStable(evs, func(i, j int) bool {
		si, iok := numeric(evs[i]["@sequence"])
		sj, jok := numeric(evs[j]["@sequence"])
		if iok && jok {
			return si < sj
		}
		return fmt.Sprint(evs[i]["@time"]) < fmt.Sprint(evs[j]["@time"])
	})
}

func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}
```

Run: `go test ./internal/bronto -v` — Expected: PASS.

- [ ] **Step 3: Commit**

```bash
gofmt -l internal && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/bronto
git commit -m "feat: tail session primitives (filter, dedup, ordering)"
```

---

### Task 8: `bronto tail` command + signal handling

**Files:**
- Create: `internal/cli/tail.go`, `internal/cli/tail_test.go`
- Modify: `cmd/bronto/main.go` (signal-aware context), `internal/cli/root.go` (register)

**Interfaces:**
- Produces:
  - `main.go`: root executed via `ExecuteContext` with `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` — Ctrl-C cancels tail cleanly (return nil → exit 0)
  - `newTailCmd() *cobra.Command` — `bronto tail [query]`:
    - flags: `--dataset/-d` (repeatable), `--from-expr`, `--interval` (duration, default `3s`, min `1s` → else `usage_invalid_interval`), `--window` (single-unit since-style, default `30s` — the per-poll lookback), `--limit/-n` (per poll, default 500), `--include` / `--exclude` (repeatable regex; invalid → `usage_invalid_regex`), `--highlight` (repeatable regex), `--no-follow` (single poll then exit)
    - per poll: `SearchRequest{From/FromExpr, Time: {TimeRange: window}, Where: query, Select: ["@time","@raw","@sequence","@origin"], Limit, MostRecentFirst: &false}`; dedup via `bronto.Dedup` (capacity 20000); admit-filtered events sorted via `SortEvents`, filtered via `TailFilter` on `@raw`
    - human TTY output: one line per event — dim timestamp, `@origin` colored by stable hash (6-color ANSI palette 31–36), raw line with `--highlight` matches wrapped in bold yellow (`\x1b[1;33m...\x1b[0m`); colors only when `app.Color`
    - piped: `PrintRow` (jsonl default; raw honors `-o raw`)
    - startup notice → stderr (suppressed by `--quiet`): `Tailing every 3s (window 30s). Ctrl-C to stop.`
    - loop: poll → render → context-aware sleep (`select` on `cmd.Context().Done()` vs `time.After(interval)`); ctx cancellation → return nil
- Consumes: everything above.

- [ ] **Step 1: Failing tests**

`internal/cli/tail_test.go`:

```go
package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"context"
)

func TestTailNoFollowSinglePollDedupSorted(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"events":[
			{"@sequence":2,"@raw":"second","@time":"t2"},
			{"@sequence":1,"@raw":"first","@time":"t1"},
			{"@sequence":1,"@raw":"first","@time":"t1"}
		]}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"tail", "--no-follow", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("polls = %d, want 1", calls.Load())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 deduped lines, got %q", out.String())
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil || first["@raw"] != "first" {
		t.Fatalf("ordering wrong: %q", lines[0])
	}
}

func TestTailIncludeExcludeFilters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"events":[
			{"@sequence":1,"@raw":"error in api"},
			{"@sequence":2,"@raw":"error in healthz"},
			{"@sequence":3,"@raw":"all good"}
		]}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"tail", "--no-follow", "--include", "error", "--exclude", "healthz",
		"-d", "11111111-1111-1111-1111-111111111111", "--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); strings.Count(got, "\n") != 0 || !strings.Contains(got, "error in api") {
		t.Fatalf("filtered output = %q", got)
	}
}

func TestTailInvalidRegexIsUsageError(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"tail", "--no-follow", "--include", "([", "-d", "x", "--api-key", "k"})
	if err := root.Execute(); err == nil {
		t.Fatal("invalid regex must error")
	}
}

func TestTailFollowStopsOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"events":[]}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"tail", "-d", "11111111-1111-1111-1111-111111111111",
		"--interval", "1s", "--base-url", srv.URL, "--api-key", "k"})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled tail must exit clean: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tail did not stop on context cancellation")
	}
}
```

Run: `go test ./internal/cli -run TestTail -v` — Expected: FAIL.

- [ ] **Step 2: Implement main.go signal context**

In `cmd/bronto/main.go` `run()`:

```go
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cmd.ExecuteContext(ctx); err != nil {
```

(add imports `context`, `os/signal`, `syscall`.)

- [ ] **Step 3: Implement tail.go**

`internal/cli/tail.go`:

```go
package cli

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/bronto"
	"github.com/svrnm/bronto-cli/internal/clierr"
	"github.com/svrnm/bronto-cli/internal/output"
	"github.com/svrnm/bronto-cli/internal/timerange"
)

func newTailCmd() *cobra.Command {
	var (
		datasets           []string
		fromExpr           string
		interval           time.Duration
		window             string
		limit              int
		includes, excludes []string
		highlights         []string
		noFollow           bool
	)
	cmd := &cobra.Command{
		Use:   "tail [query]",
		Short: "Follow new events live (like tail -f)",
		Example: "  bronto tail\n" +
			"  bronto tail \"level = 'error'\" --include 'timeout' --exclude 'healthz'\n" +
			"  bronto tail --no-follow --window 5m   # catch up, then exit",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if interval < time.Second {
				return clierr.New("usage_invalid_interval", "interval must be at least 1s")
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			ids, expr, err := resolveDataset(app, datasets, fromExpr)
			if err != nil {
				return err
			}
			spec, err := timerange.Resolve(window, "", "", nil)
			if err != nil {
				return err
			}
			if spec.TimeRange == "" {
				return clierr.New("usage_invalid_since", "window must be a single-unit duration (e.g. 30s, 5m)")
			}
			filter, err := buildFilter(includes, excludes)
			if err != nil {
				return err
			}
			hlRes, err := compileRegexps(highlights)
			if err != nil {
				return err
			}
			where := ""
			if len(args) == 1 {
				where = args[0]
			}

			format, err := output.DetectFormat(app.OutputFlag, app.StdoutIsTTY, true)
			if err != nil {
				return err
			}
			p := output.NewPrinter(app.Stdout, format)
			humanMode := format == output.FormatTable // TTY default
			if !app.Quiet {
				fmt.Fprintf(app.Stderr, "Tailing every %s (window %s). Ctrl-C to stop.\n", interval, window)
			}

			mrf := false
			req := bronto.SearchRequest{
				From: ids, FromExpr: expr, Time: spec, Where: where,
				Select: []string{"@time", "@raw", "@sequence", "@origin"},
				Limit:  limit, MostRecentFirst: &mrf,
			}
			client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
			dedup := bronto.NewDedup(20000)

			for {
				resp, err := client.Search(cmd.Context(), req)
				if err != nil {
					if cmd.Context().Err() != nil {
						return nil // cancelled mid-request: clean exit
					}
					return err
				}
				batch := resp.EventRows()
				fresh := batch[:0:0]
				for _, ev := range batch {
					if dedup.Admit(dedup.Key(ev)) {
						fresh = append(fresh, ev)
					}
				}
				bronto.SortEvents(fresh)
				for _, ev := range fresh {
					raw := fmt.Sprint(ev["@raw"])
					if !filter.Match(raw) {
						continue
					}
					if humanMode {
						fmt.Fprintln(app.Stdout, renderTailLine(ev, raw, hlRes, app.Color))
						continue
					}
					if err := p.PrintRow(nil, bronto.Flatten(ev)); err != nil {
						return err
					}
				}
				if noFollow {
					return nil
				}
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(interval):
				}
			}
		},
	}
	f := cmd.Flags()
	f.StringArrayVarP(&datasets, "dataset", "d", nil, "dataset UUID to tail (repeatable)")
	f.StringVar(&fromExpr, "from-expr", "", "dataset selector expression")
	f.DurationVar(&interval, "interval", 3*time.Second, "polling interval (min 1s)")
	f.StringVar(&window, "window", "30s", "per-poll lookback window (single unit)")
	f.IntVarP(&limit, "limit", "n", 500, "max events per poll")
	f.StringArrayVar(&includes, "include", nil, "only show lines matching this regex (repeatable, ANDed)")
	f.StringArrayVar(&excludes, "exclude", nil, "hide lines matching this regex (repeatable)")
	f.StringArrayVar(&highlights, "highlight", nil, "highlight regex matches in the output (repeatable)")
	f.BoolVar(&noFollow, "no-follow", false, "fetch the current window once, then exit")
	return cmd
}

func compileRegexps(patterns []string) ([]*regexp.Regexp, error) {
	res := make([]*regexp.Regexp, 0, len(patterns))
	for _, pat := range patterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, clierr.New("usage_invalid_regex", fmt.Sprintf("invalid regex %q: %v", pat, err))
		}
		res = append(res, re)
	}
	return res, nil
}

func buildFilter(includes, excludes []string) (bronto.TailFilter, error) {
	inc, err := compileRegexps(includes)
	if err != nil {
		return bronto.TailFilter{}, err
	}
	exc, err := compileRegexps(excludes)
	if err != nil {
		return bronto.TailFilter{}, err
	}
	return bronto.TailFilter{Include: inc, Exclude: exc}, nil
}

var originColors = []string{"31", "32", "33", "34", "35", "36"}

func renderTailLine(ev map[string]any, raw string, highlights []*regexp.Regexp, color bool) string {
	ts := fmt.Sprint(ev["@time"])
	origin := ""
	if o, ok := ev["@origin"]; ok && o != nil {
		origin = fmt.Sprint(o)
	}
	if color {
		for _, re := range highlights {
			raw = re.ReplaceAllString(raw, "\x1b[1;33m$0\x1b[0m")
		}
		line := "\x1b[2m" + ts + "\x1b[0m "
		if origin != "" {
			h := fnv.New32a()
			_, _ = h.Write([]byte(origin))
			c := originColors[h.Sum32()%uint32(len(originColors))]
			line += "\x1b[" + c + "m" + origin + "\x1b[0m "
		}
		return line + raw
	}
	if origin != "" {
		return ts + " " + origin + " " + raw
	}
	return ts + " " + raw
}
```

Register in `NewRootCmd()`: `cmd.AddCommand(newTailCmd())`.

Known limitation to note in the command's Long help: out-of-order events arriving later than one window are not re-ordered across polls (per-batch ordering only); a cross-poll reorder buffer is future work.

- [ ] **Step 4: Verify and commit**

```bash
go test ./internal/cli -run TestTail -v && go test ./...
CGO_ENABLED=0 go build ./... && gofmt -l internal cmd
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/cli cmd/bronto
git commit -m "feat: bronto tail with live follow, filters, colors, clean Ctrl-C"
```

---

## Verification (end of plan)

```bash
go test ./...                          # all green
CGO_ENABLED=0 make build
./bronto search --help                 # examples-first help
./bronto tail --help
echo "level = 'error'" | ./bronto search - --explain-only --since 1h   # against real API (manual, needs key + dataset)
BRONTO_API_KEY=<key> ./bronto search "status >= 500" -d <uuid> --since 15m -o raw | head
BRONTO_API_KEY=<key> ./bronto tail -d <uuid> --no-follow
BRONTO_API_KEY=<key> ./bronto fields -d <uuid>
```

Manual acceptance: search streams JSONL when piped and renders a capped table on a TTY; tail follows live, Ctrl-C exits 0 cleanly; `--include/--exclude` filter; `fields` lists keys; `context` returns neighborhood events.
