# bronto-cli v2 — Plan 3: Traces

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The CLI-native trace explorer: `bronto traces services|operations|aggregate|list|show|shape` — APM-style aggregations, span search, an ASCII waterfall for a single trace, and the aggregated "shape" waterfall across many traces.

**Architecture:** New leaf package `internal/traces` holds the span model, query builders, aggregation, tree/waterfall, and shape algorithms — all pure and unit-testable. It calls the API only through the existing `internal/bronto.Client`. The Cobra layer (`internal/cli/traces.go`) stays thin.

**Tech Stack:** Existing stack, no new dependencies.

**Canonical reference:** `docs/superpowers/specs/2026-07-07-v1-traces-extraction.md` — a field-exact extraction of v1's 1179-line `traces.py`. This plan embeds everything needed, but when in doubt about a literal string or formula, that document is authoritative. Cite it as "extraction §N".

## Global Constraints

- Module `github.com/bronto-community/bronto-cli`; Go `1.25.0`; `CGO_ENABLED=0 go build ./...`; gofmt clean; golangci-lint 0 issues before each commit (`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run`).
- NO new dependencies.
- stdout = data only; stats/hints → stderr (TTY-gated where noted); `--quiet` silences stderr chatter.
- Exit codes 0/1/2/3/4/5; error codes stable snake_case via `internal/clierr`.
- Time flags follow Plan 2 conventions: `--since` (timerange package), NOT v1's `--time-range`. Defaults: services/operations/aggregate/list → `15m`; show/shape → `1h`.
- All searches target `from_expr = "logset = '.traces'"` (extraction §0).
- Span-field literals, duration formatting, and status/kind semantics MUST match extraction §1 exactly — they are wire-format contracts.
- Machine output (piped / `-o json`): structured rows via the output engine — a deliberate v2 change from v1's raw-payload dumps. TTY: custom waterfall rendering for show/shape, tables for the rest.
- Conventional commits.

**Existing interfaces (do not re-implement):** `bronto.NewClient(h, baseURL)`, `(*bronto.Client).Search(ctx, bronto.SearchRequest) (*bronto.SearchResponse, error)` (fields: `From, FromExpr, Time timerange.Spec, Where, Select, Groups, Limit, Slices, MostRecentFirst *bool, OrderBy, ExplainOnly`; response: `Result/Events/Groups/GroupsSeries []map[string]any`, `Totals map[string]any`); `timerange.Resolve(since, from, to, nil)`; `cli.App/NewApp` (`.Config .Stdout .Stderr .HTTPClient .StdoutIsTTY .OutputFlag .Quiet .Color`, `.Printer(streaming bool)`); `output.DetectFormat/NewPrinter/Format*`; `clierr.New(...).WithHint(...)`.

---

### Task 1: Span model, formatting, query builders (`internal/traces`)

**Files:**
- Create: `internal/traces/span.go`, `internal/traces/span_test.go`
- Create: `internal/traces/query.go`, `internal/traces/query_test.go`

**Interfaces:**
- Produces:
  - `traces.FromExpr = "logset = '.traces'"` (const)
  - `traces.SpanFields = []string{...}` (the 11 fields, extraction §1.1)
  - `type Span struct { TraceID, SpanID, ParentSpanID, Name, Kind, Service, Status string; StartNS, EndNS, DurationNS int64 }`
  - `traces.RowToSpan(row map[string]any) Span` — tolerant coercion + backfills (extraction §1.2)
  - `(Span).IsError() bool` — `strings.HasSuffix(strings.ToUpper(status), "ERROR")`
  - `traces.FormatDurationNS(ns int64) string` — `—` / `X.Yµs` / `X.YZms` / `X.YZs` (extraction §1.3)
  - `traces.RootOnlyClause = "NOT EXISTS $span.parent_span_id"` (const)
  - `traces.NormalizeAttr(attr string) (string, error)` — trim, error on empty (`usage_invalid_attr`), prefix `$` unless present
  - `traces.KindClause(kind string) string` — upper-case, prefix `SPAN_KIND_` unless present, wrap as `$span.kind = '...'`
  - `traces.AndJoin(clauses ...string) string` — skips empty, joins with ` AND `
  - `traces.Quote(v string) string` — single-quote a literal, doubling embedded `'`
- Consumes: `clierr`.

- [ ] **Step 1: Failing tests**

`internal/traces/span_test.go`:

```go
package traces

import "testing"

func TestRowToSpanCoercionAndBackfills(t *testing.T) {
	row := map[string]any{
		"$span.trace_id": "t1", "$span.span_id": "s1", "$span.parent_span_id": nil,
		"$span.name": "GET /x", "$span.kind": "SPAN_KIND_SERVER",
		"$span.duration_nano": "0", "$span.start_time_unix_nano": float64(100),
		"$span.end_time_unix_nano": "250.0",
		"$span.status_code": "STATUS_CODE_ERROR", "$service.name": "cart",
	}
	s := RowToSpan(row)
	if s.TraceID != "t1" || s.ParentSpanID != "" || s.Kind != "SERVER" || s.Service != "cart" {
		t.Fatalf("basic fields: %+v", s)
	}
	if s.DurationNS != 150 { // backfill: end-start when duration==0
		t.Fatalf("duration backfill = %d, want 150", s.DurationNS)
	}
	if !s.IsError() {
		t.Fatal("STATUS_CODE_ERROR must be an error")
	}

	// end backfill: end==0, start+duration known
	row2 := map[string]any{"$span.start_time_unix_nano": float64(100),
		"$span.duration_nano": float64(50), "$span.end_time_unix_nano": float64(0),
		"$span.status_code": "STATUS_CODE_UNSET"}
	s2 := RowToSpan(row2)
	if s2.EndNS != 150 {
		t.Fatalf("end backfill = %d, want 150", s2.EndNS)
	}
	if s2.IsError() {
		t.Fatal("UNSET is not an error")
	}
}

func TestFormatDurationNS(t *testing.T) {
	cases := []struct {
		ns   int64
		want string
	}{
		{0, "—"}, {-5, "—"},
		{500, "0.5µs"},
		{999_999, "1000.0µs"},
		{1_000_000, "1.00ms"},
		{9_820_000, "9.82ms"},
		{999_000_000, "999.00ms"},
		{1_000_000_000, "1.00s"},
		{83_500_000_000, "83.50s"},
	}
	for _, c := range cases {
		if got := FormatDurationNS(c.ns); got != c.want {
			t.Errorf("FormatDurationNS(%d) = %q, want %q", c.ns, got, c.want)
		}
	}
}
```

`internal/traces/query_test.go`:

```go
package traces

import "testing"

func TestNormalizeAttr(t *testing.T) {
	if got, _ := NormalizeAttr(" http.route "); got != "$http.route" {
		t.Fatalf("got %q", got)
	}
	if got, _ := NormalizeAttr("$span.kind"); got != "$span.kind" {
		t.Fatalf("got %q", got)
	}
	if _, err := NormalizeAttr("  "); err == nil {
		t.Fatal("empty attr must error")
	}
}

func TestKindClause(t *testing.T) {
	if got := KindClause("server"); got != "$span.kind = 'SPAN_KIND_SERVER'" {
		t.Fatalf("got %q", got)
	}
	if got := KindClause("SPAN_KIND_CLIENT"); got != "$span.kind = 'SPAN_KIND_CLIENT'" {
		t.Fatalf("got %q", got)
	}
}

func TestAndJoinSkipsEmpty(t *testing.T) {
	if got := AndJoin("", "a = 1", "", "b = 2"); got != "a = 1 AND b = 2" {
		t.Fatalf("got %q", got)
	}
	if got := AndJoin("", ""); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestQuoteEscapesSingleQuotes(t *testing.T) {
	if got := Quote("O'Brien"); got != "'O''Brien'" {
		t.Fatalf("got %q", got)
	}
}
```

Run: `go test ./internal/traces -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/traces/span.go`:

```go
// Package traces implements the trace explorer: span model, aggregations,
// and the waterfall/shape algorithms over Bronto's .traces logset.
// Field literals and formulas follow v1 exactly (see
// docs/superpowers/specs/2026-07-07-v1-traces-extraction.md).
package traces

import (
	"fmt"
	"strconv"
	"strings"
)

const FromExpr = "logset = '.traces'"

var SpanFields = []string{
	"$span.trace_id", "$span.span_id", "$span.parent_span_id",
	"$span.name", "$span.kind", "$span.duration_nano",
	"$span.start_time_unix_nano", "$span.end_time_unix_nano",
	"$span.status_code", "$service.name", "$service.namespace",
}

type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	Kind         string // SPAN_KIND_ prefix stripped: SERVER, CLIENT, ...
	Service      string
	Status       string // raw: STATUS_CODE_ERROR etc.
	StartNS      int64
	EndNS        int64
	DurationNS   int64
}

func RowToSpan(row map[string]any) Span {
	s := Span{
		TraceID:      str(row, "$span.trace_id"),
		SpanID:       str(row, "$span.span_id"),
		ParentSpanID: str(row, "$span.parent_span_id"),
		Name:         str(row, "$span.name"),
		Kind:         strings.TrimPrefix(str(row, "$span.kind"), "SPAN_KIND_"),
		Service:      str(row, "$service.name"),
		Status:       str(row, "$span.status_code"),
		StartNS:      toInt64(row["$span.start_time_unix_nano"]),
		EndNS:        toInt64(row["$span.end_time_unix_nano"]),
		DurationNS:   toInt64(row["$span.duration_nano"]),
	}
	if s.DurationNS == 0 && s.EndNS > s.StartNS && s.StartNS > 0 {
		s.DurationNS = s.EndNS - s.StartNS
	}
	if s.EndNS == 0 && s.StartNS > 0 && s.DurationNS > 0 {
		s.EndNS = s.StartNS + s.DurationNS
	}
	return s
}

func (s Span) IsError() bool {
	return strings.HasSuffix(strings.ToUpper(s.Status), "ERROR")
}

// FormatDurationNS renders nanoseconds for humans: µs below 1ms (1 decimal),
// ms below 1s (2 decimals), seconds above (2 decimals), em dash for <=0.
func FormatDurationNS(ns int64) string {
	if ns <= 0 {
		return "—"
	}
	ms := float64(ns) / 1e6
	switch {
	case ms < 1:
		return fmt.Sprintf("%.1fµs", float64(ns)/1e3)
	case ms < 1000:
		return fmt.Sprintf("%.2fms", ms)
	default:
		return fmt.Sprintf("%.2fs", ms/1000)
	}
}

func str(row map[string]any, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// toInt64 tolerantly coerces API numbers that may arrive as float64,
// int, or numeric strings. Unparseable values become 0 (v1 semantics).
func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return int64(f)
		}
	}
	return 0
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	case string:
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return f
		}
	}
	return 0
}
```

