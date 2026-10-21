// Command demotraces sends synthetic OpenTelemetry traces for a small shop
// to a Bronto test org, so dashboards and trace commands have realistic
// data to work on (the agent eval in #195, docs examples, manual checks).
// Run it through scripts/test-env, which points it at the test org:
//
//	make demo-traces                                   # 1h of traffic, 60 traces/min
//	scripts/test-env go run ./internal/tools/demotraces -for 3h -rate 120
//	scripts/test-env go run ./internal/tools/demotraces -for 0 -burst 500
//
// Four services each land in their own .traces dataset: storefront-web,
// checkout-api, catalog-api and payments-gateway. A checkout request
// crosses all four; browse requests stop at catalog-api. Spans carry the
// OpenTelemetry HTTP attributes, and about 2% of payments fail (502 from
// the gateway, which makes the checkout a 500).
//
// Bronto stamps spans with the time it ingests them, not their start time
// (live-verified 2026-10-21), so traffic can't be backdated: for charts
// with a shape, the generator has to keep running. It sends -rate traces
// per minute, in a batch every 10 seconds, for -for (default 1h); -burst
// sends that many traces once first.
//
// It posts OTLP/HTTP JSON to the ingestion host's /v1/traces, the same way
// integration/seed_traces_test.go does, with BRONTO_API_KEY (which needs
// the IngestionApi role).
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"strings"
	"time"
)

// OTLP span kinds and status codes (opentelemetry-proto trace.proto).
const (
	kindServer = 2
	kindClient = 3
	statusOK   = 1
	statusErr  = 2
)

func main() {
	rate := flag.Int("rate", 60, "traces per minute while running")
	duration := flag.Duration("for", time.Hour, "keep sending for this long (0: only the -burst)")
	burst := flag.Int("burst", 0, "traces to send at once before the steady traffic")
	errRate := flag.Float64("error-rate", 0.02, "share of checkouts whose payment fails")
	flag.Parse()

	key := os.Getenv("BRONTO_API_KEY")
	region := os.Getenv("BRONTO_REGION")
	if key == "" || region == "" {
		log.Fatal("demotraces: BRONTO_API_KEY and BRONTO_REGION must be set; run it through scripts/test-env")
	}
	url := os.Getenv("BRONTO_INGEST_URL")
	if url == "" {
		url = "https://ingestion." + region + ".bronto.io"
	}
	url = strings.TrimRight(url, "/") + "/v1/traces"

	g := &generator{errRate: *errRate, rng: mrand.New(mrand.NewPCG(uint64(time.Now().UnixNano()), 0))} //nolint:gosec // synthetic latencies, not secrets
	sent, failed := 0, 0
	send := func(n int) {
		if n <= 0 {
			return
		}
		var b batchBuilder
		now := time.Now()
		for range n {
			// Start a little in the past so the spans end before "now".
			if g.trace(&b, now.Add(-g.ms(3000, 6000))) {
				failed++
			}
		}
		if err := post(url, key, b.body()); err != nil {
			log.Fatalf("demotraces: after %d traces: %v", sent, err)
		}
		sent += n
	}
	for left := *burst; left > 0; left -= 200 {
		send(min(left, 200))
	}
	const tick = 10 * time.Second
	perTick := float64(*rate) * tick.Seconds() / 60
	carry := 0.0
	for end := time.Now().Add(*duration); time.Now().Before(end); time.Sleep(tick) {
		carry += perTick
		n := int(carry)
		carry -= float64(n)
		send(n)
	}
	fmt.Printf("demotraces: sent %d traces (%d with a failed payment) to %s\n", sent, failed, url)
}

type generator struct {
	errRate float64
	rng     *mrand.Rand
}

// trace adds one user request to b and reports whether it failed.
func (g *generator) trace(b *batchBuilder, at time.Time) bool {
	t := &traceCtx{b: b, id: randHex(16)}
	switch r := g.rng.Float64(); {
	case r < 0.25:
		return g.checkout(t, at)
	case r < 0.75:
		g.browse(t, at, "GET /products/{id}")
	default:
		g.browse(t, at, "GET /products")
	}
	return false
}

// browse: storefront-web serves a page and reads from catalog-api.
func (g *generator) browse(t *traceCtx, at time.Time, route string) {
	catalog := g.ms(8, 30)
	total := catalog + g.ms(10, 40)
	root := t.span("storefront-web", "", "GET "+strings.TrimPrefix(route, "GET "), kindServer, at, total, 200, route)
	call := t.span("storefront-web", root, route, kindClient, at.Add(g.ms(1, 3)), catalog+g.ms(1, 2), 200, route)
	t.span("catalog-api", call, route, kindServer, at.Add(g.ms(2, 4)), catalog, 200, route)
}

