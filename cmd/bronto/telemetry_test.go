package main

import (
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
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
	var root, client *tracepb.Span
	for _, s := range col.spans {
		if hex.EncodeToString(s.GetTraceId()) != parentTraceID {
			t.Errorf("span %q has trace id %x, want %s", s.GetName(), s.GetTraceId(), parentTraceID)
		}
		switch s.GetKind() {
		case tracepb.Span_SPAN_KIND_CLIENT:
			client = s
		case tracepb.Span_SPAN_KIND_INTERNAL:
			root = s
		}
	}
	if root == nil || client == nil {
		t.Fatalf("want a root and a client span, got %d spans", len(col.spans))
	}
	if root.GetName() != "bronto search" {
		t.Errorf("root span name = %q, want %q", root.GetName(), "bronto search")
	}
	if hex.EncodeToString(root.GetParentSpanId()) != parentSpanID {
		t.Errorf("root span parent = %x, want %s", root.GetParentSpanId(), parentSpanID)
	}
	if string(client.GetParentSpanId()) != string(root.GetSpanId()) {
		t.Errorf("client span is not a child of the root span")
	}
}
