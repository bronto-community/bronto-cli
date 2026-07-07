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
