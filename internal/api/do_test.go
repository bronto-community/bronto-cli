package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func doGet(t *testing.T, h http.HandlerFunc, timeout time.Duration) (*Response, error) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewHTTPClient("k", "test")
	c.Timeout = timeout
	c.Transport.(*Transport).MaxRetries = 0
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/logs", nil)
	if err != nil {
		t.Fatal(err)
	}
	return Do(c, req)
}

func wantCode(t *testing.T, err error, code string, exit int) *clierr.Error {
	t.Helper()
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != code || clierr.ExitCode(err) != exit {
		t.Fatalf("want %s (exit %d), got %#v", code, exit, err)
	}
	return ce
}

func TestDoHangingServerIsTimeout(t *testing.T) {
	_, err := doGet(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}, 100*time.Millisecond)
	if ce := wantCode(t, err, "timeout", 5); !ce.Retryable {
		t.Fatal("timeout must be retryable")
	}
}

func TestDoBodyStallIsTimeout(t *testing.T) {
	// Headers arrive, then the body never finishes: Client.Timeout fires
	// while reading, which must classify like any other timeout.
	_, err := doGet(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("{"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, 200*time.Millisecond)
	ce := wantCode(t, err, "timeout", 5)
	if !strings.Contains(ce.Message, "reading response body") {
		t.Fatalf("message = %q", ce.Message)
	}
}

func TestDoConnectionClosedMidBodyIsNetworkError(t *testing.T) {
	_, err := doGet(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte(`{"partial":`))
		w.(http.Flusher).Flush()
		conn, _, herr := w.(http.Hijacker).Hijack()
		if herr == nil {
			_ = conn.Close()
		}
	}, 5*time.Second)
	ce := wantCode(t, err, "network_error", 1)
	if !ce.Retryable || !strings.Contains(ce.Message, "reading response body") {
		t.Fatalf("got %+v", ce)
	}
}

func TestDoStatusErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		exit   int
	}{
		{500, "api_server_error", 1},
		{429, "rate_limited", 5},
		{401, "auth_invalid_key", 3},
		{403, "auth_insufficient_role", 3},
	} {
		resp, err := doGet(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}, 5*time.Second)
		_ = wantCode(t, err, tc.code, tc.exit)
		if resp == nil || resp.StatusCode != tc.status {
			t.Fatalf("%d: response not returned alongside the error: %+v", tc.status, resp)
		}
	}
}

func TestDoUnreachableIsNetworkError(t *testing.T) {
	c := NewHTTPClient("k", "test")
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1/logs", nil)
	_, err := Do(c, req)
	_ = wantCode(t, err, "network_error", 1)
}

func TestDoCanceledContextPassesThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := NewHTTPClient("k", "test")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:1/logs", nil)
	_, err := Do(c, req)
	var ce *clierr.Error
	if errors.As(err, &ce) || !errors.Is(err, context.Canceled) {
		t.Fatalf("want the raw context error, got %#v", err)
	}
}

func TestRedirectRefusesSchemeDowngrade(t *testing.T) {
	mk := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	via := []*http.Request{mk("https://api.eu.bronto.io/logs")}
	if err := refuseCrossHostRedirect(mk("http://api.eu.bronto.io/logs"), via); err == nil ||
		!strings.Contains(err.Error(), "cleartext") {
		t.Fatalf("https->http redirect must be refused, got %v", err)
	}
	if err := refuseCrossHostRedirect(mk("https://api.eu.bronto.io/other"), via); err != nil {
		t.Fatalf("same-host https redirect refused: %v", err)
	}
	// A plain-http base (local/dev backend) may still redirect within http.
	httpVia := []*http.Request{mk("http://localhost:8080/logs")}
	if err := refuseCrossHostRedirect(mk("http://localhost:8080/other"), httpVia); err != nil {
		t.Fatalf("http->http same-host redirect refused: %v", err)
	}
}