`internal/traces/query.go`:

```go
package traces

import (
	"fmt"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

const RootOnlyClause = "NOT EXISTS $span.parent_span_id"

// NormalizeAttr turns a user-supplied attribute into query form:
// leading $ added unless present. http.route -> $http.route.
func NormalizeAttr(attr string) (string, error) {
	a := strings.TrimSpace(attr)
	if a == "" {
		return "", clierr.New("usage_invalid_attr", "attribute name is empty")
	}
	if !strings.HasPrefix(a, "$") {
		a = "$" + a
	}
	return a, nil
}

// KindClause builds the span-kind filter. Accepts bare (server) or
// prefixed (SPAN_KIND_SERVER) forms; where-clauses always use the
// full SPAN_KIND_* wire form (extraction §5.3).
func KindClause(kind string) string {
	k := strings.ToUpper(strings.TrimSpace(kind))
	if !strings.HasPrefix(k, "SPAN_KIND_") {
		k = "SPAN_KIND_" + k
	}
	return fmt.Sprintf("$span.kind = '%s'", k)
}

func AndJoin(clauses ...string) string {
	var parts []string
	for _, c := range clauses {
		if c != "" {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, " AND ")
}

// Quote single-quotes a literal for the query language, doubling
// embedded quotes ('O''Brien').
func Quote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}
```

Run: `go test ./internal/traces -v` — Expected: PASS.

- [ ] **Step 3: Commit**

```bash
gofmt -l internal && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/traces
git commit -m "feat: traces span model, duration formatting, query builders"
```

---

### Task 2: Aggregations (services, operations, attribute breakdown)

**Files:**
- Create: `internal/traces/aggregate.go`, `internal/traces/aggregate_test.go`

**Interfaces:**
- Produces:
  - `type Aggregator struct { Client *bronto.Client; Time timerange.Spec }`
  - `(*Aggregator).Services(ctx context.Context, errorsOnly bool, limit int) ([]map[string]any, error)` — rows `{service, spans int64, avg_ns, max_ns int64, avg, max string}` sorted spans desc (extraction §2.1)
  - `(*Aggregator).Operations(ctx, service string, errorsOnly bool, limit int) ([]map[string]any, error)` — rows `{service, operation, spans, avg_ns, max_ns, avg, max}` (extraction §2.2)
  - `type AttrOptions struct { By []string; RootOnly bool; Service, Kind, Where string; ErrorsOnly, IncludeEmpty bool; Limit int }`
  - `(*Aggregator).Attributes(ctx, opts AttrOptions) (rows []map[string]any, columns []string, droppedEmpty int, err error)` — overfetch `max(limit*5,200)`, 3+1 queries, `<missing>` labeling, merge-sort-trim (extraction §2.3)
  - Internal: `groupAggregate(ctx, aggregate string, groups []string, where string, limit int) (map[string]aggEntry, error)` with `aggEntry{Vals []string; V float64}`, key = vals joined by `"\x1f"`; `parseGroup(v any) []string` handles []any / map (sorted keys) / bracketed string `"[a, b]"` / scalar
- Consumes: `bronto.Client/SearchRequest/SearchResponse`, Task 1.

- [ ] **Step 1: Failing tests**

`internal/traces/aggregate_test.go`:

```go
package traces

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

// aggServer answers /search per the aggregate in select[0].
func aggServer(t *testing.T, responses map[string]string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		bodies = append(bodies, body)
		sel, _ := body["select"].([]any)
		key := ""
		if len(sel) > 0 {
			key = sel[0].(string)
		}
		resp, ok := responses[key]
		if !ok {
			resp = `{"groups":[]}`
		}
		_, _ = w.Write([]byte(resp))
	}))
	return srv, &bodies
}

func newAgg(srv *httptest.Server) *Aggregator {
	return &Aggregator{
		Client: bronto.NewClient(srv.Client(), srv.URL),
		Time:   timerange.Spec{TimeRange: "Last 15 minutes"},
	}
}

func TestServicesMergesThreeAggregates(t *testing.T) {
	srv, bodies := aggServer(t, map[string]string{
		"count(*)":                  `{"groups":[{"group":["cart"],"count(*)":30},{"group":["web"],"count(*)":10}]}`,
		"avg($span.duration_nano)":  `{"groups":[{"group":["cart"],"avg($span.duration_nano)":2000000}]}`,
		"max($span.duration_nano)":  `{"groups":[{"group":["web"],"max($span.duration_nano)":9000000}]}`,
	})
	defer srv.Close()
	rows, err := newAgg(srv).Services(context.Background(), false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["service"] != "cart" || rows[0]["spans"] != int64(30) {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0]["avg"] != "2.00ms" || rows[1]["max"] != "9.00ms" {
		t.Fatalf("formatted: %v", rows)
	}
	if rows[1]["avg"] != "—" { // missing entry defaults to 0 -> em dash
		t.Fatalf("missing avg = %v", rows[1]["avg"])
	}
	// every request targeted the traces logset
	for _, b := range *bodies {
		if b["from_expr"] != FromExpr {
			t.Fatalf("from_expr = %v", b["from_expr"])
		}
	}
}

func TestOperationsGroupsByServiceAndName(t *testing.T) {
	srv, bodies := aggServer(t, map[string]string{
		"count(*)": `{"groups":[{"group":["cart","HGET"],"count(*)":5}]}`,
	})
	defer srv.Close()
	rows, err := newAgg(srv).Operations(context.Background(), "cart", true, 25)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0]["operation"] != "HGET" || rows[0]["spans"] != int64(5) {
		t.Fatalf("rows = %v", rows)
	}
	// where must include service filter AND errors filter
	b := (*bodies)[0]
	where, _ := b["where"].(string)
	if where != "$service.name = 'cart' AND $span.status_code = 'STATUS_CODE_ERROR'" {
		t.Fatalf("where = %q", where)
	}
}

func TestAttributesMissingHandlingAndTrim(t *testing.T) {
	srv, bodies := aggServer(t, map[string]string{
		"count(*)": `{"groups":[
			{"group":["/api/a"],"count(*)":50},
			{"group":[""],"count(*)":40},
			{"group":["/api/b"],"count(*)":30}]}`,
		"avg($span.duration_nano)": `{"groups":[{"group":["/api/a"],"avg($span.duration_nano)":1500000}]}`,
		"max($span.duration_nano)": `{"groups":[]}`,
	})
	defer srv.Close()

	rows, cols, dropped, err := newAgg(srv).Attributes(context.Background(), AttrOptions{
		By: []string{"http.route"}, RootOnly: true, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1 (empty group)", dropped)
	}
	if len(rows) != 2 || rows[0]["http.route"] != "/api/a" || rows[1]["http.route"] != "/api/b" {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0]["err%"] == nil {
		t.Fatalf("err%% column missing: %v", rows[0])
	}
	wantCols := []string{"http.route", "spans", "errors", "err%", "avg", "max"}
	if len(cols) != len(wantCols) {
		t.Fatalf("cols = %v", cols)
	}
	// 4 queries ran (count, avg, max, errors-count) with overfetch limit 200
	if len(*bodies) != 4 {
		t.Fatalf("queries = %d, want 4", len(*bodies))
	}
	if (*bodies)[0]["limit"] != float64(200) {
		t.Fatalf("overfetch limit = %v, want 200", (*bodies)[0]["limit"])
	}
	// root-only clause present
	where, _ := (*bodies)[0]["where"].(string)
	if where != RootOnlyClause {
		t.Fatalf("where = %q", where)
	}
}

func TestAttributesIncludeEmptyLabelsMissing(t *testing.T) {
	srv, _ := aggServer(t, map[string]string{
		"count(*)": `{"groups":[{"group":["null"],"count(*)":7}]}`,
	})
	defer srv.Close()
	rows, _, dropped, err := newAgg(srv).Attributes(context.Background(), AttrOptions{
		By: []string{"http.route"}, IncludeEmpty: true, ErrorsOnly: true, Limit: 10,
	})
	if err != nil || dropped != 0 {
		t.Fatalf("err=%v dropped=%d", err, dropped)
	}
	if rows[0]["http.route"] != "<missing>" {
		t.Fatalf("label = %v", rows[0]["http.route"])
	}
	if _, has := rows[0]["errors"]; has { // errorsOnly drops errors columns
		t.Fatalf("errors column must be absent: %v", rows[0])
	}
}

func TestParseGroupForms(t *testing.T) {
	if got := parseGroup([]any{"a", float64(2)}); got[0] != "a" || got[1] != "2" {
		t.Fatalf("list form: %v", got)
	}
	if got := parseGroup("[a, b]"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("bracket form: %v", got)
	}
	if got := parseGroup("single"); len(got) != 1 || got[0] != "single" {
		t.Fatalf("scalar form: %v", got)
	}
	got := parseGroup(map[string]any{"b": "2", "a": "1"}) // sorted keys -> deterministic
	if len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("map form: %v", got)
	}
}
```

Note: drop the `_ = api.ErrorFromStatus` line and its import if not needed — it exists only to hint that the import list must compile; manage imports properly.

Run: `go test ./internal/traces -run 'TestServices|TestOperations|TestAttributes|TestParseGroup' -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/traces/aggregate.go`:

```go
package traces

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

const (
	aggCount = "count(*)"
	aggAvg   = "avg($span.duration_nano)"
	aggMax   = "max($span.duration_nano)"
	errorsClause = "$span.status_code = 'STATUS_CODE_ERROR'"
)

type Aggregator struct {
	Client *bronto.Client
	Time   timerange.Spec
}

type aggEntry struct {
	Vals []string
	V    float64
}

func (a *Aggregator) groupAggregate(ctx context.Context, aggregate string, groups []string, where string, limit int) (map[string]aggEntry, error) {
	resp, err := a.Client.Search(ctx, bronto.SearchRequest{
		FromExpr: FromExpr, Time: a.Time,
		Select: []string{aggregate}, Groups: groups,
		Where: where, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	rows := resp.Groups
	if len(rows) == 0 {
		rows = resp.Result
	}
	out := make(map[string]aggEntry, len(rows))
	for _, row := range rows {
		vals := parseGroup(row["group"])
		key := strings.Join(vals, "\x1f")
		out[key] = aggEntry{Vals: vals, V: toFloat(row[aggregate])}
	}
	return out, nil
}

// parseGroup normalizes the API's "group" field: list, map (sorted keys
// for determinism), bracketed string "[a, b]", or scalar.
func parseGroup(v any) []string {
	switch g := v.(type) {
	case []any:
		vals := make([]string, len(g))
		for i, item := range g {
			if item == nil {
				vals[i] = ""
				continue
			}
			vals[i] = fmt.Sprint(item)
		}
		return vals
	case map[string]any:
		keys := make([]string, 0, len(g))
		for k := range g {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		vals := make([]string, len(keys))
		for i, k := range keys {
			vals[i] = fmt.Sprint(g[k])
		}
		return vals
	case string:
		s := strings.TrimSpace(g)
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			inner := strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
			if inner == "" {
				return nil
			}
			parts := strings.Split(inner, ", ")
			return parts
		}
		return []string{s}
	case nil:
		return nil
	default:
		return []string{fmt.Sprint(g)}
	}
}

func (a *Aggregator) Services(ctx context.Context, errorsOnly bool, limit int) ([]map[string]any, error) {
	where := ""
	if errorsOnly {
		where = errorsClause
	}
	return a.threeWayRows(ctx, []string{"$service.name"}, where, limit,
		func(vals []string) map[string]any {
			return map[string]any{"service": at(vals, 0)}
		})
}

func (a *Aggregator) Operations(ctx context.Context, service string, errorsOnly bool, limit int) ([]map[string]any, error) {
	var svcClause, errClause string
	if service != "" {
		svcClause = "$service.name = " + Quote(service)
	}
	if errorsOnly {
		errClause = errorsClause
	}
	return a.threeWayRows(ctx, []string{"$service.name", "$span.name"},
		AndJoin(svcClause, errClause), limit,
		func(vals []string) map[string]any {
			return map[string]any{"service": at(vals, 0), "operation": at(vals, 1)}
		})
}

// threeWayRows runs count/avg/max over the same grouping, unions the keys
// (count is the ranking ground truth; missing entries default to 0), and
// returns rows sorted by span count descending.
func (a *Aggregator) threeWayRows(ctx context.Context, groups []string, where string, limit int, keyCols func([]string) map[string]any) ([]map[string]any, error) {
	counts, err := a.groupAggregate(ctx, aggCount, groups, where, limit)
	if err != nil {
		return nil, err
	}
	avgs, err := a.groupAggregate(ctx, aggAvg, groups, where, limit)
	if err != nil {
		return nil, err
	}
	maxes, err := a.groupAggregate(ctx, aggMax, groups, where, limit)
	if err != nil {
		return nil, err
	}
	keys := unionKeys(counts, avgs, maxes)
	rows := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		vals := entryVals(k, counts, avgs, maxes)
		row := keyCols(vals)
		row["spans"] = int64(counts[k].V)
		row["avg_ns"] = int64(avgs[k].V)
		row["max_ns"] = int64(maxes[k].V)
		row["avg"] = FormatDurationNS(int64(avgs[k].V))
		row["max"] = FormatDurationNS(int64(maxes[k].V))
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i]["spans"].(int64) > rows[j]["spans"].(int64)
	})
	return rows, nil
}

type AttrOptions struct {
	By           []string
	RootOnly     bool
	Service      string
	Kind         string
	Where        string
	ErrorsOnly   bool
	IncludeEmpty bool
	Limit        int
}

func (a *Aggregator) Attributes(ctx context.Context, opts AttrOptions) ([]map[string]any, []string, int, error) {
	groupKeys := make([]string, len(opts.By))
	for i, attr := range opts.By {
		norm, err := NormalizeAttr(attr)
		if err != nil {
			return nil, nil, 0, err
		}
		groupKeys[i] = norm
	}
	var clauses []string
	if opts.RootOnly {
		clauses = append(clauses, RootOnlyClause)
	}
	if opts.Service != "" {
		clauses = append(clauses, "$service.name = "+Quote(opts.Service))
	}
	if opts.Kind != "" {
		clauses = append(clauses, KindClause(opts.Kind))
	}
	if opts.ErrorsOnly {
		clauses = append(clauses, errorsClause)
	}
	if opts.Where != "" {
		clauses = append(clauses, "("+opts.Where+")")
	}
	where := AndJoin(clauses...)

	fetchLimit := opts.Limit * 5
	if fetchLimit < 200 {
		fetchLimit = 200
	}
	counts, err := a.groupAggregate(ctx, aggCount, groupKeys, where, fetchLimit)
	if err != nil {
		return nil, nil, 0, err
	}
	avgs, err := a.groupAggregate(ctx, aggAvg, groupKeys, where, fetchLimit)
	if err != nil {
		return nil, nil, 0, err
	}
	maxes, err := a.groupAggregate(ctx, aggMax, groupKeys, where, fetchLimit)
	if err != nil {
		return nil, nil, 0, err
	}
	var errCounts map[string]aggEntry
	if !opts.ErrorsOnly {
		errCounts, err = a.groupAggregate(ctx, aggCount, groupKeys,
			AndJoin(where, errorsClause), fetchLimit)
		if err != nil {
			return nil, nil, 0, err
		}
	}

	attrNames := make([]string, len(groupKeys))
	for i, g := range groupKeys {
		attrNames[i] = strings.TrimPrefix(g, "$")
	}
	dropped := 0
	rows := make([]map[string]any, 0, len(counts))
	for key, entry := range counts {
		vals := entry.Vals
		for len(vals) < len(groupKeys) {
			vals = append(vals, "")
		}
		if !opts.IncludeEmpty && hasMissing(vals[:len(groupKeys)]) {
			dropped++
			continue
		}
		row := map[string]any{}
		for i, name := range attrNames {
			row[name] = labelGroupValue(vals[i])
		}
		n := int64(entry.V)
		row["spans"] = n
		if errCounts != nil {
			errN := int64(errCounts[key].V)
			row["errors"] = errN
			if n > 0 {
				row["err%"] = fmt.Sprintf("%.1f", float64(errN)/float64(n)*100)
			} else {
				row["err%"] = ""
			}
		}
		row["avg"] = FormatDurationNS(int64(avgs[key].V))
		row["max"] = FormatDurationNS(int64(maxes[key].V))
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i]["spans"].(int64) > rows[j]["spans"].(int64)
	})
	if len(rows) > opts.Limit {
		rows = rows[:opts.Limit]
	}
	columns := append([]string{}, attrNames...)
	columns = append(columns, "spans")
	if errCounts != nil {
		columns = append(columns, "errors", "err%")
	}
	columns = append(columns, "avg", "max")
	return rows, columns, dropped, nil
}

func hasMissing(vals []string) bool {
	for _, v := range vals {
		if v == "" || v == "null" || v == "None" {
			return true
		}
	}
	return false
}

func labelGroupValue(v string) string {
	if v == "" || v == "null" || v == "None" {
		return "<missing>"
	}
	return v
}

func unionKeys(maps ...map[string]aggEntry) []string {
	seen := map[string]bool{}
	var keys []string
	for _, m := range maps {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys) // deterministic pre-sort; final order set by span count
	return keys
}

func entryVals(key string, maps ...map[string]aggEntry) []string {
	for _, m := range maps {
		if e, ok := m[key]; ok && len(e.Vals) > 0 {
			return e.Vals
		}
	}
	if key == "" {
		return nil
	}
	return strings.Split(key, "\x1f")
}

func at(vals []string, i int) string {
	if i < len(vals) {
		return vals[i]
	}
	return ""
}
```

Run: `go test ./internal/traces -v` — Expected: PASS.

- [ ] **Step 3: Commit**

```bash
gofmt -l internal && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/traces
git commit -m "feat: traces aggregations (services, operations, attribute breakdown)"
```

---

### Task 3: Span listing and trace fetching

**Files:**
- Create: `internal/traces/fetch.go`, `internal/traces/fetch_test.go`

**Interfaces:**
- Produces:
  - `type ListOptions struct { Service, Operation string; MinDurationMS float64; ErrorsOnly bool; Limit int }`
  - `(*Aggregator).ListSpans(ctx, opts ListOptions) ([]map[string]any, error)` — single query `select=["@time", SpanFields...]`, `most_recent_first=true`; rows `{@time, service, operation, duration (formatted), duration_ns int64, status (prefix-stripped), trace_id, span_id}` (extraction §2.4)
  - `(*Aggregator).FetchTraceSpans(ctx, traceIDs []string) ([]Span, error)` — OR-chain batches of 15, `limit 5000` per batch, `most_recent_first=false`, select SpanFields (no @time) (extraction §4.2; `IN(...)` returns 500)
  - `(*Aggregator).FindSampleTraceIDs(ctx, where string, sample int) ([]string, error)` — `select=["$span.trace_id"]`, `limit=max(sample*3,30)`, `most_recent_first=true`, dedup preserving order, early-stop at `sample` (extraction §4.1)
- Consumes: Tasks 1–2.

- [ ] **Step 1: Failing tests**

`internal/traces/fetch_test.go`:

```go
package traces

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListSpansBuildsWhereAndRows(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"result":[{"@time":"t1","$span.trace_id":"tr","$span.span_id":"sp",
			"$span.name":"GET /x","$service.name":"web","$span.duration_nano":2000000,
			"$span.status_code":"STATUS_CODE_OK"}]}`))
	}))
	defer srv.Close()

	rows, err := newAgg(srv).ListSpans(context.Background(), ListOptions{
		Service: "web", Operation: "GET /x", MinDurationMS: 1.5, ErrorsOnly: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	where, _ := body["where"].(string)
	want := "$service.name = 'web' AND $span.name = 'GET /x' AND $span.duration_nano > 1500000 AND $span.status_code = 'STATUS_CODE_ERROR'"
	if where != want {
		t.Fatalf("where = %q\nwant    %q", where, want)
	}
	if body["most_recent_first"] != true {
		t.Fatal("most_recent_first must be true")
	}
	if rows[0]["duration"] != "2.00ms" || rows[0]["status"] != "OK" || rows[0]["trace_id"] != "tr" {
		t.Fatalf("row = %v", rows[0])
	}
}

func TestFetchTraceSpansBatchesOrChains(t *testing.T) {
	var wheres []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		wh, _ := body["where"].(string)
		wheres = append(wheres, wh)
		if body["limit"] != float64(5000) {
			t.Errorf("limit = %v, want 5000", body["limit"])
		}
		_, _ = w.Write([]byte(`{"result":[{"$span.trace_id":"x","$span.span_id":"s1"}]}`))
	}))
	defer srv.Close()

	ids := make([]string, 17) // forces 2 batches (15 + 2)
	for i := range ids {
		ids[i] = fmt.Sprintf("id%02d", i)
	}
	spans, err := newAgg(srv).FetchTraceSpans(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(wheres) != 2 {
		t.Fatalf("batches = %d, want 2", len(wheres))
	}
	if strings.Count(wheres[0], " OR ") != 14 || strings.Count(wheres[1], " OR ") != 1 {
		t.Fatalf("OR chains wrong: %q / %q", wheres[0], wheres[1])
	}
	if !strings.Contains(wheres[0], "$span.trace_id = 'id00'") {
		t.Fatalf("clause form: %q", wheres[0])
	}
	if len(spans) != 2 {
		t.Fatalf("spans = %d", len(spans))
	}
}

