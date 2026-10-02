// Command genfixtures writes the bulky, generated part of the docs mock
// world: the log events under docs/testdata/mock/events/ and the spans
// under docs/testdata/mock/traces/. The small hand-written fixtures
// (datasets, monitors, users, ...) live next to them under api/ and are
// not touched by this program.
//
// The output is fully deterministic (fixed PCG seed, fixed clock), so
// re-running it on an unchanged generator is a no-op diff:
//
//	go run ./internal/tools/docsmock/genfixtures
//
// The story it encodes (see "The docs mock world" in docs/AGENTS.md):
// checkout-service in prod serves steady traffic from 09:00 to 09:30 UTC
// on 2026-07-19. From 09:19 payments-api slows down, and from 09:21:00 to
// 09:24:30 its payment provider times out, so checkout-service answers
// status=502. Every checkout-service request has a matching trace in the
// .traces logset; trace 4bf92f3577b34da6a3ce929d0e0e4736 is one of the
// failed ones.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ExampleTraceID is the trace the docs use in `bronto traces show`.
const ExampleTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

var (
	t0         = time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC)
	slowStart  = t0.Add(19 * time.Minute)
	burstStart = t0.Add(21 * time.Minute)
	burstEnd   = t0.Add(24*time.Minute + 30*time.Second)
	end        = t0.Add(30 * time.Minute)
	hosts      = []string{"web-1", "web-2", "web-3"}
)

// dataset log ids must match docs/testdata/mock/api/logs.json.
const (
	logCheckoutProd    = "0b7c6f2e-1d4a-4c8e-9a51-3f2d8e6b7a10"
	logPaymentsProd    = "5e2a9c41-7b3d-4f6a-8c20-9d1e4b5f6a21"
	logWebProd         = "8f1d3b7a-2c5e-4a9b-b6d4-1e7f9a3c2b32"
	logCheckoutStaging = "c3a8e5d2-9f4b-4e1a-a7c3-6b2d8f1e4c43"
)

type event struct {
	at     time.Time
	logID  string
	status string // @status: info|warn|error
	kvs    map[string]any
}

type span struct {
	traceID, spanID, parentID string
	name, kind, service       string
	route                     string
	httpStatus                int
	start                     time.Time
	dur                       time.Duration
	err                       bool
	extra                     map[string]any
}

type gen struct {
	rng    *rand.Rand
	events []event
	spans  []span
	nTrace int
}

func (g *gen) hex(n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[g.rng.IntN(16)]
	}
	return string(b)
}

func (g *gen) ms(lo, hi int) time.Duration {
	return time.Duration(lo+g.rng.IntN(hi-lo+1)) * time.Millisecond
}

func (g *gen) host() string { return hosts[g.rng.IntN(len(hosts))] }

