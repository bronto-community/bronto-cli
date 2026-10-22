package main

import (
	"strconv"
	"strings"
	"testing"
	"time"

	mrand "math/rand/v2"
)

// TestTracesNestAndRoutesArePaths checks every generated trace: each
// child span lies inside its parent, http.route carries the path without
// the method, and payments-gateway serves more than one operation.
func TestTracesNestAndRoutesArePaths(t *testing.T) {
	g := &generator{errRate: 0.5, rng: mrand.New(mrand.NewPCG(1, 2))}
	var b batchBuilder
	at := time.Unix(1_800_000_000, 0)
	for range 2000 {
		g.trace(&b, at)
	}

	type span struct{ start, end int64 }
	spans := map[string]span{}
	parents := map[string]string{}
	ops := map[string]map[string]bool{}
	for svc, list := range b.spans {
		for _, raw := range list {
			s := raw.(map[string]any)
			id := s["spanId"].(string)
			start, _ := strconv.ParseInt(s["startTimeUnixNano"].(string), 10, 64)
			end, _ := strconv.ParseInt(s["endTimeUnixNano"].(string), 10, 64)
			spans[id] = span{start, end}
			if p, ok := s["parentSpanId"].(string); ok {
				parents[id] = p
			}
			if s["kind"] == kindServer {
				if ops[svc] == nil {
					ops[svc] = map[string]bool{}
				}
				ops[svc][s["name"].(string)] = true
			}
			for _, a := range s["attributes"].([]any) {
				kv := a.(map[string]any)
				if kv["key"] == "http.route" {
					route := kv["value"].(map[string]any)["stringValue"].(string)
					if !strings.HasPrefix(route, "/") {
						t.Fatalf("%s span %q: http.route %q isn't a path", svc, s["name"], route)
					}
				}
			}
		}
	}
	for id, p := range parents {
		c, par := spans[id], spans[p]
		if c.start < par.start || c.end > par.end {
			t.Fatalf("span %s [%d, %d] isn't inside its parent %s [%d, %d]", id, c.start, c.end, p, par.start, par.end)
		}
	}
	if len(ops["payments-gateway"]) < 3 {
		t.Fatalf("payments-gateway operations = %v, want at least 3", ops["payments-gateway"])
	}
}