func TestFindSampleTraceIDsDedupsAndStops(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"result":[
			{"$span.trace_id":"a"},{"$span.trace_id":"b"},{"$span.trace_id":"a"},
			{"$span.trace_id":"c"},{"$span.trace_id":"d"}]}`))
	}))
	defer srv.Close()

	ids, err := newAgg(srv).FindSampleTraceIDs(context.Background(), "x = 1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != "a" || ids[1] != "b" || ids[2] != "c" {
		t.Fatalf("ids = %v", ids)
	}
	if body["limit"] != float64(30) { // max(3*3, 30)
		t.Fatalf("limit = %v", body["limit"])
	}
	if body["most_recent_first"] != true {
		t.Fatal("sampling is most-recent-first")
	}
}
```

Run: `go test ./internal/traces -run 'TestListSpans|TestFetchTrace|TestFindSample' -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/traces/fetch.go`:

```go
package traces

import (
	"context"
	"fmt"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/bronto"
)

type ListOptions struct {
	Service       string
	Operation     string
	MinDurationMS float64
	ErrorsOnly    bool
	Limit         int
}

func (a *Aggregator) ListSpans(ctx context.Context, opts ListOptions) ([]map[string]any, error) {
	var clauses []string
	if opts.Service != "" {
		clauses = append(clauses, "$service.name = "+Quote(opts.Service))
	}
	if opts.Operation != "" {
		clauses = append(clauses, "$span.name = "+Quote(opts.Operation))
	}
	if opts.MinDurationMS > 0 {
		clauses = append(clauses, fmt.Sprintf("$span.duration_nano > %d", int64(opts.MinDurationMS*1e6)))
	}
	if opts.ErrorsOnly {
		clauses = append(clauses, errorsClause)
	}
	mrf := true
	resp, err := a.Client.Search(ctx, bronto.SearchRequest{
		FromExpr: FromExpr, Time: a.Time,
		Select: append([]string{"@time"}, SpanFields...),
		Where:  AndJoin(clauses...), Limit: opts.Limit, MostRecentFirst: &mrf,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, len(resp.EventRows()))
	for _, r := range resp.EventRows() {
		s := RowToSpan(r)
		rows = append(rows, map[string]any{
			"@time":       r["@time"],
			"service":     s.Service,
			"operation":   s.Name,
			"duration":    FormatDurationNS(s.DurationNS),
			"duration_ns": s.DurationNS,
			"status":      strings.TrimPrefix(s.Status, "STATUS_CODE_"),
			"trace_id":    s.TraceID,
			"span_id":     s.SpanID,
		})
	}
	return rows, nil
}

// FetchTraceSpans fetches every span of the given traces. /search rejects
// IN(...) with a 500, so trace ids go into OR-chains batched at 15 per
// request with a 5000-span ceiling per batch (extraction §4.2).
func (a *Aggregator) FetchTraceSpans(ctx context.Context, traceIDs []string) ([]Span, error) {
	const batchSize = 15
	mrf := false
	var spans []Span
	for start := 0; start < len(traceIDs); start += batchSize {
		end := start + batchSize
		if end > len(traceIDs) {
			end = len(traceIDs)
		}
		clauses := make([]string, 0, end-start)
		for _, id := range traceIDs[start:end] {
			clauses = append(clauses, "$span.trace_id = "+Quote(id))
		}
		resp, err := a.Client.Search(ctx, bronto.SearchRequest{
			FromExpr: FromExpr, Time: a.Time,
			Select: SpanFields, Where: strings.Join(clauses, " OR "),
			Limit: 5000, MostRecentFirst: &mrf,
		})
		if err != nil {
			return nil, err
		}
		for _, row := range resp.EventRows() {
			spans = append(spans, RowToSpan(row))
		}
	}
	return spans, nil
}

// FindSampleTraceIDs returns up to sample distinct trace ids from the most
// recent spans matching where (not a uniform random sample; extraction §5.8).
func (a *Aggregator) FindSampleTraceIDs(ctx context.Context, where string, sample int) ([]string, error) {
	limit := sample * 3
	if limit < 30 {
		limit = 30
	}
	mrf := true
	resp, err := a.Client.Search(ctx, bronto.SearchRequest{
		FromExpr: FromExpr, Time: a.Time,
		Select: []string{"$span.trace_id"}, Where: where,
		Limit: limit, MostRecentFirst: &mrf,
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var ids []string
	for _, row := range resp.EventRows() {
		id := str(row, "$span.trace_id")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		if len(ids) >= sample {
			break
		}
	}
	return ids, nil
}
```

Run: `go test ./internal/traces -v` — Expected: PASS.

- [ ] **Step 3: Commit**

```bash
gofmt -l internal && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/traces
git commit -m "feat: traces span listing, batched trace fetch, sampling"
```

---

### Task 4: Single-trace waterfall (`show`)

**Files:**
- Create: `internal/traces/waterfall.go`, `internal/traces/waterfall_test.go`

**Interfaces:**
- Produces:
  - `traces.BuildTree(spans []Span) (roots []Span, children map[string][]Span)` — children keyed by ParentSpanID, each list sorted by (StartNS, DurationNS); roots = spans whose parent is not in the batch (client-side root, extraction §3.1/§5.1), sorted by StartNS; fallback single earliest span when none
  - `traces.TraceBounds(spans []Span) (start, end, total int64)` — extraction §3.2
  - `traces.RenderBar(s Span, traceStart, total int64, width int, color bool) string` — `·`-padded `█` bar; red on error, green otherwise; DELIBERATE v2 improvement over v1 (extraction §5.14): clamp `leftPad ≤ width-1` and `leftPad+barLen ≤ width` so bars never overflow
  - `traces.RenderWaterfall(w io.Writer, spans []Span, width int, color bool)` — header line (`N span(s) across M service(s), total X`), then iterative DFS: 2-space indent per depth, `service/name` label padded to `min(maxLen+4, 70)`, bar, formatted duration, ` ERROR` suffix (red when color) for error spans, dim status (prefix-stripped) for non-UNSET non-error statuses
  - `traces.WaterfallRows(spans []Span) []map[string]any` — machine form: DFS-ordered rows `{depth int, service, operation, trace_id, span_id, parent_span_id, start_ns, duration_ns, duration, status, error bool}`
- Consumes: Task 1.

- [ ] **Step 1: Failing tests**

`internal/traces/waterfall_test.go`:

```go
package traces

import (
	"bytes"
	"strings"
	"testing"
)

func testSpans() []Span {
	return []Span{
		{TraceID: "t", SpanID: "root", Name: "POST /add", Service: "cart",
			Kind: "SERVER", StartNS: 0, DurationNS: 100, EndNS: 100},
		{TraceID: "t", SpanID: "c1", ParentSpanID: "root", Name: "HGET", Service: "cart",
			Kind: "CLIENT", StartNS: 10, DurationNS: 30, EndNS: 40},
		{TraceID: "t", SpanID: "c2", ParentSpanID: "root", Name: "HMSET", Service: "cart",
			Kind: "CLIENT", StartNS: 50, DurationNS: 40, EndNS: 90,
			Status: "STATUS_CODE_ERROR"},
		{TraceID: "t", SpanID: "orphan", ParentSpanID: "gone", Name: "stray", Service: "web",
			StartNS: 5, DurationNS: 10, EndNS: 15},
	}
}

func TestBuildTreeRootsAndOrphans(t *testing.T) {
	roots, children := BuildTree(testSpans())
	if len(roots) != 2 { // true root + orphan (parent "gone" not in batch)
		t.Fatalf("roots = %d: %v", len(roots), roots)
	}
	if roots[0].SpanID != "root" { // sorted by StartNS: 0 < 5
		t.Fatalf("first root = %s", roots[0].SpanID)
	}
	kids := children["root"]
	if len(kids) != 2 || kids[0].SpanID != "c1" { // sorted by StartNS
		t.Fatalf("children = %v", kids)
	}
}

func TestBuildTreeFallbackEarliest(t *testing.T) {
	// self-referencing cycle: no span's parent is missing, so no roots
	spans := []Span{
		{SpanID: "a", ParentSpanID: "b", StartNS: 20},
		{SpanID: "b", ParentSpanID: "a", StartNS: 10},
	}
	roots, _ := BuildTree(spans)
	if len(roots) != 1 || roots[0].SpanID != "b" {
		t.Fatalf("fallback root = %v", roots)
	}
}

func TestTraceBounds(t *testing.T) {
	start, end, total := TraceBounds(testSpans())
	if start != 0 || end != 100 || total != 100 {
		t.Fatalf("bounds = %d..%d total %d", start, end, total)
	}
}

func TestRenderBarClamped(t *testing.T) {
	// span occupying the second half of a width-10 axis
	s := Span{StartNS: 50, DurationNS: 50}
	bar := RenderBar(s, 0, 100, 10, false)
	if bar != "·····█████" {
		t.Fatalf("bar = %q", bar)
	}
	// pathological offset beyond axis must not overflow width
	far := Span{StartNS: 1000, DurationNS: 50}
	bar2 := RenderBar(far, 0, 100, 10, false)
	if len([]rune(bar2)) != 10 {
		t.Fatalf("overflow: %q (%d runes)", bar2, len([]rune(bar2)))
	}
}

func TestRenderWaterfallStructure(t *testing.T) {
	var buf bytes.Buffer
	RenderWaterfall(&buf, testSpans(), 20, false)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.Contains(lines[0], "4 span(s) across 2 service(s)") {
		t.Fatalf("header = %q", lines[0])
	}
	// DFS order: root, its children (c1, c2), then orphan root
	if !strings.Contains(lines[1], "cart/POST /add") {
		t.Fatalf("line1 = %q", lines[1])
	}
	if !strings.HasPrefix(strings.TrimLeft(lines[2], " "), "cart/HGET") ||
		!strings.HasPrefix(lines[2], "  ") {
		t.Fatalf("child not indented: %q", lines[2])
	}
	if !strings.Contains(lines[3], "ERROR") {
		t.Fatalf("error span unmarked: %q", lines[3])
	}
	if !strings.Contains(lines[4], "web/stray") {
		t.Fatalf("orphan missing: %q", lines[4])
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatal("color=false must not emit ANSI codes")
	}
}

func TestWaterfallRowsDepthAndOrder(t *testing.T) {
	rows := WaterfallRows(testSpans())
	if len(rows) != 4 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0]["depth"] != 0 || rows[1]["depth"] != 1 || rows[3]["depth"] != 0 {
		t.Fatalf("depths: %v %v %v", rows[0]["depth"], rows[1]["depth"], rows[3]["depth"])
	}
	if rows[2]["error"] != true {
		t.Fatalf("error flag: %v", rows[2])
	}
}
```