// checkout emits one checkout request: the trace (web-frontend ->
// checkout-service -> payments-api) plus the matching log line in each
// service's dataset.
func (g *gen) checkout(start time.Time, traceID string) {
	phase := "ok"
	switch {
	case !start.Before(burstStart) && start.Before(burstEnd):
		phase = "timeout"
	case !start.Before(slowStart) && start.Before(burstStart):
		phase = "slow"
	}
	host := g.host()
	sid := func() string { return g.hex(16) }
	webID, coID, dbID, cliID, payID, provID := sid(), sid(), sid(), sid(), sid(), sid()

	var provDur time.Duration
	switch phase {
	case "ok":
		provDur = g.ms(40, 140)
	case "slow":
		provDur = g.ms(2100, 2900)
	default:
		provDur = 30*time.Second + g.ms(0, 12)
	}
	failed := phase == "timeout"
	dbDur := g.ms(3, 14)
	payDur := provDur + g.ms(4, 11)
	cliDur := payDur + g.ms(1, 4)
	coDur := dbDur + cliDur + g.ms(6, 22)
	webDur := coDur + g.ms(3, 9)

	webStart := start
	coStart := webStart.Add(g.ms(1, 3))
	dbStart := coStart.Add(g.ms(1, 2))
	cliStart := dbStart.Add(dbDur + g.ms(0, 2))
	payStart := cliStart.Add(g.ms(0, 1))
	provStart := payStart.Add(g.ms(1, 3))

	code := 200
	if failed {
		code = 502
	}
	payCode := 200
	if failed {
		payCode = 504
	}
	g.spans = append(g.spans,
		span{traceID: traceID, spanID: webID, name: "GET /checkout", kind: "SERVER", service: "web-frontend",
			route: "/checkout", httpStatus: code, start: webStart, dur: webDur, err: failed},
		span{traceID: traceID, spanID: coID, parentID: webID, name: "POST /api/checkout", kind: "SERVER",
			service: "checkout-service", route: "/api/checkout", httpStatus: code, start: coStart, dur: coDur, err: failed},
		span{traceID: traceID, spanID: dbID, parentID: coID, name: "SELECT orders", kind: "CLIENT",
			service: "checkout-service", start: dbStart, dur: dbDur,
			extra: map[string]any{"$db.system": "postgresql"}},
		span{traceID: traceID, spanID: cliID, parentID: coID, name: "POST /charge", kind: "CLIENT",
			service: "checkout-service", httpStatus: payCode, start: cliStart, dur: cliDur, err: failed},
		span{traceID: traceID, spanID: payID, parentID: cliID, name: "POST /charge", kind: "SERVER",
			service: "payments-api", route: "/charge", httpStatus: payCode, start: payStart, dur: payDur, err: failed},
		span{traceID: traceID, spanID: provID, parentID: payID, name: "provider.authorize", kind: "CLIENT",
			service: "payments-api", start: provStart, dur: provDur, err: failed,
			extra: map[string]any{"$peer.service": "card-provider"}},
	)

	coMsg, coStatus := "checkout completed", "info"
	payMsg, payStatus := "charge authorized", "info"
	webMsg, webStatus := "GET /checkout 200", "info"
	switch phase {
	case "slow":
		coMsg, coStatus = fmt.Sprintf("payments-api slow to respond (%.1fs)", cliDur.Seconds()), "warn"
		payMsg, payStatus = fmt.Sprintf("card provider slow (%.1fs)", provDur.Seconds()), "warn"
	case "timeout":
		coMsg, coStatus = "payment failed: payments-api timed out after 30s", "error"
		payMsg, payStatus = "card provider timeout after 30s", "error"
		webMsg, webStatus = "GET /checkout 502", "error"
	}
	g.events = append(g.events,
		event{at: coStart.Add(coDur), logID: logCheckoutProd, status: coStatus, kvs: map[string]any{
			"message": coMsg, "service": "checkout-service", "host": host, "status": code,
			"duration_ms": coDur.Milliseconds(), "path": "/api/checkout", "trace_id": traceID}},
		event{at: payStart.Add(payDur), logID: logPaymentsProd, status: payStatus, kvs: map[string]any{
			"message": payMsg, "service": "payments-api", "host": host, "status": payCode,
			"duration_ms": payDur.Milliseconds(), "path": "/charge", "trace_id": traceID}},
		event{at: webStart.Add(webDur), logID: logWebProd, status: webStatus, kvs: map[string]any{
			"message": webMsg, "service": "web-frontend", "host": host, "status": code,
			"duration_ms": webDur.Milliseconds(), "path": "/checkout", "trace_id": traceID}},
	)
}

// cart emits a cart view: web-frontend -> checkout-service, no payment.
func (g *gen) cart(start time.Time, traceID string) {
	host := g.host()
	webID, coID, dbID := g.hex(16), g.hex(16), g.hex(16)
	dbDur := g.ms(2, 9)
	coDur := dbDur + g.ms(8, 30)
	webDur := coDur + g.ms(3, 8)
	coStart := start.Add(g.ms(1, 3))
	g.spans = append(g.spans,
		span{traceID: traceID, spanID: webID, name: "GET /cart", kind: "SERVER", service: "web-frontend",
			route: "/cart", httpStatus: 200, start: start, dur: webDur},
		span{traceID: traceID, spanID: coID, parentID: webID, name: "GET /api/cart", kind: "SERVER",
			service: "checkout-service", route: "/api/cart", httpStatus: 200, start: coStart, dur: coDur},
		span{traceID: traceID, spanID: dbID, parentID: coID, name: "SELECT cart_items", kind: "CLIENT",
			service: "checkout-service", start: coStart.Add(g.ms(1, 2)), dur: dbDur,
			extra: map[string]any{"$db.system": "postgresql"}},
	)
	g.events = append(g.events,
		event{at: coStart.Add(coDur), logID: logCheckoutProd, status: "info", kvs: map[string]any{
			"message": "cart loaded", "service": "checkout-service", "host": host, "status": 200,
			"duration_ms": coDur.Milliseconds(), "path": "/api/cart", "trace_id": traceID}},
		event{at: start.Add(webDur), logID: logWebProd, status: "info", kvs: map[string]any{
			"message": "GET /cart 200", "service": "web-frontend", "host": host, "status": 200,
			"duration_ms": webDur.Milliseconds(), "path": "/cart", "trace_id": traceID}},
	)
}

