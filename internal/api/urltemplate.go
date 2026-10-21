package api

//go:generate go run ../tools/urltemplates -spec ../../api/openapi.yaml -out urltemplates_gen.go

import (
	"net/http"
	"net/url"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// undocumentedURLTemplates are paths the CLI calls that api/openapi.yaml
// doesn't describe (see specLiveButUndocumented in internal/cli). Keep in
// step with that set; TestEveryInventoriedEndpointHasURLTemplate fails if
// a path the CLI calls has no template.
var undocumentedURLTemplates = []string{
	// Template catalogs, verified live 2026-10-21. The literal "templates"
	// outranks the spec's /dashboards/{dashboardId} and /widgets/{id}.
	"/dashboards/templates",
	"/dashboards/templates/{templateId}",
	"/dashboards/templates/{templateId}/instance",
	"/dashboards/templates/{templateId}/preview",
	"/logs/{logId}", // the spec documents only the nested /logs/{logId}/... paths
	// The literal "definitions" outranks the spec's /metrics/{metric_id}.
	"/metrics/definitions",
	"/metrics/definitions/{definitionId}",
	"/organizations",
	"/parsers",
	"/parsers/{parserId}",
	"/saved-searches",
	"/saved-searches/{savedSearchId}",
	"/usage",
	"/widgets/templates",
	"/widgets/templates/{templateId}",
}

// urlTemplates holds every known template, split into path segments and
// grouped by segment count.
var urlTemplates = func() map[int][][]string {
	m := map[int][][]string{}
	for _, list := range [][]string{specURLTemplates, undocumentedURLTemplates} {
		for _, t := range list {
			segs := splitPath(t)
			m[len(segs)] = append(m[len(segs)], segs)
		}
	}
	return m
}()

// URLTemplate returns the API route template matching path, such as
// "/monitors/{monitorId}" for "/monitors/8f3a…". A literal segment beats
// a parameter ("/users/all" wins over "/users/{userId}"). ok is false when
// no template matches, e.g. an arbitrary path passed to `bronto api` or a
// base_url with a path prefix.
func URLTemplate(path string) (tmpl string, ok bool) {
	segs := splitPath(path)
	best, bestLiterals := -1, -1
	candidates := urlTemplates[len(segs)]
	for i, t := range candidates {
		literals := 0
		match := true
		for j, s := range t {
			if isParam(s) {
				if segs[j] == "" {
					match = false
					break
				}
				continue
			}
			if s != segs[j] {
				match = false
				break
			}
			literals++
		}
		if match && literals > bestLiterals {
			best, bestLiterals = i, literals
		}
	}
	if best < 0 {
		return "", false
	}
	return "/" + strings.Join(candidates[best], "/"), true
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func isParam(seg string) bool { return strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") }

// knownMethods are the methods the HTTP semantic conventions name spans
// after; anything else is named "HTTP".
var knownMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodDelete: true, http.MethodConnect: true, http.MethodOptions: true,
	http.MethodTrace: true, http.MethodPatch: true, "QUERY": true,
}

// SpanName names an HTTP client span per the OpenTelemetry HTTP semantic
// conventions: "{method} {url.template}" when the path matches a known
// route, otherwise just "{method}". The raw path is never used, which
// keeps span names low-cardinality.
func SpanName(r *http.Request) string {
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	if !knownMethods[method] {
		method = "HTTP"
	}
	if tmpl, ok := URLTemplate(r.URL.Path); ok {
		return method + " " + tmpl
	}
	return method
}

// newTracingTransport wraps base in otelhttp with SpanName as the span
// namer, records url.template on each span, and redacts url.full.
func newTracingTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(urlTemplateTransport{base},
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return SpanName(r) }))
}

// urlTemplateTransport runs inside otelhttp, so the request context
// carries the client span; it adds the url.template attribute to it and
// overwrites otelhttp's url.full with RedactURL's form.
type urlTemplateTransport struct{ base http.RoundTripper }

func (t urlTemplateTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(semconv.URLFull(RedactURL(r.URL)))
	if tmpl, ok := URLTemplate(r.URL.Path); ok {
		span.SetAttributes(semconv.URLTemplate(tmpl))
	}
	return t.base.RoundTrip(r)
}

// RedactURL renders u for the url.full span attribute without userinfo,
// fragment, or query values: each value becomes "REDACTED" and the keys
// are kept. otelhttp strips only userinfo, but query values can carry
// user input (`bronto api -X GET -f where=...`). The semantic conventions
// ask for sensitive query values to be redacted; all of them are here,
// since the CLI can't tell which ones are sensitive.
func RedactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.User = nil
	c.Fragment, c.RawFragment = "", ""
	if c.RawQuery != "" {
		var keys []string
		seen := map[string]bool{}
		for _, kv := range strings.Split(c.RawQuery, "&") {
			k, _, _ := strings.Cut(kv, "=")
			if k, err := url.QueryUnescape(k); err == nil && k != "" && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		q := url.Values{}
		for _, k := range keys {
			q.Set(k, "REDACTED")
		}
		c.RawQuery = q.Encode()
	}
	return c.String()
}
