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
// crosses all four; browse requests stop at catalog-api; refunds and
// health checks give payments-gateway more than one operation. Spans carry the
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
	// Batches run on a fixed wall-clock schedule, so a slow ingestion
	// request delays a batch but doesn't lower the overall rate.
	const tick = 10 * time.Second
	perTick := float64(*rate) * tick.Seconds() / 60
	carry := 0.0
	start := time.Now()
	for next := start; next.Before(start.Add(*duration)); next = next.Add(tick) {
		time.Sleep(time.Until(next))
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

// trace adds one request to b and reports whether it failed. Most traces
// are user requests to storefront-web; a few are refunds started by
// checkout-api and load-balancer health checks on payments-gateway, so
// payments-gateway serves more than one operation.
func (g *generator) trace(b *batchBuilder, at time.Time) bool {
	t := &traceCtx{b: b, id: randHex(16)}
	switch r := g.rng.Float64(); {
	case r < 0.25:
		return g.checkout(t, at)
	case r < 0.70:
		g.browse(t, at, "/products/{id}")
	case r < 0.92:
		g.browse(t, at, "/products")
	case r < 0.96:
		g.refund(t, at)
	default:
		t.span("payments-gateway", "", "GET", "/health", kindServer, at, g.ms(1, 3), 200)
	}
	return false
}

// Timing: every server span lies inside the client span that called it
// (one network hop of 1-2 ms each way), and every client span inside the
// server span that made the call. Durations are computed bottom-up from
// the leaf services' own work, then the spans are emitted top-down.

// browse: storefront-web serves a page and reads from catalog-api.
func (g *generator) browse(t *traceCtx, at time.Time, route string) {
	net := g.ms(1, 2)
	catalog := g.ms(8, 30)
	call := catalog + 2*net
	pre, post := g.ms(2, 6), g.ms(3, 10)
	root := t.span("storefront-web", "", "GET", route, kindServer, at, pre+call+post, 200)
	c := t.span("storefront-web", root, "GET", route, kindClient, at.Add(pre), call, 200)
	t.span("catalog-api", c, "GET", route, kindServer, at.Add(pre+net), catalog, 200)
}

// checkout: storefront-web -> checkout-api, which checks stock with
// catalog-api and then charges through payments-gateway. A failed charge
// is a 502 from the gateway and a 500 from checkout-api and the
// storefront.
func (g *generator) checkout(t *traceCtx, at time.Time) bool {
	failed := g.rng.Float64() < g.errRate
	net := g.ms(1, 2)
	stock := g.ms(6, 20)
	charge := g.ms(80, 160)
	if g.rng.Float64() < 0.05 {
		charge += g.ms(300, 900) // a slow card network now and then
	}
	if failed {
		charge = g.ms(1000, 3000) // payment provider timeout
	}
	code, gw := 200, 200
	if failed {
		code, gw = 500, 502
	}
	stockCall, chargeCall := stock+2*net, charge+2*net
	pre2, gap, post2 := g.ms(2, 5), g.ms(3, 8), g.ms(5, 15)
	srv := pre2 + stockCall + gap + chargeCall + post2
	apiCall := srv + 2*net
	pre1, post1 := g.ms(3, 8), g.ms(5, 20)

	root := t.span("storefront-web", "", "POST", "/checkout", kindServer, at, pre1+apiCall+post1, code)
	c1 := t.span("storefront-web", root, "POST", "/api/checkout", kindClient, at.Add(pre1), apiCall, code)
	srvAt := at.Add(pre1 + net)
	s := t.span("checkout-api", c1, "POST", "/api/checkout", kindServer, srvAt, srv, code)
	stockAt := srvAt.Add(pre2)
	c2 := t.span("checkout-api", s, "GET", "/stock/{sku}", kindClient, stockAt, stockCall, 200)
	t.span("catalog-api", c2, "GET", "/stock/{sku}", kindServer, stockAt.Add(net), stock, 200)
	payAt := stockAt.Add(stockCall + gap)
	c3 := t.span("checkout-api", s, "POST", "/charge", kindClient, payAt, chargeCall, gw)
	t.span("payments-gateway", c3, "POST", "/charge", kindServer, payAt.Add(net), charge, gw)
	return failed
}

// refund: checkout-api refunds an order through payments-gateway.
func (g *generator) refund(t *traceCtx, at time.Time) {
	net := g.ms(1, 2)
	refund := g.ms(100, 250)
	call := refund + 2*net
	pre, post := g.ms(2, 5), g.ms(3, 8)
	s := t.span("checkout-api", "", "POST", "/api/refunds", kindServer, at, pre+call+post, 200)
	c := t.span("checkout-api", s, "POST", "/refunds", kindClient, at.Add(pre), call, 200)
	t.span("payments-gateway", c, "POST", "/refunds", kindServer, at.Add(pre+net), refund, 200)
}

// ms returns a random duration between lo and hi milliseconds.
func (g *generator) ms(lo, hi int) time.Duration {
	return time.Duration(lo)*time.Millisecond + time.Duration(g.rng.Int64N(int64(hi-lo+1)))*time.Millisecond
}

type traceCtx struct {
	b  *batchBuilder
	id string
}

// span adds a span named "<method> <route>" and returns its id; the
// http.route attribute holds the route alone. Status follows the HTTP
// code: an error for any 5xx, on the server and on the client that saw it.
func (t *traceCtx) span(service, parent, method, route string, kind int, start time.Time, dur time.Duration, code int) string {
	id := randHex(8)
	name := method + " " + route
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