func (g *gen) traceID(at time.Time) string {
	g.nTrace++
	// The docs' example trace: the first checkout attempt after 09:22:30,
	// in the middle of the timeout burst.
	if at.After(t0.Add(22*time.Minute+30*time.Second)) && !exampleUsed {
		exampleUsed = true
		return ExampleTraceID
	}
	return g.hex(32)
}

var exampleUsed bool

func (g *gen) run() {
	// Checkout attempts: every ~40s normally, every ~15s during the burst
	// (clients retry), with a little jitter.
	for at := t0.Add(7 * time.Second); at.Before(end.Add(-20 * time.Second)); {
		g.checkout(at, g.traceID(at))
		step := 38*time.Second + g.ms(0, 6000)
		if !at.Before(burstStart.Add(-15*time.Second)) && at.Before(burstEnd) {
			step = 13*time.Second + g.ms(0, 4000)
		}
		at = at.Add(step)
	}
	// Cart views, every ~75s, offset from checkouts.
	for at := t0.Add(31 * time.Second); at.Before(end.Add(-30 * time.Second)); at = at.Add(70*time.Second + g.ms(0, 10000)) {
		g.cart(at, g.hex(32))
	}
	// web-frontend page views without a backend call.
	pages := []string{"/", "/products/espresso-machine", "/products/grinder", "/search"}
	for at := t0.Add(19 * time.Second); at.Before(end.Add(-15 * time.Second)); at = at.Add(55*time.Second + g.ms(0, 9000)) {
		p := pages[g.rng.IntN(len(pages))]
		d := g.ms(4, 30)
		g.events = append(g.events, event{at: at.Add(d), logID: logWebProd, status: "info", kvs: map[string]any{
			"message": "GET " + p + " 200", "service": "web-frontend", "host": g.host(), "status": 200,
			"duration_ms": d.Milliseconds(), "path": p, "trace_id": g.hex(32)}})
	}
	// staging/checkout-service: a quiet trickle of test checkouts.
	for at := t0.Add(95 * time.Second); at.Before(end); at = at.Add(3*time.Minute + g.ms(0, 40000)) {
		d := g.ms(70, 260)
		g.events = append(g.events, event{at: at, logID: logCheckoutStaging, status: "info", kvs: map[string]any{
			"message": "checkout completed", "service": "checkout-service", "host": "web-1", "status": 200,
			"duration_ms": d.Milliseconds(), "path": "/api/checkout", "trace_id": g.hex(32)}})
	}
}

// eventDoc renders an event in the live /search "events" shape: @time,
// @status, @raw (the original JSON line), the parsed message_kvs, and the
// metadata block (logId, timestamp, sequence, origin).
func eventDoc(e event, seq int64) map[string]any {
	raw := map[string]any{"level": e.status}
	for k, v := range e.kvs {
		raw[k] = v
	}
	rawLine := mustCompactJSON(orderedRaw(raw))
	return map[string]any{
		"@time":       e.at.Format("2006-01-02 15:04:05.000 MST"),
		"@status":     e.status,
		"@raw":        rawLine,
		"message_kvs": e.kvs,
		"metadata": map[string]any{
			"logId":     e.logID,
			"timestamp": e.at.UnixMilli(),
			"sequence":  seq,
			"origin":    e.kvs["host"],
		},
	}
}

