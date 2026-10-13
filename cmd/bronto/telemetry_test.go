package main

import (
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const (
	parentTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID  = "00f067aa0ba902b7"
)

// stubCollector accepts OTLP/HTTP protobuf trace exports and keeps the spans.
type stubCollector struct {
	mu    sync.Mutex
	spans []*tracepb.Span
	svc   []string
}

func (c *stubCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req collectortrace.ExportTraceServiceRequest
	if r.URL.Path != "/v1/traces" || proto.Unmarshal(body, &req) != nil {
		http.Error(w, "bad export", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rs := range req.GetResourceSpans() {
		for _, a := range rs.GetResource().GetAttributes() {
			if a.GetKey() == "service.name" {
				c.svc = append(c.svc, a.GetValue().GetStringValue())
			}
		}
		for _, ss := range rs.GetScopeSpans() {
			c.spans = append(c.spans, ss.GetSpans()...)
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(nil)
}

func TestTelemetryContinuesTraceparent(t *testing.T) {
	col := &stubCollector{}
	colSrv := httptest.NewServer(col)
	t.Cleanup(colSrv.Close)

	var mu sync.Mutex
	var gotTraceparent []string
	api := stubAPI()
	t.Cleanup(api.Close)
	recordingAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotTraceparent = append(gotTraceparent, r.Header.Get("traceparent"))
		mu.Unlock()
		api.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(recordingAPI.Close)

	// testscript runs scripts as parallel subtests; the group returns only
	// once they have finished, so the assertions below see their effects.
	t.Run("group", func(t *testing.T) {
		testscript.Run(t, testscript.Params{
			Dir: "testdata/telemetry",
			Setup: func(env *testscript.Env) error {
				env.Setenv("STUB_URL", recordingAPI.URL)
				env.Setenv("OTLP_URL", colSrv.URL)
				return nil
			},
		})
	})

	// The API saw the trace continued from TRACEPARENT, with a new parent
	// span id (the HTTP client span), not the caller's.
	if len(gotTraceparent) == 0 {
		t.Fatal("API received no requests")
	}
	for _, tp := range gotTraceparent {
		parts := strings.Split(tp, "-")
		if len(parts) != 4 || parts[1] != parentTraceID || parts[2] == parentSpanID {
			t.Errorf("API traceparent = %q, want trace %s with a new span id", tp, parentTraceID)
		}
	}

	col.mu.Lock()
	defer col.mu.Unlock()
	if len(col.svc) == 0 || col.svc[0] != "bronto-cli" {
		t.Errorf("service.name = %v, want bronto-cli", col.svc)
	}
	var root, client, apiRoot, apiClient *tracepb.Span
	for _, s := range col.spans {
		if hex.EncodeToString(s.GetTraceId()) != parentTraceID {
			t.Errorf("span %q has trace id %x, want %s", s.GetName(), s.GetTraceId(), parentTraceID)
		}
		for k, v := range attrs(s) {
			if strings.Contains(v, "secret-value") {
				t.Errorf("span %q attribute %s = %q leaks a --field value", s.GetName(), k, v)
			}
		}
		switch s.GetName() {
		case "bronto search":
			root = s
		case "bronto api":
			apiRoot = s
		}
	}
	for _, s := range col.spans {
		if s.GetKind() != tracepb.Span_SPAN_KIND_CLIENT {
			continue
		}
		switch {
		case root != nil && string(s.GetParentSpanId()) == string(root.GetSpanId()):
			client = s
		case apiRoot != nil && string(s.GetParentSpanId()) == string(apiRoot.GetSpanId()):
			apiClient = s
		}
	}
	if root == nil || client == nil || apiRoot == nil || apiClient == nil {
		t.Fatalf("want root and client spans for both runs, got %d spans", len(col.spans))
	}
	if root.GetName() != "bronto search" {
		t.Errorf("root span name = %q, want %q", root.GetName(), "bronto search")
	}
	if hex.EncodeToString(root.GetParentSpanId()) != parentSpanID {
		t.Errorf("root span parent = %x, want %s", root.GetParentSpanId(), parentSpanID)
	}
	if client.GetName() != "POST /search" {
		t.Errorf("client span name = %q, want %q", client.GetName(), "POST /search")
	}
	rootAttrs, clientAttrs := attrs(root), attrs(client)
	for k, want := range map[string]string{"bronto.command": "bronto search", "process.executable.name": "bronto"} {
		if rootAttrs[k] != want {
			t.Errorf("root span %s = %q, want %q", k, rootAttrs[k], want)
		}
	}
	// Arguments are recorded sanitized: flag names, safe values, the query's
	// shape. The leak check above covers the api key and the query literal.
	for k, want := range map[string]string{
		"bronto.flags":        "api-key,base-url,dataset,output,since",
		"bronto.flag.since":   "1h",
		"bronto.flag.dataset": "11111111-1111-1111-1111-111111111111",
		"bronto.args":         "user = ?",
	} {
		if rootAttrs[k] != want {
			t.Errorf("root span %s = %q, want %q", k, rootAttrs[k], want)
		}
	}
	if rootAttrs["process.pid"] == "" {
		t.Error("root span has no process.pid")
	}
	if clientAttrs["url.template"] != "/search" {
		t.Errorf("client span url.template = %q, want /search", clientAttrs["url.template"])
	}
	if got, want := attrs(apiClient)["url.full"], recordingAPI.URL+"/logs?where=REDACTED"; got != want {
		t.Errorf("api client span url.full = %q, want %q", got, want)
	}
}

// attrs flattens a span's attributes to strings for comparison; string
// arrays are joined with commas.
func attrs(s *tracepb.Span) map[string]string {
	m := map[string]string{}
	for _, a := range s.GetAttributes() {
		v := a.GetValue()
		switch {
		case v.GetStringValue() != "":
			m[a.GetKey()] = v.GetStringValue()
		case v.GetIntValue() != 0:
			m[a.GetKey()] = strconv.FormatInt(v.GetIntValue(), 10)
		case v.GetArrayValue() != nil:
			var vs []string
			for _, e := range v.GetArrayValue().GetValues() {
				vs = append(vs, e.GetStringValue())
			}
			m[a.GetKey()] = strings.Join(vs, ",")
		}
	}
	return m
}