Run: `go test ./internal/traces -run 'TestBuildTree|TestTraceBounds|TestRenderBar|TestRenderWaterfall|TestWaterfallRows' -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/traces/waterfall.go`:

```go
package traces

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
	ansiCyan  = "\x1b[36m"
)

// BuildTree indexes spans into a parent->children map and finds roots.
// A span is a root when its parent is absent from THIS batch — which
// includes true roots and orphans whose parent fell outside the query
// window (extraction §5.1).
func BuildTree(spans []Span) ([]Span, map[string][]Span) {
	byID := make(map[string]bool, len(spans))
	for _, s := range spans {
		if s.SpanID != "" {
			byID[s.SpanID] = true
		}
	}
	children := map[string][]Span{}
	for _, s := range spans {
		children[s.ParentSpanID] = append(children[s.ParentSpanID], s)
	}
	for k := range children {
		kids := children[k]
		sort.SliceStable(kids, func(i, j int) bool {
			if kids[i].StartNS != kids[j].StartNS {
				return kids[i].StartNS < kids[j].StartNS
			}
			return kids[i].DurationNS < kids[j].DurationNS
		})
	}
	var roots []Span
	for _, s := range spans {
		if !byID[s.ParentSpanID] {
			roots = append(roots, s)
		}
	}
	if len(roots) == 0 && len(spans) > 0 {
		earliest := spans[0]
		for _, s := range spans[1:] {
			if s.StartNS < earliest.StartNS {
				earliest = s
			}
		}
		roots = []Span{earliest}
	}
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].StartNS < roots[j].StartNS })
	return roots, children
}

func TraceBounds(spans []Span) (start, end, total int64) {
	var maxDur int64 = 1
	for _, s := range spans {
		if s.StartNS > 0 && (start == 0 || s.StartNS < start) {
			start = s.StartNS
		}
		if s.EndNS > end {
			end = s.EndNS
		}
		if s.DurationNS > maxDur {
			maxDur = s.DurationNS
		}
	}
	if end < start {
		end = start
	}
	total = end - start
	if total < maxDur {
		total = maxDur
	}
	return start, end, total
}

// RenderBar draws a span's position on the trace axis. Unlike v1, bars are
// clamped into [0,width] so pathological offsets cannot overflow the column.
func RenderBar(s Span, traceStart, total int64, width int, color bool) string {
	if total < 1 {
		total = 1
	}
	offset := s.StartNS - traceStart
	if offset < 0 || s.StartNS == 0 {
		offset = 0
	}
	length := s.DurationNS
	if length < 1 {
		length = 1
	}
	leftPad := int(offset * int64(width) / total)
	if leftPad > width-1 {
		leftPad = width - 1
	}
	barLen := int(length * int64(width) / total)
	if barLen < 1 {
		barLen = 1
	}
	if leftPad+barLen > width {
		barLen = width - leftPad
	}
	rightPad := width - leftPad - barLen
	dots := func(n int) string { return strings.Repeat("·", n) }
	bar := strings.Repeat("█", barLen)
	if !color {
		return dots(leftPad) + bar + dots(rightPad)
	}
	c := ansiGreen
	if s.IsError() {
		c = ansiRed
	}
	return ansiDim + dots(leftPad) + ansiReset + c + bar + ansiReset + ansiDim + dots(rightPad) + ansiReset
}

type frame struct {
	span  Span
	depth int
}

func dfsOrder(spans []Span) []frame {
	roots, children := BuildTree(spans)
	var out []frame
	stack := make([]frame, 0, len(spans))
	for i := len(roots) - 1; i >= 0; i-- {
		stack = append(stack, frame{roots[i], 0})
	}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		out = append(out, f)
		kids := children[f.span.SpanID]
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, frame{kids[i], f.depth + 1})
		}
	}
	return out
}

func RenderWaterfall(w io.Writer, spans []Span, width int, color bool) {
	services := map[string]bool{}
	maxLabel := 0
	for _, s := range spans {
		services[s.Service] = true
		if l := len(s.Service) + len(s.Name) + 1; l > maxLabel {
			maxLabel = l
		}
	}
	nameCol := maxLabel + 4
	if nameCol > 70 {
		nameCol = 70
	}
	start, _, total := TraceBounds(spans)
	fmt.Fprintf(w, "%d span(s) across %d service(s), total %s\n",
		len(spans), len(services), FormatDurationNS(total))

	for _, f := range dfsOrder(spans) {
		indent := strings.Repeat("  ", f.depth)
		labelPlain := f.span.Service + "/" + f.span.Name
		pad := nameCol - len(indent) - len(labelPlain)
		if pad < 1 {
			pad = 1
		}
		bar := RenderBar(f.span, start, total, width, color)
		status := ""
		switch {
		case f.span.IsError():
			if color {
				status = " " + ansiRed + "ERROR" + ansiReset
			} else {
				status = " ERROR"
			}
		case f.span.Status != "" && !strings.HasSuffix(strings.ToUpper(f.span.Status), "UNSET"):
			st := strings.TrimPrefix(f.span.Status, "STATUS_CODE_")
			if color {
				status = " " + ansiDim + st + ansiReset
			} else {
				status = " " + st
			}
		}
		fmt.Fprintf(w, "%s%s%s%s %s%s\n", indent, labelPlain,
			strings.Repeat(" ", pad), bar, FormatDurationNS(f.span.DurationNS), status)
	}
}

// WaterfallRows is the machine form of the waterfall: DFS order with depth.
func WaterfallRows(spans []Span) []map[string]any {
	rows := make([]map[string]any, 0, len(spans))
	for _, f := range dfsOrder(spans) {
		s := f.span
		rows = append(rows, map[string]any{
			"depth": f.depth, "service": s.Service, "operation": s.Name,
			"trace_id": s.TraceID, "span_id": s.SpanID, "parent_span_id": s.ParentSpanID,
			"start_ns": s.StartNS, "duration_ns": s.DurationNS,
			"duration": FormatDurationNS(s.DurationNS),
			"status":   strings.TrimPrefix(s.Status, "STATUS_CODE_"),
			"error":    s.IsError(),
		})
	}
	return rows
}
```

Run: `go test ./internal/traces -v` — Expected: PASS.

- [ ] **Step 3: Commit**

```bash
gofmt -l internal && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/traces
git commit -m "feat: single-trace waterfall (tree, bounds, clamped bars, DFS render)"
```

---

### Task 5: Aggregated shape waterfall

**Files:**
- Create: `internal/traces/shape.go`, `internal/traces/shape_test.go`

