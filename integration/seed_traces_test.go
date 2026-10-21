package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- seeded trace fixture ----------------------------------------------------
//
// The dashboard tests (metric definitions, widgets) need spans to query, and
// the CI account has none of its own. seededTraces sends one small OTLP
// batch per test binary, lazily like seededData: two services (each lands
// in its own .traces dataset named after the service), with a server span,
// a client span, and an error span.
//
// It posts OTLP/HTTP JSON straight to the ingestion host's /v1/traces with
// the management key (which carries IngestionApi, see CONTRIBUTING), the
// same endpoint the telemetry test's exporter uses. `bronto send` can't do
// this: it sends log events, and spans only get their $span.* fields
// through OTLP (live-verified 2026-10-21: kind, status code and duration
// all arrive as SPAN_KIND_SERVER, STATUS_CODE_ERROR and $span.duration_nano).
//
// The service names come from resourceName, so the datasets they create
// are bronto-ci-<ts>-... and the sweeper deletes them an hour later.

type traceSeed struct {
	serviceA, serviceB string // .traces datasets, named after the services
	err                error
}

var (
	traceSeedOnce sync.Once
	traceSeedVal  traceSeed
)

// seededTraces returns the two seeded services, sending the batch on first
// use. It doesn't wait for the spans to be searchable: callers poll for the
// data they need (one poll, through the command under test).
func seededTraces(t *testing.T) (serviceA, serviceB string) {
	t.Helper()
	key := skipIfNoCreds(t)
	traceSeedOnce.Do(func() {
		traceSeedVal = doSeedTraces(key, regionOrDefault(), time.Now())
	})
	if traceSeedVal.err != nil {
		t.Fatalf("trace seed fixture: %v", traceSeedVal.err)
	}
	return traceSeedVal.serviceA, traceSeedVal.serviceB
}

func doSeedTraces(key, region string, now time.Time) traceSeed {
	s := traceSeed{serviceA: resourceName("svc-a"), serviceB: resourceName("svc-b")}
	body, err := json.Marshal(traceSeedBody(s.serviceA, s.serviceB, now))
	if err != nil {
		return traceSeed{err: err}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://ingestion."+region+".bronto.io/v1/traces", bytes.NewReader(body))
	if err != nil {
		return traceSeed{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-bronto-api-key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return traceSeed{err: fmt.Errorf("sending OTLP spans: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return traceSeed{err: fmt.Errorf("OTLP ingestion answered %s: %s", resp.Status, strings.TrimSpace(string(b)))}
	}
	return s
}

// OTLP span kinds and status codes (opentelemetry-proto trace.proto).
const (
	otlpKindServer = 2
	otlpKindClient = 3
	otlpStatusOK   = 1
	otlpStatusErr  = 2
)

// traceSeedBody is the OTLP/JSON ExportTraceServiceRequest: one trace in
// which serviceA serves GET /checkout (server span) and calls POST /pay
// (client span), and serviceB serves POST /pay and fails (error server
// span).
func traceSeedBody(serviceA, serviceB string, now time.Time) map[string]any {
	traceID := randHex(16)
	root, call, pay := randHex(8), randHex(8), randHex(8)
	span := func(id, parent, name string, kind, status int, dur time.Duration) map[string]any {
		s := map[string]any{
			"traceId": traceID, "spanId": id, "name": name, "kind": kind,
			"startTimeUnixNano": fmt.Sprint(now.Add(-dur).UnixNano()),
			"endTimeUnixNano":   fmt.Sprint(now.UnixNano()),
			"status":            map[string]any{"code": status},
		}
		if parent != "" {
			s["parentSpanId"] = parent
		}
		return s
	}
	service := func(name string, spans ...map[string]any) map[string]any {
		return map[string]any{
			"resource": map[string]any{"attributes": []any{map[string]any{
				"key": "service.name", "value": map[string]any{"stringValue": name}}}},
			"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "bronto-cli-integration"}, "spans": spans}},
		}
	}
	return map[string]any{"resourceSpans": []any{
		service(serviceA,
			span(root, "", "GET /checkout", otlpKindServer, otlpStatusOK, 120*time.Millisecond),
			span(call, root, "POST /pay", otlpKindClient, otlpStatusOK, 80*time.Millisecond)),
		service(serviceB,
			span(pay, call, "POST /pay", otlpKindServer, otlpStatusErr, 70*time.Millisecond)),
	}}
}

// TestTraceSeedBody_Shape is hermetic: the batch has the spans the
// dashboard tests rely on.
func TestTraceSeedBody_Shape(t *testing.T) {
	body := traceSeedBody("a", "b", time.Unix(1_800_000_000, 0))
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ResourceSpans []struct {
			Resource struct {
				Attributes []struct {
					Value struct {
						StringValue string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					TraceID string `json:"traceId"`
					Kind    int    `json:"kind"`
					Status  struct {
						Code int `json:"code"`
					} `json:"status"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var services []string
	kinds := map[int]int{}
	errs := 0
	traces := map[string]bool{}
	for _, rs := range doc.ResourceSpans {
		services = append(services, rs.Resource.Attributes[0].Value.StringValue)
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				kinds[sp.Kind]++
				traces[sp.TraceID] = true
				if sp.Status.Code == otlpStatusErr {
					errs++
				}
			}
		}
	}
	if strings.Join(services, ",") != "a,b" || kinds[otlpKindServer] != 2 || kinds[otlpKindClient] != 1 || errs != 1 || len(traces) != 1 {
		t.Fatalf("services %v, kinds %v, error spans %d, traces %d", services, kinds, errs, len(traces))
	}
}
