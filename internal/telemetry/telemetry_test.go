package telemetry

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func stubEnv(t *testing.T, env map[string]string) {
	t.Helper()
	orig := Getenv
	Getenv = func(k string) string { return env[k] }
	t.Cleanup(func() { Getenv = orig })
}

func TestSpanExportersSelection(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    int
		wantErr string
	}{
		{"nothing set is off", nil, 0, ""},
		{"endpoint enables otlp", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4318"}, 1, ""},
		{"traces endpoint enables otlp", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://127.0.0.1:4318/v1/traces"}, 1, ""},
		{"sdk disabled wins", map[string]string{"OTEL_SDK_DISABLED": "TRUE", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://x"}, 0, ""},
		{"none wins", map[string]string{"OTEL_TRACES_EXPORTER": "console,none"}, 0, ""},
		{"console", map[string]string{"OTEL_TRACES_EXPORTER": "console"}, 1, ""},
		{"grpc", map[string]string{"OTEL_TRACES_EXPORTER": "otlp", "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"}, 1, ""},
		{"list", map[string]string{"OTEL_TRACES_EXPORTER": "otlp, console"}, 2, ""},
		{"unknown exporter", map[string]string{"OTEL_TRACES_EXPORTER": "zipkin"}, 0, "unsupported OTEL_TRACES_EXPORTER"},
		{"unknown protocol", map[string]string{"OTEL_TRACES_EXPORTER": "otlp", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "http/json"}, 0, "unsupported OTLP protocol"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubEnv(t, tc.env)
			exps, err := spanExporters(context.Background())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(exps) != tc.want {
				t.Fatalf("got %d exporters, want %d", len(exps), tc.want)
			}
		})
	}
}

func TestContextFromEnvAndEnvironRoundTrip(t *testing.T) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	stubEnv(t, map[string]string{"TRACEPARENT": tp, "TRACESTATE": "vendor=x", "BAGGAGE": "team=obs"})

	ctx := ContextFromEnv(context.Background())
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsRemote() || sc.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("span context from env = %+v", sc)
	}

	env := Environ(ctx, []string{"PATH=/bin", "TRACEPARENT=stale", "TRACESTATE=stale"})
	for _, want := range []string{"PATH=/bin", "TRACEPARENT=" + tp, "TRACESTATE=vendor=x", "BAGGAGE=team=obs"} {
		if !slices.Contains(env, want) {
			t.Errorf("env %v missing %q", env, want)
		}
	}
	if slices.Contains(env, "TRACEPARENT=stale") || slices.Contains(env, "TRACESTATE=stale") {
		t.Errorf("stale carrier values not replaced: %v", env)
	}
}

func TestEnvironWithoutSpanIsUnchanged(t *testing.T) {
	base := []string{"PATH=/bin", "TRACEPARENT=inherited"}
	if got := Environ(context.Background(), base); !slices.Equal(got, base) {
		t.Fatalf("Environ = %v, want %v", got, base)
	}
}

func TestHandleErrorSilentByDefault(t *testing.T) {
	var buf bytes.Buffer
	orig := Stderr
	Stderr = &buf
	t.Cleanup(func() { Stderr = orig })

	stubEnv(t, nil)
	handleError(context.DeadlineExceeded)
	if buf.Len() != 0 {
		t.Fatalf("default handler wrote %q", buf.String())
	}
	stubEnv(t, map[string]string{"OTEL_LOG_LEVEL": "debug"})
	handleError(context.DeadlineExceeded)
	if !strings.Contains(buf.String(), "bronto: telemetry: context deadline exceeded") {
		t.Fatalf("debug handler wrote %q", buf.String())
	}
}

func TestCaptureLevel(t *testing.T) {
	var reported []error
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { reported = append(reported, err) }))
	t.Cleanup(func() { otel.SetErrorHandler(otel.ErrorHandlerFunc(handleError)) })

	cases := map[string]Capture{"": CaptureDefault, "default": CaptureDefault, "ALL": CaptureAll, "none": CaptureNone}
	for v, want := range cases {
		stubEnv(t, map[string]string{CaptureEnv: v})
		if got := CaptureLevel(); got != want {
			t.Errorf("CaptureLevel() with %q = %v, want %v", v, got, want)
		}
	}
	if len(reported) != 0 {
		t.Errorf("valid values reported errors: %v", reported)
	}
	// A typo records less, not more, and says so.
	stubEnv(t, map[string]string{CaptureEnv: "al"})
	if got := CaptureLevel(); got != CaptureNone {
		t.Errorf("CaptureLevel() with a typo = %v, want CaptureNone", got)
	}
	if len(reported) != 1 || !strings.Contains(reported[0].Error(), CaptureEnv) {
		t.Errorf("typo reported %v", reported)
	}
}