**Interfaces:**
- Produces:
  - `type ShapeBucket struct { Identity, Parent string; Service, Name string; Offsets, Durations []int64; TraceIDs map[string]bool; Errors int }` with methods `NSamples() int`, `NTraces() int`, `AvgOffset() int64`, `AvgDuration() int64`, `MinOffset() int64`, `MaxEnd() int64` (max over offset+duration pairs), `Depth() int`
  - Identity encoding: hops `service + "\x1f" + name` joined by `"\x1e"`; `Parent` = identity minus last hop ("" for anchor). Full-path grouping per extraction §5.11.
  - `type EntryMatch struct { EntryOnly bool; Service, Operation string; ErrorsOnly bool; MinDurationMS float64 }`; `(EntryMatch).Matches(s Span) bool`; `(EntryMatch).Active() bool`
  - `traces.ComputeShape(spans []Span, match EntryMatch) (buckets map[string]*ShapeBucket, tracesUsed int)` — group by trace; anchor = earliest matching span when `match.Active()`, else client-side root, else earliest (extraction §4.3); DFS from anchor with a children index carrying the path (NO parent-walk, no mutation — v1's mutate/restore dance eliminated per extraction §5.10; results identical); offsets relative to anchor.StartNS, negatives clamped in `add`
  - `traces.FilterMinTraces(buckets map[string]*ShapeBucket, min int) map[string]*ShapeBucket`
  - `traces.ShapeRows(buckets map[string]*ShapeBucket) []map[string]any` — machine form sorted by (depth, avg_offset): `{service, name, depth, parent (last hop "svc/name" or nil), samples, traces, avg_offset_ns, avg_duration_ns, min_offset_ns, max_end_ns, errors}` (extraction §4.6)
  - `traces.RenderShapeBar(b *ShapeBucket, axisEnd int64, width int, color bool) string` — `█` avg segment (priority), `▒` min/max band, `·` outside; red when Errors>0 else green; band clamped, avg segment truncated by loop bound (extraction §4.8)
  - `traces.RenderShape(w io.Writer, buckets map[string]*ShapeBucket, tracesUsed, totalSpans, width int, color bool)` — header + legend + DFS table with `k/N` presence column (extraction §4.7; sibling sort `(AvgOffset, -AvgDuration)`; axisEnd = max over (avgOffset+avgDur) and MaxEnd, floor 1)
- Consumes: Tasks 1, 4 (Span; not BuildTree — shape has its own per-trace traversal).

- [ ] **Step 1: Failing tests**

`internal/traces/shape_test.go`:

```go
package traces

import (
	"bytes"
	"strings"
	"testing"
)

// two traces with identical shape: entry -> db call; one trace has an extra retry span
func shapeSpans() []Span {
	return []Span{
		// trace 1
		{TraceID: "t1", SpanID: "a1", Name: "POST /add", Service: "cart", Kind: "SERVER",
			StartNS: 1000, DurationNS: 100, EndNS: 1100},
		{TraceID: "t1", SpanID: "b1", ParentSpanID: "a1", Name: "HGET", Service: "cart",
			Kind: "CLIENT", StartNS: 1010, DurationNS: 30, EndNS: 1040},
		// trace 2
		{TraceID: "t2", SpanID: "a2", Name: "POST /add", Service: "cart", Kind: "SERVER",
			StartNS: 5000, DurationNS: 120, EndNS: 5120},
		{TraceID: "t2", SpanID: "b2", ParentSpanID: "a2", Name: "HGET", Service: "cart",
			Kind: "CLIENT", StartNS: 5020, DurationNS: 40, EndNS: 5060,
			Status: "STATUS_CODE_ERROR"},
		{TraceID: "t2", SpanID: "c2", ParentSpanID: "a2", Name: "HGET", Service: "cart",
			Kind: "CLIENT", StartNS: 5070, DurationNS: 20, EndNS: 5090},
	}
}

func TestComputeShapeBucketsAndStats(t *testing.T) {
	buckets, used := ComputeShape(shapeSpans(), EntryMatch{EntryOnly: true})
	if used != 2 {
		t.Fatalf("tracesUsed = %d", used)
	}
	if len(buckets) != 2 { // entry bucket + cart/HGET bucket (same path in both traces)
		t.Fatalf("buckets = %d: %v", len(buckets), keysOf(buckets))
	}
	var entry, hget *ShapeBucket
	for _, b := range buckets {
		switch b.Name {
		case "POST /add":
			entry = b
		case "HGET":
			hget = b
		}
	}
	if entry == nil || hget == nil {
		t.Fatal("missing buckets")
	}
	if entry.Parent != "" || entry.Depth() != 0 {
		t.Fatalf("entry parentage: %q depth %d", entry.Parent, entry.Depth())
	}
	if hget.NSamples() != 3 || hget.NTraces() != 2 { // 1 + 2 occurrences, 2 traces
		t.Fatalf("hget samples=%d traces=%d", hget.NSamples(), hget.NTraces())
	}
	if hget.Errors != 1 {
		t.Fatalf("errors = %d", hget.Errors)
	}
	// offsets relative to each trace's anchor: 10, 20, 70 -> avg 33
	if hget.AvgOffset() != 33 {
		t.Fatalf("avg offset = %d", hget.AvgOffset())
	}
	if hget.MinOffset() != 10 {
		t.Fatalf("min offset = %d", hget.MinOffset())
	}
	if hget.MaxEnd() != 90 { // max(10+30, 20+40, 70+20)
		t.Fatalf("max end = %d", hget.MaxEnd())
	}
}

func keysOf(m map[string]*ShapeBucket) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestComputeShapeAnchorFallbackToRoot(t *testing.T) {
	// no SERVER span; match inactive -> root anchor
	spans := []Span{
		{TraceID: "t", SpanID: "r", Name: "job", Service: "worker", Kind: "INTERNAL",
			StartNS: 100, DurationNS: 50},
		{TraceID: "t", SpanID: "k", ParentSpanID: "r", Name: "step", Service: "worker",
			Kind: "INTERNAL", StartNS: 110, DurationNS: 10},
	}
	buckets, used := ComputeShape(spans, EntryMatch{})
	if used != 1 || len(buckets) != 2 {
		t.Fatalf("used=%d buckets=%d", used, len(buckets))
	}
}

func TestFilterMinTraces(t *testing.T) {
	buckets, _ := ComputeShape(shapeSpans(), EntryMatch{EntryOnly: true})
	if got := FilterMinTraces(buckets, 2); len(got) != 2 {
		t.Fatalf("min=2: %d", len(got))
	}
	if got := FilterMinTraces(buckets, 3); len(got) != 0 {
		t.Fatalf("min=3: %d", len(got))
	}
}

func TestShapeRowsSortedByDepthThenOffset(t *testing.T) {
	buckets, _ := ComputeShape(shapeSpans(), EntryMatch{EntryOnly: true})
	rows := ShapeRows(buckets)
	if rows[0]["depth"] != 0 || rows[1]["depth"] != 1 {
		t.Fatalf("order: %v", rows)
	}
	if rows[1]["traces"] != 2 || rows[1]["samples"] != 3 {
		t.Fatalf("row1 = %v", rows[1])
	}
}

func TestRenderShapeBarGlyphs(t *testing.T) {
	b := &ShapeBucket{Offsets: []int64{20, 40}, Durations: []int64{20, 20},
		TraceIDs: map[string]bool{"t1": true, "t2": true}}
	// axis 100, width 10: avg offset 30 -> cell 3; avg dur 20 -> len 2
	// band: min 20 -> cell 2; maxEnd 60 -> cell 6
	bar := RenderShapeBar(b, 100, 10, false)
	if bar != "··▒██▒····" {
		t.Fatalf("bar = %q", bar)
	}
}

func TestRenderShapeTable(t *testing.T) {
	buckets, used := ComputeShape(shapeSpans(), EntryMatch{EntryOnly: true})
	var buf bytes.Buffer
	RenderShape(&buf, buckets, used, 5, 20, false)
	out := buf.String()
	if !strings.Contains(out, "2 trace(s), 5 span(s)") {
		t.Fatalf("header: %q", out)
	}
	if !strings.Contains(out, "legend:") {
		t.Fatal("legend missing")
	}
	if !strings.Contains(out, "cart/POST /add") || !strings.Contains(out, "  cart/HGET") {
		t.Fatalf("tree labels: %q", out)
	}
	// hget appears in both traces -> presence shown as bare "2"; entry likewise
	if strings.Contains(out, "2/2") {
		t.Fatalf("full presence must be bare count: %q", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatal("color=false must not emit ANSI")
	}
}
```

Run: `go test ./internal/traces -run 'TestComputeShape|TestFilterMin|TestShapeRows|TestRenderShape' -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/traces/shape.go`:

```go
package traces

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	hopSep  = "\x1f"
	pathSep = "\x1e"
)

type ShapeBucket struct {
	Identity  string
	Parent    string
	Service   string
	Name      string
	Offsets   []int64
	Durations []int64
	TraceIDs  map[string]bool
	Errors    int
}

func (b *ShapeBucket) add(traceID string, offset, duration int64, isErr bool) {
	if offset < 0 {
		offset = 0
	}
	if duration < 0 {
		duration = 0
	}
	b.Offsets = append(b.Offsets, offset)
	b.Durations = append(b.Durations, duration)
	b.TraceIDs[traceID] = true
	if isErr {
		b.Errors++
	}
}

func (b *ShapeBucket) NSamples() int { return len(b.Offsets) }
func (b *ShapeBucket) NTraces() int  { return len(b.TraceIDs) }
func (b *ShapeBucket) Depth() int {
	if b.Identity == "" {
		return 0
	}
	return strings.Count(b.Identity, pathSep)
}

func (b *ShapeBucket) AvgOffset() int64   { return mean(b.Offsets) }
func (b *ShapeBucket) AvgDuration() int64 { return mean(b.Durations) }

func (b *ShapeBucket) MinOffset() int64 {
	if len(b.Offsets) == 0 {
		return 0
	}
	m := b.Offsets[0]
	for _, o := range b.Offsets[1:] {
		if o < m {
			m = o
		}
	}
	return m
}

// MaxEnd is the latest (offset+duration) pair seen — NOT max(offset)+max(dur).
func (b *ShapeBucket) MaxEnd() int64 {
	var m int64
	for i := range b.Offsets {
		if e := b.Offsets[i] + b.Durations[i]; e > m {
			m = e
		}
	}
	return m
}

func mean(vals []int64) int64 {
	if len(vals) == 0 {
		return 0
	}
	var sum int64
	for _, v := range vals {
		sum += v
	}
	return sum / int64(len(vals))
}

type EntryMatch struct {
	EntryOnly     bool
	Service       string
	Operation     string
	ErrorsOnly    bool
	MinDurationMS float64
}

func (m EntryMatch) Active() bool {
	return m.EntryOnly || m.Service != "" || m.Operation != "" || m.ErrorsOnly || m.MinDurationMS > 0
}

func (m EntryMatch) Matches(s Span) bool {
	if m.EntryOnly && s.Kind != "SERVER" {
		return false
	}
	if m.Service != "" && s.Service != m.Service {
		return false
	}
	if m.Operation != "" && s.Name != m.Operation {
		return false
	}
	if m.ErrorsOnly && !s.IsError() {
		return false
	}
	if m.MinDurationMS > 0 && float64(s.DurationNS) < m.MinDurationMS*1e6 {
		return false
	}
	return true
}

// ComputeShape groups sampled spans by trace, re-roots each trace at its
// anchor (earliest matching span, else client-side root, else earliest),
// and merges identical (service,name) paths across traces into buckets.
// Identity is computed top-down during traversal — no parent-walk, no
// span mutation (v1's mutate/restore eliminated; results identical).
func ComputeShape(spans []Span, match EntryMatch) (map[string]*ShapeBucket, int) {
	byTrace := map[string][]Span{}
	for _, s := range spans {
		byTrace[s.TraceID] = append(byTrace[s.TraceID], s)
	}
	buckets := map[string]*ShapeBucket{}
	tracesUsed := 0
	traceIDs := make([]string, 0, len(byTrace))
	for id := range byTrace {
		traceIDs = append(traceIDs, id)
	}
	sort.Strings(traceIDs) // deterministic iteration
	for _, traceID := range traceIDs {
		tspans := byTrace[traceID]
		anchor, ok := pickAnchor(tspans, match)
		if !ok {
			continue
		}
		tracesUsed++
		children := map[string][]Span{}
		for _, s := range tspans {
			children[s.ParentSpanID] = append(children[s.ParentSpanID], s)
		}
		t0 := anchor.StartNS
		type node struct {
			span Span
			path string
		}
		seen := map[string]bool{}
		stack := []node{{anchor, hop(anchor)}}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if n.span.SpanID != "" {
				if seen[n.span.SpanID] {
					continue
				}
				seen[n.span.SpanID] = true
			}
			b := buckets[n.path]
			if b == nil {
				parent := ""
				if i := strings.LastIndex(n.path, pathSep); i >= 0 {
					parent = n.path[:i]
				}
				b = &ShapeBucket{Identity: n.path, Parent: parent,
					Service: n.span.Service, Name: n.span.Name,
					TraceIDs: map[string]bool{}}
				buckets[n.path] = b
			}
			offset := int64(0)
			if n.span.StartNS != 0 {
				offset = n.span.StartNS - t0
			}
			b.add(traceID, offset, n.span.DurationNS, n.span.IsError())
			for _, kid := range children[n.span.SpanID] {
				if kid.SpanID == n.span.SpanID {
					continue // self-reference guard
				}
				stack = append(stack, node{kid, n.path + pathSep + hop(kid)})
			}
		}
	}
	return buckets, tracesUsed
}

func hop(s Span) string { return s.Service + hopSep + s.Name }

func pickAnchor(tspans []Span, match EntryMatch) (Span, bool) {
	if len(tspans) == 0 {
		return Span{}, false
	}
	if match.Active() {
		var best Span
		found := false
		for _, s := range tspans {
			if match.Matches(s) && (!found || s.StartNS < best.StartNS) {
				best, found = s, true
			}
		}
		if found {
			return best, true
		}
	}
	byID := map[string]bool{}
	for _, s := range tspans {
		if s.SpanID != "" {
			byID[s.SpanID] = true
		}
	}
	var root Span
	found := false
	for _, s := range tspans {
		if !byID[s.ParentSpanID] && (!found || s.StartNS < root.StartNS) {
			root, found = s, true
		}
	}
	if found {
		return root, true
	}
	earliest := tspans[0]
	for _, s := range tspans[1:] {
		if s.StartNS < earliest.StartNS {
			earliest = s
		}
	}
	return earliest, true
}

func FilterMinTraces(buckets map[string]*ShapeBucket, min int) map[string]*ShapeBucket {
	out := map[string]*ShapeBucket{}
	for k, b := range buckets {
		if b.NTraces() >= min {
			out[k] = b
		}
	}
	return out
}

func ShapeRows(buckets map[string]*ShapeBucket) []map[string]any {
	list := sortedBuckets(buckets, func(a, b *ShapeBucket) bool {
		if a.Depth() != b.Depth() {
			return a.Depth() < b.Depth()
		}
		return a.AvgOffset() < b.AvgOffset()
	})
	rows := make([]map[string]any, 0, len(list))
	for _, b := range list {
		var parent any
		if b.Parent != "" {
			lastHop := b.Parent
			if i := strings.LastIndex(lastHop, pathSep); i >= 0 {
				lastHop = lastHop[i+1:]
			}
			parent = strings.ReplaceAll(lastHop, hopSep, "/")
		}
		rows = append(rows, map[string]any{
			"service": b.Service, "name": b.Name, "depth": b.Depth(),
			"parent": parent, "samples": b.NSamples(), "traces": b.NTraces(),
			"avg_offset_ns": b.AvgOffset(), "avg_duration_ns": b.AvgDuration(),
			"min_offset_ns": b.MinOffset(), "max_end_ns": b.MaxEnd(),
			"errors": b.Errors,
		})
	}
	return rows
}

func sortedBuckets(buckets map[string]*ShapeBucket, less func(a, b *ShapeBucket) bool) []*ShapeBucket {
	list := make([]*ShapeBucket, 0, len(buckets))
	for _, b := range buckets {
		list = append(list, b)
	}
	sort.SliceStable(list, func(i, j int) bool { return less(list[i], list[j]) })
	return list
}

// RenderShapeBar: █ = average position/duration (priority), ▒ = min/max
// spread band, · = outside. Band clamped into [0,width]; avg segment
// truncated by the loop bound (extraction §4.8).
func RenderShapeBar(b *ShapeBucket, axisEnd int64, width int, color bool) string {
	span := axisEnd
	if span < 1 {
		span = 1
	}
	avgLeft := int(b.AvgOffset() * int64(width) / span)
	avgLen := int(b.AvgDuration() * int64(width) / span)
	if avgLen < 1 {
		avgLen = 1
	}
	avgRight := avgLeft + avgLen

	minOff := b.MinOffset()
	if minOff < 0 {
		minOff = 0
	}
	maxEnd := b.MaxEnd()
	if maxEnd < minOff+1 {
		maxEnd = minOff + 1
	}
	bandLeft := int(minOff * int64(width) / span)
	bandRight := int(maxEnd * int64(width) / span)
	if bandRight < bandLeft+1 {
		bandRight = bandLeft + 1
	}
	if bandLeft > width-1 {
		bandLeft = width - 1
	}
	if bandRight > width {
		bandRight = width
	}

	var sb strings.Builder
	for i := 0; i < width; i++ {
		switch {
		case i >= avgLeft && i < avgRight:
			if color {
				c := ansiGreen
				if b.Errors > 0 {
					c = ansiRed
				}
				sb.WriteString(c + "█" + ansiReset)
			} else {
				sb.WriteString("█")
			}
		case i >= bandLeft && i < bandRight:
			if color {
				sb.WriteString(ansiDim + ansiCyan + "▒" + ansiReset)
			} else {
				sb.WriteString("▒")
			}
		default:
			if color {
				sb.WriteString(ansiDim + "·" + ansiReset)
			} else {
				sb.WriteString("·")
			}
		}
	}
	return sb.String()
}

func RenderShape(w io.Writer, buckets map[string]*ShapeBucket, tracesUsed, totalSpans, width int, color bool) {
	var axisEnd int64 = 1
	for _, b := range buckets {
		if e := b.AvgOffset() + b.AvgDuration(); e > axisEnd {
			axisEnd = e
		}
		if e := b.MaxEnd(); e > axisEnd {
			axisEnd = e
		}
	}
	fmt.Fprintf(w, "%d trace(s), %d span(s) · axis 0 → %s\n",
		tracesUsed, totalSpans, FormatDurationNS(axisEnd))
	fmt.Fprintln(w, "legend: █ avg position · ▒ min/max spread · · before/after")

	children := map[string][]*ShapeBucket{}
	for _, b := range buckets {
		children[b.Parent] = append(children[b.Parent], b)
	}
	for k := range children {
		kids := children[k]
		sort.SliceStable(kids, func(i, j int) bool {
			if kids[i].AvgOffset() != kids[j].AvgOffset() {
				return kids[i].AvgOffset() < kids[j].AvgOffset()
			}
			return kids[i].AvgDuration() > kids[j].AvgDuration()
		})
	}
	roots := children[""]
	if len(roots) == 0 && len(buckets) > 0 {
		minDepth := -1
		for _, b := range buckets {
			if minDepth == -1 || b.Depth() < minDepth {
				minDepth = b.Depth()
			}
		}
		for _, b := range buckets {
			if b.Depth() == minDepth {
				roots = append(roots, b)
			}
		}
		sort.SliceStable(roots, func(i, j int) bool { return roots[i].AvgOffset() < roots[j].AvgOffset() })
	}

	maxLabel := 0
	for _, b := range buckets {
		if l := len(b.Service) + len(b.Name) + 1 + 2*b.Depth(); l > maxLabel {
			maxLabel = l
		}
	}
	nameCol := maxLabel + 2
	if nameCol > 60 {
		nameCol = 60
	}
	if nameCol < 16 {
		nameCol = 16
	}
	anyErrors := false
	for _, b := range buckets {
		if b.Errors > 0 {
			anyErrors = true
		}
	}

	type sframe struct {
		b     *ShapeBucket
		depth int
	}
	stack := make([]sframe, 0, len(buckets))
	for i := len(roots) - 1; i >= 0; i-- {
		stack = append(stack, sframe{roots[i], 0})
	}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		b := f.b
		label := strings.Repeat("  ", f.depth) + b.Service + "/" + b.Name
		if len(label) > nameCol {
			label = label[:nameCol-1] + "…"
		}
		presence := fmt.Sprintf("%d", b.NTraces())
		if b.NTraces() < tracesUsed {
			presence = fmt.Sprintf("%d/%d", b.NTraces(), tracesUsed)
		}
		errCell := ""
		if anyErrors && b.Errors > 0 {
			errCell = fmt.Sprintf(" %d", b.Errors)
		}
		fmt.Fprintf(w, "%-*s %s %9s %9s %7s%s\n", nameCol, label,
			RenderShapeBar(b, axisEnd, width, color),
			FormatDurationNS(b.AvgDuration()), FormatDurationNS(b.AvgOffset()),
			presence, errCell)
		kids := children[b.Identity]
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, sframe{kids[i], f.depth + 1})
		}
	}
}
```

Run: `go test ./internal/traces -v` — Expected: PASS.

- [ ] **Step 3: Commit**

```bash
gofmt -l internal && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/traces
git commit -m "feat: aggregated shape waterfall (buckets, stats, glyph rendering)"
```

---

### Task 6: `bronto traces` command tree

**Files:**
- Create: `internal/cli/traces.go`, `internal/cli/traces_test.go`
- Modify: `internal/cli/root.go` (register `newTracesCmd()`)

**Interfaces:**
- Produces: `newTracesCmd() *cobra.Command` with six subcommands. Shared plumbing per subcommand: `NewApp`, `timerange.Resolve(since,"","",nil)` (defaults: 15m for services/operations/aggregate/list; 1h for show/shape), `traces.Aggregator{Client: bronto.NewClient(app.HTTPClient, app.Config.BaseURL()), Time: spec}`.
  - `traces services [--errors] [-n 50] [--since 15m]` → rows via `Printer(false).PrintRows([]string{"service","spans","avg","max"}, rows)` (machine mode gets all fields incl. `avg_ns`/`max_ns` since PrintRows JSON emits full row maps)
  - `traces operations [-s service] [--errors] [-n 25] [--since 15m]` → columns `service,operation,spans,avg,max`
  - `traces aggregate --by attr [--by ...] [--root-only|--all-spans] [-s svc] [--kind k] [--errors] [--where raw] [--include-empty] [-n 50] [--since 15m]` → `Attributes(...)`; columns from the returned `columns`; when zero rows AND RootOnly AND dropped>0, TTY+!quiet hint to stderr: "Root spans on ingress/proxy services often lack app attributes — try --all-spans --kind server --service <name>." `--by` required (`MarkFlagRequired`).
  - `traces list [-s svc] [--operation op] [--min-duration-ms N] [--errors] [-n 50] [--since 15m]` → columns `@time,service,operation,duration,status,trace_id`
  - `traces show <trace-id> [--since 1h] [-n 500] [--bar-width 40]` → fetch via `FetchTraceSpans(ctx, []string{traceID})` with the Aggregator's Time set from --since... IMPORTANT: `FetchTraceSpans` limit is 5000 per batch; the `-n` flag caps rendered spans client-side (`spans = spans[:n]` after sort? NO — cap the fetch instead: for a single trace use a direct search). Implement show's fetch as its own single query: `Where: "$span.trace_id = " + Quote(id)`, `Select: append([]string{"@time"}, traces.SpanFields...)`, `Limit: n`, `MostRecentFirst: &false` — matching extraction §3. Empty result → `clierr.New("trace_not_found", ...)` (exit 4 via `_not_found` suffix). TTY: `RenderWaterfall(app.Stdout, spans, barWidth, app.Color)`; piped/`-o json|jsonl`: `WaterfallRows` through the printer (streaming rows).
  - `traces shape [-s svc] [--operation op] [--where raw] [--errors] [--min-duration-ms N] [--entry|--any-span] [--sample 30] [--min-traces 1] [--since 1h] [--bar-width 50]` → build the sample where-clause exactly as extraction §4.1 (kind clause when entry; service; operation; errors; duration; parenthesized raw where — reuse `traces.AndJoin`/`Quote`/`KindClause`); `FindSampleTraceIDs`; empty → `clierr.New("traces_not_found", "no traces matched the filter")` — NOTE: use code `trace_not_found` (exit 4) for consistency; `FetchTraceSpans`; empty → `trace_not_found` with hint about the time window; `ComputeShape` with `EntryMatch{...}`; `FilterMinTraces`; empty → `usage_min_traces_too_high` (exit 2) with hint. TTY: `RenderShape`; piped: `ShapeRows` via printer.
- Consumes: everything above.

- [ ] **Step 1: Failing tests**

`internal/cli/traces_test.go`:

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

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// tracesServer routes /search responses by the first select entry.
func tracesServer(t *testing.T, bySelect map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		sel, _ := body["select"].([]any)
		key := ""
		if len(sel) > 0 {
			key = sel[0].(string)
		}
		resp, ok := bySelect[key]
		if !ok {
			resp = `{"result":[]}`
		}
		_, _ = w.Write([]byte(resp))
	}))
}

