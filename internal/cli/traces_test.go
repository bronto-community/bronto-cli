package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestTracesAggregateRejectsBadKind(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"traces", "aggregate", "--by", "x", "--kind", "sideways", "--api-key", "k"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2 (no network), got %v", err)
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

// TestTracesShowAppliesFieldsFilter pins: printWaterfall builds its printer
// through App.PrinterFor (not output.NewPrinter directly), so --fields is
// honored instead of silently ignored — each streamed row has only the
// selected keys.
func TestTracesShowAppliesFieldsFilter(t *testing.T) {
	srv := tracesServer(t, map[string]string{
		"@time": `{"result":[
			{"$span.trace_id":"tr1","$span.span_id":"a","$span.name":"root","$service.name":"cart",
			 "$span.start_time_unix_nano":100,"$span.duration_nano":50,"$span.status_code":"STATUS_CODE_OK"},
			{"$span.trace_id":"tr1","$span.span_id":"b","$span.parent_span_id":"a","$span.name":"child",
			 "$service.name":"cart","$span.start_time_unix_nano":110,"$span.duration_nano":20,
			 "$span.status_code":"STATUS_CODE_UNSET"}]}`,
	})
	defer srv.Close()
	out, err := runTraces(t, srv, "show", "tr1", "--fields", "service,operation")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 jsonl rows, got %q", out)
	}
	for _, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("line %q not JSON: %v", line, err)
		}
		if len(row) != 2 {
			t.Fatalf("row has keys beyond the field filter: %+v", row)
		}
		if _, ok := row["service"]; !ok {
			t.Fatalf("row missing service: %+v", row)
		}
		if _, ok := row["operation"]; !ok {
			t.Fatalf("row missing operation: %+v", row)
		}
	}
}

// TestTracesShowRejectsFieldsOnTTYWaterfall pins: the waterfall renderer
// (chosen when stdout is a TTY and no -o override applies) is a bespoke
// human view that draws bars directly, bypassing the Printer's --fields
// support entirely. Silently ignoring --fields there would look like a bug
// (the flag would have zero effect); reject it instead, same as -o raw.
// The guard must fire before any network call.
func TestTracesShowRejectsFieldsOnTTYWaterfall(t *testing.T) {
	old := stdoutIsTTY
	stdoutIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY = old })

	var networkCallCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkCallCount.Add(1)
		t.Error("unexpected network call when guard should have fired early")
		_, _ = w.Write([]byte(`{"result":[]}`))
	}))
	defer srv.Close()
	_, err := runTraces(t, srv, "show", "tr1", "--fields", "service")
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v", err)
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_invalid_flags" {
		t.Fatalf("want usage_invalid_flags, got %v", err)
	}
	if !strings.Contains(ce.Hint, "json") {
		t.Fatalf("hint = %q, want it to mention -o json/jsonl", ce.Hint)
	}
	if networkCallCount.Load() != 0 {
		t.Fatalf("want no network calls, got %d", networkCallCount.Load())
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

// TestTracesShapeRejectsFieldsOnTTYRender mirrors
// TestTracesShowRejectsFieldsOnTTYWaterfall for 'traces shape', whose TTY
// render (traces.RenderShape) is likewise a bespoke view that bypasses the
// Printer's --fields support. The guard must fire before any network call.
func TestTracesShapeRejectsFieldsOnTTYRender(t *testing.T) {
	old := stdoutIsTTY
	stdoutIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY = old })

	var networkCallCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkCallCount.Add(1)
		t.Error("unexpected network call when guard should have fired early")
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		if body["limit"] == float64(5000) {
			_, _ = w.Write([]byte(`{"result":[
				{"$span.trace_id":"t1","$span.span_id":"a1","$span.name":"POST /x","$service.name":"web",
				 "$span.kind":"SPAN_KIND_SERVER","$span.start_time_unix_nano":100,"$span.duration_nano":50}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":[{"$span.trace_id":"t1"}]}`))
	}))
	defer srv.Close()
	_, err := runTraces(t, srv, "shape", "--sample", "1", "--fields", "name")
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v", err)
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_invalid_flags" {
		t.Fatalf("want usage_invalid_flags, got %v", err)
	}
	if !strings.Contains(ce.Hint, "json") {
		t.Fatalf("hint = %q, want it to mention -o json/jsonl", ce.Hint)
	}
	if networkCallCount.Load() != 0 {
		t.Fatalf("want no network calls, got %d", networkCallCount.Load())
	}
}

func TestTracesAggregateRejectsNonPositiveLimit(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"traces", "aggregate", "--by", "http.route", "-n", "-1", "--api-key", "k"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2 (not a panic), got %v", err)
	}
}

