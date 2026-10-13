package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestTelemetry_OTLPExportContinuesTraceparent live-verifies the CLI's own
// tracing end to end: a command run with TRACEPARENT set and Bronto's OTLP
// ingestion endpoint as the exporter lands in the account's .traces logset
// as a child of that TRACEPARENT, with its API call nested under it. The
// export needs an ingestion key; reading the trace back needs the
// management key.
func TestTelemetry_OTLPExportContinuesTraceparent(t *testing.T) {
	key := skipIfNoCreds(t)
	ingestKey := os.Getenv("BRONTO_IT_INGEST_KEY")
	if ingestKey == "" {
		t.Skip("BRONTO_IT_INGEST_KEY not set; skipping OTLP export test")
	}
	traceID, parentID := randHex(16), randHex(8)

	r := NewRunner(t, key)
	r.ExtraEnv = []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT=https://ingestion." + r.Region + ".bronto.io",
		"OTEL_EXPORTER_OTLP_HEADERS=x-bronto-api-key=" + ingestKey,
		"OTEL_SERVICE_NAME=bronto-cli-it-" + runID(),
		"OTEL_LOG_LEVEL=debug", // surface export failures on stderr
		"TRACEPARENT=00-" + traceID + "-" + parentID + "-01",
	}
	res := mustExitZero(t, r, "datasets", "list", "-o", "json")
	if strings.Contains(res.Stderr, "telemetry:") {
		t.Fatalf("span export failed:\n%s", res.Stderr)
	}

	reader := NewRunner(t, key)
	var spans []map[string]any
	PollUntil(t, 3*time.Minute, 5*time.Second, func() (bool, error) {
		res, err := reader.Run(t.Context(), "", "traces", "show", traceID, "--since", "1h", "-o", "json")
		if err != nil {
			return false, err
		}
		// Not ingested yet: an empty trace, or (on an account with no
		// .traces data at all yet) a 404 for the logset.
		if res.ExitCode != 0 {
			return false, fmt.Errorf("traces show exited %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		spans = nil
		if err := json.Unmarshal([]byte(res.Stdout), &spans); err != nil {
			return false, fmt.Errorf("traces show -o json: %w", err)
		}
		return len(spans) >= 2, nil
	})

	var root map[string]any
	for _, s := range spans {
		if s["operation"] == "bronto datasets list" {
			root = s
		}
	}
	if root == nil {
		t.Fatalf("no root span named %q in trace %s: %v", "bronto datasets list", traceID, spans)
	}
	if root["parent_span_id"] != parentID {
		t.Errorf("root span parent = %v, want TRACEPARENT's span %s", root["parent_span_id"], parentID)
	}
	var sawChild bool
	for _, s := range spans {
		// Named "{method} {url.template}", e.g. "GET /logs"; never a bare method
		// for a path the CLI calls.
		if s["parent_span_id"] == root["span_id"] && strings.HasPrefix(fmt.Sprint(s["operation"]), "GET /") {
			sawChild = true
		}
	}
	if !sawChild {
		t.Errorf("no templated HTTP client span (\"GET /...\") under the root span: %v", spans)
	}
}