func runTraces(t *testing.T, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	full := append([]string{"traces"}, args...)
	full = append(full, "--base-url", srv.URL, "--api-key", "k")
	root.SetArgs(full)
	err := root.Execute()
	return out.String(), err
}

func TestTracesServicesJSON(t *testing.T) {
	srv := tracesServer(t, map[string]string{
		"count(*)":                 `{"groups":[{"group":["cart"],"count(*)":9}]}`,
		"avg($span.duration_nano)": `{"groups":[{"group":["cart"],"avg($span.duration_nano)":1000000}]}`,
		"max($span.duration_nano)": `{"groups":[{"group":["cart"],"max($span.duration_nano)":2000000}]}`,
	})
	defer srv.Close()
	out, err := runTraces(t, srv, "services", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || rows[0]["service"] != "cart" {
		t.Fatalf("out = %q", out)
	}
	if rows[0]["avg"] != "1.00ms" || rows[0]["avg_ns"] != float64(1000000) {
		t.Fatalf("row = %v", rows[0])
	}
}

func TestTracesAggregateRequiresBy(t *testing.T) {
	srv := tracesServer(t, nil)
	defer srv.Close()
	_, err := runTraces(t, srv, "aggregate")
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v", err)
	}
}

func TestTracesShowStreamsRows(t *testing.T) {
	srv := tracesServer(t, map[string]string{
		"@time": `{"result":[
			{"$span.trace_id":"tr1","$span.span_id":"a","$span.name":"root","$service.name":"cart",
			 "$span.start_time_unix_nano":100,"$span.duration_nano":50,"$span.status_code":"STATUS_CODE_OK"},
			{"$span.trace_id":"tr1","$span.span_id":"b","$span.parent_span_id":"a","$span.name":"child",
			 "$service.name":"cart","$span.start_time_unix_nano":110,"$span.duration_nano":20,
			 "$span.status_code":"STATUS_CODE_UNSET"}]}`,
	})
	defer srv.Close()
	out, err := runTraces(t, srv, "show", "tr1")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 jsonl rows, got %q", out)
	}
	var row0 map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &row0)
	if row0["depth"] != float64(0) || row0["operation"] != "root" {
		t.Fatalf("row0 = %v", row0)
	}
}