func TestTracesShapeRejectsNonPositiveSample(t *testing.T) {
	for _, args := range [][]string{
		{"traces", "shape", "--sample", "0", "--api-key", "k"},
		{"traces", "shape", "--min-traces", "0", "--api-key", "k"},
	} {
		root := NewRootCmd()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(args)
		err := root.Execute()
		if err == nil || clierr.ExitCode(err) != 2 {
			t.Fatalf("%v: want usage exit 2, got %v", args, err)
		}
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

func TestTracesListPipedDefaultIsJSONL(t *testing.T) {
	srv := tracesServer(t, map[string]string{
		"@time": `{"result":[
			{"@time":"t1","$span.trace_id":"tr1","$span.span_id":"sp1",
			 "$span.name":"op1","$service.name":"svc","$span.duration_nano":1000000,
			 "$span.status_code":"STATUS_CODE_OK"},
			{"@time":"t2","$span.trace_id":"tr2","$span.span_id":"sp2",
			 "$span.name":"op2","$service.name":"svc","$span.duration_nano":2000000,
			 "$span.status_code":"STATUS_CODE_OK"}]}`,
	})
	defer srv.Close()
	// No -o flag: piped (non-TTY) stdout must default to JSONL, like search.
	out, err := runTraces(t, srv, "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 jsonl lines, got %q", out)
	}
	for _, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("line %q not valid JSON: %v", line, err)
		}
	}
	var row0 map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &row0)
	if row0["duration"] != "1.00ms" {
		t.Fatalf("row0 = %v", row0)
	}
}

func TestTracesRejectsBadLimitAndBarWidth(t *testing.T) {
	for _, args := range [][]string{
		{"traces", "list", "-n", "0"},
		{"traces", "list", "-n", "-1"},
		{"traces", "show", "abc", "--bar-width", "0"},
		{"traces", "show", "abc", "--bar-width", "-5"},
		{"traces", "show", "abc", "--bar-width", "9"},
		{"traces", "shape", "--bar-width", "0"},
	} {
		root := NewRootCmd()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		// An unreachable base URL: validation must fail before any request.
		root.SetArgs(append(args, "--api-key", "k", "--base-url", "http://127.0.0.1:0"))
		err := root.Execute()
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != "usage_invalid_flag" {
			t.Fatalf("%v: want usage_invalid_flag, got %v", args, err)
		}
	}
}

func TestServiceFlagsResolve(t *testing.T) {
	ok := []struct{ service, dataset, want string }{
		{"cart", "", "cart"},
		{"", ".traces/checkout-api", "checkout-api"},
		{"", "checkout-api", "checkout-api"},
		{"", ".traces", ""},
		{"checkout-api", ".traces/checkout-api", "checkout-api"},
	}
	for _, c := range ok {
		got, err := (&serviceFlags{service: c.service, dataset: c.dataset}).resolve()
		if err != nil || got != c.want {
			t.Errorf("-s %q -d %q = %q, %v; want %q", c.service, c.dataset, got, err, c.want)
		}
	}
	bad := map[[2]string]string{
		{"", "web/nginx"}: "usage_invalid_flags",
		{"", "11111111-1111-1111-1111-111111111111"}: "usage_invalid_flags",
		{"cart", ".traces/checkout-api"}:              "usage_conflicting_flags",
	}
	for in, code := range bad {
		_, err := (&serviceFlags{service: in[0], dataset: in[1]}).resolve()
		if got := codeOf(err); got != code {
			t.Errorf("-s %q -d %q: code = %q (%v), want %q", in[0], in[1], got, err, code)
		}
	}
}

// -d .traces/<service> filters operations and services like -s does; a
// dataset outside .traces fails before any request.
func TestTracesDatasetAlias(t *testing.T) {
	for _, sub := range []string{"operations", "services"} {
		var wheres []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			where, _ := body["where"].(string)
			wheres = append(wheres, where)
			_, _ = w.Write([]byte(`{"groups":[]}`))
		}))
		if _, err := runTraces(t, srv, sub, "-d", ".traces/checkout-api", "-o", "json"); err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
		srv.Close()
		if len(wheres) == 0 || wheres[0] != "$service.name = 'checkout-api'" {
			t.Errorf("%s -d: wheres = %q", sub, wheres)
		}

		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Errorf("%s: no request expected for a non-trace dataset", sub)
		}))
		_, err := runTraces(t, srv, sub, "-d", "web/nginx")
		srv.Close()
		if codeOf(err) != "usage_invalid_flags" || !strings.Contains(err.Error(), ".traces") {
			t.Errorf("%s -d web/nginx: %v", sub, err)
		}
	}
}

func TestShapeSampleWhereMinDurationIsInclusive(t *testing.T) {
	where, err := shapeSampleWhere(false, "", "", false, 500, "")
	if err != nil {
		t.Fatal(err)
	}
	if where != "$span.duration_nano >= 500000000" {
		t.Fatalf("where = %q", where)
	}
}