// checkout: storefront-web -> checkout-api -> catalog-api (stock check)
// and payments-gateway (charge). A failed charge is a 502 from the
// gateway and a 500 from checkout-api and the storefront.
func (g *generator) checkout(t *traceCtx, at time.Time) bool {
	failed := g.rng.Float64() < g.errRate
	stock := g.ms(6, 20)
	charge := g.ms(80, 160)
	if g.rng.Float64() < 0.05 {
		charge += g.ms(300, 900) // a slow card network now and then
	}
	if failed {
		charge = g.ms(1000, 3000) // payment provider timeout
	}
	checkout := stock + charge + g.ms(10, 30)
	total := checkout + g.ms(15, 40)
	code, gw := 200, 200
	if failed {
		code, gw = 500, 502
	}
	root := t.span("storefront-web", "", "POST /checkout", kindServer, at, total, code, "/checkout")
	c1 := t.span("storefront-web", root, "POST /api/checkout", kindClient, at.Add(g.ms(2, 5)), checkout+g.ms(1, 3), code, "/api/checkout")
	srv := t.span("checkout-api", c1, "POST /api/checkout", kindServer, at.Add(g.ms(3, 6)), checkout, code, "/api/checkout")
	c2 := t.span("checkout-api", srv, "GET /stock/{sku}", kindClient, at.Add(g.ms(4, 8)), stock+g.ms(1, 2), 200, "/stock/{sku}")
	t.span("catalog-api", c2, "GET /stock/{sku}", kindServer, at.Add(g.ms(5, 9)), stock, 200, "/stock/{sku}")
	payAt := at.Add(stock + g.ms(10, 15))
	c3 := t.span("checkout-api", srv, "POST /charge", kindClient, payAt, charge+g.ms(1, 3), gw, "/charge")
	t.span("payments-gateway", c3, "POST /charge", kindServer, payAt.Add(g.ms(1, 2)), charge, gw, "/charge")
	return failed
}

// ms returns a random duration between lo and hi milliseconds.
func (g *generator) ms(lo, hi int) time.Duration {
	return time.Duration(lo)*time.Millisecond + time.Duration(g.rng.Int64N(int64(hi-lo+1)))*time.Millisecond
}

type traceCtx struct {
	b  *batchBuilder
	id string
}

// span adds a span and returns its id. Status follows the HTTP code: an
// error for 5xx on a server span, as the OpenTelemetry HTTP conventions
// say, and for any 5xx a client sees.
func (t *traceCtx) span(service, parent, name string, kind int, start time.Time, dur time.Duration, code int, route string) string {
	id := randHex(8)
	method, _, _ := strings.Cut(name, " ")
	status := statusOK
	s := map[string]any{
		"traceId": t.id, "spanId": id, "name": name, "kind": kind,
		"startTimeUnixNano": fmt.Sprint(start.UnixNano()),
		"endTimeUnixNano":   fmt.Sprint(start.Add(dur).UnixNano()),
		"attributes": []any{
			attr("http.request.method", method),
			attr("http.route", route),
			intAttr("http.response.status_code", code),
		},
	}
	if code >= 500 {
		status = statusErr
		s["attributes"] = append(s["attributes"].([]any), attr("error.type", fmt.Sprint(code)))
	}
	s["status"] = map[string]any{"code": status}
	if parent != "" {
		s["parentSpanId"] = parent
	}
	t.b.add(service, s)
	return id
}

func attr(k, v string) map[string]any {
	return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
}

func intAttr(k string, v int) map[string]any {
	return map[string]any{"key": k, "value": map[string]any{"intValue": fmt.Sprint(v)}}
}

// batchBuilder groups spans by service into one ExportTraceServiceRequest.
type batchBuilder struct {
	order []string
	spans map[string][]any
}

func (b *batchBuilder) add(service string, span map[string]any) {
	if b.spans == nil {
		b.spans = map[string][]any{}
	}
	if _, ok := b.spans[service]; !ok {
		b.order = append(b.order, service)
	}
	b.spans[service] = append(b.spans[service], span)
}

func (b *batchBuilder) body() map[string]any {
	rs := make([]any, 0, len(b.order))
	for _, svc := range b.order {
		rs = append(rs, map[string]any{
			"resource":   map[string]any{"attributes": []any{attr("service.name", svc), attr("deployment.environment.name", "demo")}},
			"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "bronto-cli-demotraces"}, "spans": b.spans[svc]}},
		})
	}
	return map[string]any{"resourceSpans": rs}
}

func post(url, key string, body map[string]any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data)) //nolint:gosec // the test org's ingestion URL, from scripts/test-env
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-bronto-api-key", key)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // see above
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("OTLP ingestion answered %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