// orderedRaw keeps @raw readable: a fixed key order instead of Go's
// sorted map order.
func orderedRaw(m map[string]any) []kv {
	order := []string{"level", "service", "host", "message", "path", "status", "duration_ms", "trace_id"}
	out := make([]kv, 0, len(m))
	for _, k := range order {
		if v, ok := m[k]; ok {
			out = append(out, kv{k, v})
		}
	}
	return out
}

type kv struct {
	k string
	v any
}

func mustCompactJSON(kvs []kv) string {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range kvs {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(p.k)
		vb, _ := json.Marshal(p.v)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.String()
}

func spanDoc(s span) map[string]any {
	status := "STATUS_CODE_UNSET"
	if s.err {
		status = "STATUS_CODE_ERROR"
	}
	end := s.start.Add(s.dur)
	d := map[string]any{
		"@time":                      s.start.Format("2006-01-02 15:04:05.000 MST"),
		"$span.trace_id":             s.traceID,
		"$span.span_id":              s.spanID,
		"$span.name":                 s.name,
		"$span.kind":                 "SPAN_KIND_" + s.kind,
		"$span.duration_nano":        s.dur.Nanoseconds(),
		"$span.start_time_unix_nano": s.start.UnixNano(),
		"$span.end_time_unix_nano":   end.UnixNano(),
		"$span.status_code":          status,
		"$service.name":              s.service,
		"$service.namespace":         "shop",
	}
	if s.parentID != "" {
		d["$span.parent_span_id"] = s.parentID
	}
	if s.route != "" {
		d["$http.route"] = s.route
	}
	if s.httpStatus != 0 {
		d["$http.response.status_code"] = s.httpStatus
	}
	for k, v := range s.extra {
		d[k] = v
	}
	return d
}

func writeJSONL(path string, docs []map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	var b bytes.Buffer
	for _, d := range docs {
		line, err := json.Marshal(d)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, b.Bytes(), 0o600)
}

func main() {
	out := flag.String("out", "docs/testdata/mock", "fixture directory to write events/ and traces/ into")
	flag.Parse()

	g := &gen{rng: rand.New(rand.NewPCG(20260719, 930))} // #nosec G404 -- deterministic fixtures, not crypto
	g.run()
	if !exampleUsed {
		fmt.Fprintln(os.Stderr, "genfixtures: example trace was never assigned")
		os.Exit(1)
	}

	files := map[string]string{
		logCheckoutProd:    "events/prod/checkout-service.jsonl",
		logPaymentsProd:    "events/prod/payments-api.jsonl",
		logWebProd:         "events/prod/web-frontend.jsonl",
		logCheckoutStaging: "events/staging/checkout-service.jsonl",
	}
	seqBase := map[string]int64{
		logCheckoutProd: 48211000, logPaymentsProd: 31877000, logWebProd: 77120000, logCheckoutStaging: 1204000,
	}
	sort.SliceStable(g.events, func(i, j int) bool { return g.events[i].at.Before(g.events[j].at) })
	byLog := map[string][]map[string]any{}
	for _, e := range g.events {
		if e.at.Before(t0) || !e.at.Before(end) {
			fmt.Fprintf(os.Stderr, "genfixtures: event outside the frozen window: %s\n", e.at)
			os.Exit(1)
		}
		seqBase[e.logID] += 3
		byLog[e.logID] = append(byLog[e.logID], eventDoc(e, seqBase[e.logID]))
	}
	for logID, rel := range files {
		if err := writeJSONL(filepath.Join(*out, rel), byLog[logID]); err != nil {
			fmt.Fprintln(os.Stderr, "genfixtures:", err)
			os.Exit(1)
		}
	}

	sort.SliceStable(g.spans, func(i, j int) bool { return g.spans[i].start.Before(g.spans[j].start) })
	spanDocs := make([]map[string]any, 0, len(g.spans))
	for _, s := range g.spans {
		spanDocs = append(spanDocs, spanDoc(s))
	}
	if err := writeJSONL(filepath.Join(*out, "traces/spans.jsonl"), spanDocs); err != nil {
		fmt.Fprintln(os.Stderr, "genfixtures:", err)
		os.Exit(1)
	}
	fmt.Printf("genfixtures: %d events, %d spans, %d traces\n", len(g.events), len(g.spans), g.nTrace)
}