func TestTracesShowNotFound(t *testing.T) {
	srv := tracesServer(t, nil) // empty result
	defer srv.Close()
	_, err := runTraces(t, srv, "show", "missing-trace")
	if err == nil || clierr.ExitCode(err) != 4 {
		t.Fatalf("want exit 4, got %v (%d)", err, clierr.ExitCode(err))
	}
}

func TestTracesShapeJSON(t *testing.T) {
	// Both FindSampleTraceIDs and FetchTraceSpans use select[0] ==
	// "$span.trace_id", so route by "limit" instead: sampling requests
	// max(sample*3, 30); the span fetch always requests 5000.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		if body["limit"] == float64(5000) {
			_, _ = w.Write([]byte(`{"result":[
				{"$span.trace_id":"t1","$span.span_id":"a1","$span.name":"POST /x","$service.name":"web",
				 "$span.kind":"SPAN_KIND_SERVER","$span.start_time_unix_nano":100,"$span.duration_nano":50},
				{"$span.trace_id":"t2","$span.span_id":"a2","$span.name":"POST /x","$service.name":"web",
				 "$span.kind":"SPAN_KIND_SERVER","$span.start_time_unix_nano":900,"$span.duration_nano":70}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":[{"$span.trace_id":"t1"},{"$span.trace_id":"t2"}]}`))
	}))
	defer srv.Close()
	out, err := runTraces(t, srv, "shape", "--sample", "2", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("out = %q", out)
	}
	if rows[0]["traces"] != float64(2) || rows[0]["name"] != "POST /x" {
		t.Fatalf("row = %v", rows[0])
	}
}

func TestTracesListColumns(t *testing.T) {
	srv := tracesServer(t, map[string]string{
		"@time": `{"result":[{"@time":"t1","$span.trace_id":"tr","$span.span_id":"sp",
			"$span.name":"op","$service.name":"svc","$span.duration_nano":3000000,
			"$span.status_code":"STATUS_CODE_OK"}]}`,
	})
	defer srv.Close()
	out, err := runTraces(t, srv, "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || rows[0]["duration"] != "3.00ms" {
		t.Fatalf("out = %q", out)
	}
}
```

Run: `go test ./internal/cli -run TestTraces -v` — Expected: FAIL.

- [ ] **Step 2: Implement `internal/cli/traces.go`**

Structure (write clean, complete Go — signatures below are binding; bodies follow the patterns of search.go/tail.go):

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/bronto-community/bronto-cli/internal/timerange"
	"github.com/bronto-community/bronto-cli/internal/traces"
)

func newTracesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "traces",
		Short: "Explore OpenTelemetry traces (APM-style views over the .traces logset)",
	}
	cmd.AddCommand(newTracesServicesCmd(), newTracesOperationsCmd(),
		newTracesAggregateCmd(), newTracesListCmd(), newTracesShowCmd(), newTracesShapeCmd())
	return cmd
}

// tracesAgg builds the shared App + Aggregator pair.
func tracesAgg(cmd *cobra.Command, since, defaultSince string) (*App, *traces.Aggregator, error) {
	app, err := NewApp(cmd)
	if err != nil {
		return nil, nil, err
	}
	if since == "" {
		since = defaultSince
	}
	spec, err := timerange.Resolve(since, "", "", nil)
	if err != nil {
		return nil, nil, err
	}
	agg := &traces.Aggregator{
		Client: bronto.NewClient(app.HTTPClient, app.Config.BaseURL()),
		Time:   spec,
	}
	return app, agg, nil
}
```

Each subcommand follows this template — `services` shown fully; implement the other five analogously per the Interfaces block above:

```go
func newTracesServicesCmd() *cobra.Command {
	var since string
	var errorsOnly bool
	var limit int
	cmd := &cobra.Command{
		Use:   "services",
		Short: "Span counts and latency per service",
		Example: "  bronto traces services --since 15m\n  bronto traces services --errors",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, agg, err := tracesAgg(cmd, since, "15m")
			if err != nil {
				return err
			}
			rows, err := agg.Services(cmd.Context(), errorsOnly, limit)
			if err != nil {
				return err
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows([]string{"service", "spans", "avg", "max"}, rows)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "relative lookback (default 15m)")
	cmd.Flags().BoolVar(&errorsOnly, "errors", false, "only error spans")
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "max services")
	return cmd
}
```

Binding details for the remaining five:
- `operations`: flags `-s/--service`, `--errors`, `-n` (25), `--since`; columns `service,operation,spans,avg,max`.
- `aggregate`: flags `-b/--by` (StringArray, `MarkFlagRequired`), `--root-only` (default true) + `--all-spans` (sets RootOnly=false; make them mutually exclusive via `cmd.MarkFlagsMutuallyExclusive("root-only", "all-spans")`), `-s/--service`, `-k/--kind`, `--errors`, `-w/--where`, `--include-empty`, `-n` (50), `--since`. After `Attributes`: if `len(rows)==0 && opts.RootOnly && dropped>0 && app.StdoutIsTTY && !app.Quiet` print the ingress-proxy hint to stderr. Print with the returned `columns`.
- `list`: flags `-s/--service`, `--operation`, `--min-duration-ms` (Float64), `--errors`, `-n` (50), `--since`; columns `@time,service,operation,duration,status,trace_id`.
- `show`: positional `trace-id` (ExactArgs(1)); flags `--since` (default 1h), `-n` (500), `--bar-width` (40). Own single search (see Interfaces). Empty → `clierr.New("trace_not_found", fmt.Sprintf("no spans found for trace %s", id)).WithHint("Widen the window with --since (default 1h).")`. TTY+table format → `traces.RenderWaterfall(app.Stdout, spans, barWidth, app.Color)`; else stream `traces.WaterfallRows(spans)` via `p.PrintRow` (jsonl) or `p.PrintRows` (json), mirroring search.go's `printEvents` format branching.
- `shape`: flags `-s/--service`, `--operation`, `-w/--where`, `--errors`, `--min-duration-ms`, `--entry` (default true) / `--any-span` (mutually exclusive), `--sample` (30), `--min-traces` (1), `--since` (1h), `--bar-width` (50). Pipeline per the Interfaces block. TTY+table → `traces.RenderShape(app.Stdout, visible, tracesUsed, len(spans), barWidth, app.Color)`; else `traces.ShapeRows(visible)` via printer.

Register in `NewRootCmd()`: `cmd.AddCommand(newTracesCmd())`.

- [ ] **Step 3: Verify and commit**

```bash
go test ./internal/cli -run TestTraces -v && go test ./...
CGO_ENABLED=0 go build ./... && gofmt -l internal cmd
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run
git add internal/cli
git commit -m "feat: bronto traces command tree (services/operations/aggregate/list/show/shape)"
```

---

## Verification (end of plan)

```bash
go test ./...
CGO_ENABLED=0 make build
./bronto traces --help && ./bronto traces shape --help
# Manual against real data (needs key + traced services):
BRONTO_API_KEY=<key> ./bronto traces services --since 15m
BRONTO_API_KEY=<key> ./bronto traces list --errors -n 5
BRONTO_API_KEY=<key> ./bronto traces show <trace-id-from-list>
BRONTO_API_KEY=<key> ./bronto traces shape -s <service> --sample 10
```

Manual acceptance: services/operations render merged aggregate tables; `show` draws an indented waterfall with clamped bars and error markers; `shape` draws the merged waterfall with █/▒/· glyphs and k/N presence; all six emit clean structured rows when piped.
