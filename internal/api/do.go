package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// Response is a completed request whose body has been read in full.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// Fetch executes req with client and reads the whole response body.
// Transport failures and body-read failures (a connection dropped
// mid-body, Client.Timeout firing while reading) are classified with
// ClassifyTransportError; the status code is NOT inspected. Use Do unless
// the caller maps some status itself first (ingestion's 413).
func Fetch(client *http.Client, req *http.Request) (*Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, ClassifyTransportError(req.Context(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, ClassifyTransportError(req.Context(), &bodyReadError{err: err})
	}
	return &Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

// Do is Fetch plus ErrorFromStatus: every way a request can fail comes
// back as the same typed error, whichever command issued it. Every
// management-API and ingestion request goes through this, so a timeout is
// exit 5 in `bronto api` exactly as in `bronto datasets list`. On a
// non-2xx status the response is returned alongside the error.
func Do(client *http.Client, req *http.Request) (*Response, error) {
	resp, err := Fetch(client, req)
	if err != nil {
		return nil, err
	}
	if apiErr := ErrorFromStatus(resp.StatusCode, resp.Body); apiErr != nil {
		return resp, apiErr
	}
	return resp, nil
}

// bodyReadError marks a failure while reading the response body, so the
// message says where it happened. Unwrap keeps net.Error detection (and so
// the timeout classification) working.
type bodyReadError struct{ err error }

func (e *bodyReadError) Error() string { return "reading response body: " + e.err.Error() }
func (e *bodyReadError) Unwrap() error { return e.err }

// ClassifyTransportError maps a failed round trip to the CLI's typed
// errors: context cancellation passes through unwrapped (callers detect
// ctx state), timeouts become a retryable "timeout" with the
// BRONTO_TIMEOUT hint, everything else a retryable network_error. Fetch
// and Do apply it; streaming callers that can't use them (export
// downloads) call it directly.
func ClassifyTransportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return err
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return clierr.New("timeout", "request timed out: "+err.Error()).WithRetryable().
			WithHint("Increase the timeout via BRONTO_TIMEOUT or 'bronto config set timeout <seconds>'.")
	}
	return clierr.New("network_error", err.Error()).WithRetryable().
		WithHint("Check your network and the API base URL / region.")
}
