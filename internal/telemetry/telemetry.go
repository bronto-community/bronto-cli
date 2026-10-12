// Package telemetry wires opt-in OpenTelemetry tracing for the CLI itself.
//
// Everything is configured through the standard OTel environment variables;
// there are no bronto-specific knobs. Tracing stays off (a no-op tracer
// provider, no exporter, no goroutines) unless an OTLP endpoint or an
// explicit OTEL_TRACES_EXPORTER is set, and OTEL_SDK_DISABLED=true always
// wins.
//
// Trace context is continued from the TRACEPARENT / TRACESTATE / BAGGAGE
// environment variables (the OTel env-var carrier convention), whether or
// not export is enabled: an un-exported run still forwards the caller's
// context to the Bronto API and to exec plugins.
package telemetry

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// ServiceName is the default service.name; OTEL_SERVICE_NAME overrides it.
const ServiceName = "bronto-cli"

const tracerName = "github.com/bronto-community/bronto-cli"

// envCarrierKeys maps the W3C propagation fields to their environment
// variable names (the OTel env-var carrier spec: uppercased field names).
var envCarrierKeys = map[string]string{
	"traceparent": "TRACEPARENT",
	"tracestate":  "TRACESTATE",
	"baggage":     "BAGGAGE",
}

// Getenv is the environment lookup used for bronto's own decisions.
// Package-level so tests can stub it. The OTLP exporters read their own
// OTEL_EXPORTER_OTLP_* settings directly from the process environment.
var Getenv = os.Getenv

// Stderr receives the console exporter's output and, when OTEL_LOG_LEVEL
// asks for it, SDK errors. stdout is never used: it carries the command's
// own (often machine-read) output.
var Stderr io.Writer = os.Stderr

// Setup installs the W3C propagator and, when the environment enables
// export, an SDK tracer provider. It returns a shutdown func that flushes
// pending spans; callers bound it with a context deadline. Setup never
// fails the command: a misconfiguration is reported through the OTel error
// handler (silent unless OTEL_LOG_LEVEL is set) and tracing stays off.
func Setup(ctx context.Context, serviceVersion string) func(context.Context) error {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(handleError))

	noop := func(context.Context) error { return nil }
	exporters, err := spanExporters(ctx)
	if err != nil {
		otel.Handle(err)
		return noop
	}
	if len(exporters) == 0 {
		return noop
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(ServiceName),
			semconv.ServiceVersion(serviceVersion),
		),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(), // OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES
	)
	if err != nil {
		// A partial resource is still usable (e.g. one malformed
		// OTEL_RESOURCE_ATTRIBUTES entry); report and carry on.
		otel.Handle(err)
	}

	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	for _, exp := range exporters {
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(opts...) // sampler: OTEL_TRACES_SAMPLER, default parentbased_always_on
	otel.SetTracerProvider(tp)
	return tp.Shutdown
}

// spanExporters builds the exporters named by OTEL_TRACES_EXPORTER. When
// that is unset, OTLP is used only if an OTLP endpoint is configured, so a
// plain run never tries to reach localhost:4318.
func spanExporters(ctx context.Context) ([]sdktrace.SpanExporter, error) {
	if strings.EqualFold(strings.TrimSpace(Getenv("OTEL_SDK_DISABLED")), "true") {
		return nil, nil
	}
	names := strings.TrimSpace(Getenv("OTEL_TRACES_EXPORTER"))
	if names == "" {
		if Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
			return nil, nil
		}
		names = "otlp"
	}
	var exps []sdktrace.SpanExporter
	for _, name := range strings.Split(names, ",") {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "none":
			return nil, nil // spec: "none" disables export regardless of other entries
		case "otlp":
			exp, err := otlpExporter(ctx)
			if err != nil {
				return nil, err
			}
			exps = append(exps, exp)
		case "console":
			exp, err := stdouttrace.New(stdouttrace.WithWriter(Stderr), stdouttrace.WithPrettyPrint())
			if err != nil {
				return nil, err
			}
			exps = append(exps, exp)
		case "":
		default:
			return nil, fmt.Errorf("telemetry: unsupported OTEL_TRACES_EXPORTER %q (supported: otlp, console, none)", name)
		}
	}
	return exps, nil
}

func otlpExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	proto := Getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL")
	if proto == "" {
		proto = Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	}
	switch strings.TrimSpace(proto) {
	case "", "http/protobuf":
		return otlptracehttp.New(ctx)
	case "grpc":
		return otlptracegrpc.New(ctx)
	default:
		return nil, fmt.Errorf("telemetry: unsupported OTLP protocol %q (supported: http/protobuf, grpc)", proto)
	}
}

// handleError surfaces SDK errors (failed exports, bad config) on stderr
// only when OTEL_LOG_LEVEL is set. By default they are dropped: an
// unreachable collector must not add noise to a CLI whose stderr is often
// parsed as JSON.
func handleError(err error) {
	lvl := strings.ToLower(strings.TrimSpace(Getenv("OTEL_LOG_LEVEL")))
	if lvl == "" || lvl == "none" {
		return
	}
	_, _ = fmt.Fprintf(Stderr, "bronto: telemetry: %v\n", err)
}

// Tracer returns the CLI's tracer from the global provider.
func Tracer() trace.Tracer { return otel.Tracer(tracerName) }

// ContextFromEnv returns ctx carrying the trace context found in the
// TRACEPARENT / TRACESTATE / BAGGAGE environment variables, if any.
func ContextFromEnv(ctx context.Context) context.Context {
	carrier := propagation.MapCarrier{}
	for field, env := range envCarrierKeys {
		if v := Getenv(env); v != "" {
			carrier[field] = v
		}
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

// Environ returns base (an os.Environ-style list) with TRACEPARENT,
// TRACESTATE and BAGGAGE set from ctx, so a child process joins the
// current trace. Inherited values for those keys are replaced. If ctx
// carries no valid span context, base is returned unchanged.
func Environ(ctx context.Context, base []string) []string {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return base
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	out := make([]string, 0, len(base)+len(envCarrierKeys))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if isCarrierEnv(k) {
			continue
		}
		out = append(out, kv)
	}
	for field, env := range envCarrierKeys {
		if v := carrier.Get(field); v != "" {
			out = append(out, env+"="+v)
		}
	}
	return out
}

func isCarrierEnv(k string) bool {
	for _, env := range envCarrierKeys {
		if k == env {
			return true
		}
	}
	return false
}

// EndCommand records the command's outcome on its root span and ends it.
// errType is the typed error code (e.g. "auth_invalid_key"), empty on
// success.
func EndCommand(span trace.Span, exitCode int, errType string) {
	span.SetAttributes(semconv.ProcessExitCode(exitCode))
	if exitCode != 0 {
		if errType == "" {
			errType = "exit_" + fmt.Sprint(exitCode)
		}
		span.SetAttributes(semconv.ErrorTypeKey.String(errType))
		span.SetStatus(codes.Error, errType)
	}
	span.End()
}
