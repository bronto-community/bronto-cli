package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransportSetsHeaders(t *testing.T) {
	var gotKey, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-BRONTO-API-KEY")
		gotUA = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	c := NewHTTPClient("test-key", "1.2.3")
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotKey != "test-key" {
		t.Fatalf("api key header = %q", gotKey)
	}
	if gotUA != "bronto-cli/1.2.3" {
		t.Fatalf("user agent = %q", gotUA)
	}
}

func TestTransportRetriesOn429ThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := &Transport{APIKey: "k", UserAgent: "t", MaxRetries: 2, Sleep: func(time.Duration) {}}
	c := &http.Client{Transport: tr}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestTransportDoesNotRetryPOST(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tr := &Transport{APIKey: "k", UserAgent: "t", MaxRetries: 2, Sleep: func(time.Duration) {}}
	c := &http.Client{Transport: tr}
	resp, err := c.Post(srv.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if calls.Load() != 1 {
		t.Fatalf("POST retried: calls = %d, want 1", calls.Load())
	}
}

func TestTransportRetryExhaustionReturnsFinalResponse(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tr := &Transport{APIKey: "k", UserAgent: "t", MaxRetries: 2, Sleep: func(time.Duration) {}}
	c := &http.Client{Transport: tr}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3 (1 + 2 retries)", calls.Load())
	}
}

func TestTransportCanceledContextAbortsRetryWaitPromptly(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	tr := &Transport{APIKey: "k", UserAgent: "t", MaxRetries: 2}
	c := &http.Client{Transport: tr}

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	time.AfterFunc(20*time.Millisecond, cancel)

	start := time.Now()
	_, err = c.Do(req)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("want context error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("RoundTrip took %v, want it to return promptly after context cancellation", elapsed)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (no retry attempt should complete after cancellation)", calls.Load())
	}
}

// erroringRoundTripper always fails at the transport layer (no HTTP
// response at all), exercising RoundTrip's base.RoundTrip error passthrough.
type erroringRoundTripper struct{ err error }

func (e erroringRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, e.err }

func TestTransportBaseRoundTripErrorPropagates(t *testing.T) {
	wantErr := errors.New("connection reset")
	tr := &Transport{APIKey: "k", UserAgent: "t", Base: erroringRoundTripper{err: wantErr}}
	c := &http.Client{Transport: tr}
	_, err := c.Get("http://127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestTransportGetBodyErrorDuringRetryAbortsRoundTrip(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("getbody boom")
	req.GetBody = func() (io.ReadCloser, error) { return nil, wantErr }

	tr := &Transport{APIKey: "k", UserAgent: "t", MaxRetries: 2, Sleep: func(time.Duration) {}}
	_, err = tr.RoundTrip(req)
	if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("err = %v, want it to surface the GetBody error", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (aborted before a second attempt)", calls.Load())
	}
}

// TestTransportWaitsRealTimerWhenSleepNil pins the wait() path taken in
// production (Sleep unset): it must actually block on time.After rather
// than returning immediately, and still complete the retry once the timer
// fires (as opposed to the context.Done() early-exit path, covered by
// TestTransportCanceledContextAbortsRetryWaitPromptly).
func TestTransportWaitsRealTimerWhenSleepNil(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := &Transport{APIKey: "k", UserAgent: "t", MaxRetries: 2} // Sleep left nil
	c := &http.Client{Transport: tr}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestRetryDelayAboveCapIsClamped(t *testing.T) {
	r := &http.Response{Header: http.Header{}}
	r.Header.Set("Retry-After", "3600")
	if d := retryDelay(r, 0); d != 30*time.Second {
		t.Fatalf("retryDelay = %v, want clamped to 30s", d)
	}
}

func TestRetryDelayForms(t *testing.T) {
	mk := func(h string) *http.Response {
		r := &http.Response{Header: http.Header{}}
		if h != "" {
			r.Header.Set("Retry-After", h)
		}
		return r
	}
	if d := retryDelay(mk("2"), 0); d != 2*time.Second {
		t.Errorf("delta-seconds: got %v", d)
	}
	if d := retryDelay(mk("-1"), 0); d != 0 {
		t.Errorf("negative clamped: got %v", d)
	}
	if d := retryDelay(mk("Mon, 02 Jan 2006 15:04:05 GMT"), 0); d != 0 {
		t.Errorf("past HTTP-date clamped to 0: got %v", d)
	}
	future := time.Now().UTC().Add(10 * time.Second)
	if d := retryDelay(mk(future.Format(http.TimeFormat)), 0); d <= 0 || d > 10*time.Second {
		t.Errorf("future HTTP-date: got %v, want a positive delay close to 10s", d)
	}
	if d := retryDelay(mk("bogus"), 1); d != 1*time.Second {
		t.Errorf("fallback backoff attempt 1: got %v, want 1s", d)
	}
	if d := retryDelay(mk(""), 0); d != 500*time.Millisecond {
		t.Errorf("no header backoff: got %v, want 500ms", d)
	}
}

func TestErrorFromStatus(t *testing.T) {
	cases := []struct {
		status    int
		code      string
		exit      int
		retryable bool
	}{
		{401, "auth_invalid_key", 3, false},
		{403, "auth_insufficient_role", 3, false},
		{404, "resource_not_found", 4, false},
		{429, "rate_limited", 5, true},
		{502, "api_server_error", 1, true},
		{418, "api_error", 1, false},
	}
	for _, c := range cases {
		e := ErrorFromStatus(c.status, []byte(`{"message":"nope"}`))
		if e == nil {
			t.Fatalf("status %d: nil error", c.status)
		}
		if e.Code != c.code || e.ExitCode() != c.exit || e.Retryable != c.retryable {
			t.Errorf("status %d: got code=%s exit=%d retryable=%v", c.status, e.Code, e.ExitCode(), e.Retryable)
		}
	}
	if ErrorFromStatus(200, nil) != nil {
		t.Error("2xx must map to nil")
	}
}

func TestErrorFromStatusMessageExtraction(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"message field wins", `{"message":"bad filter"}`, "Bronto API returned 400: bad filter"},
		{"details field extracted", `{"code":400,"type":"Bad Request","correlation_id":"abc","details":"provided object should contain property queries"}`, "Bronto API returned 400: provided object should contain property queries"},
		{"other JSON shapes surface raw", `{"errors":[{"field":"window"}]}`, `Bronto API returned 400: {"errors":[{"field":"window"}]}`},
		{"plain text surfaces raw", "Bad Request", "Bronto API returned 400: Bad Request"},
		{"whitespace-only body omitted", "  \n", "Bronto API returned 400"},
		{"empty body omitted", "", "Bronto API returned 400"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := ErrorFromStatus(400, []byte(c.body))
			if e == nil {
				t.Fatal("nil error for 400")
			}
			if e.Message != c.want {
				t.Errorf("message = %q, want %q", e.Message, c.want)
			}
		})
	}

	t.Run("long body truncated", func(t *testing.T) {
		e := ErrorFromStatus(400, []byte(strings.Repeat("x", 500)))
		want := "Bronto API returned 400: " + strings.Repeat("x", 300) + "…"
		if e.Message != want {
			t.Errorf("truncated message = %q (len %d), want 300-char snippet + ellipsis", e.Message, len(e.Message))
		}
	})
}

// TestIdempotentHintRetriesPOST pins the search-retry contract: a POST
// carrying the hint retries on 429 (with the hint header stripped from
// the wire); an unmarked POST does not retry.
func TestIdempotentHintRetriesPOST(t *testing.T) {
	calls := 0
	sawHint := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get(IdempotentHint) != "" {
			sawHint = true
		}
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &Transport{APIKey: "k", UserAgent: "t", MaxRetries: 2, Sleep: func(time.Duration) {}}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"q":1}`))
	req.Header.Set(IdempotentHint, "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || calls != 2 {
		t.Fatalf("status=%d calls=%d, want retried success", resp.StatusCode, calls)
	}
	if sawHint {
		t.Fatal("internal hint header must not reach the wire")
	}

	// unmarked POST: no retry
	calls = 0
	req2, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if calls != 1 || resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("unmarked POST must not retry: calls=%d status=%d", calls, resp2.StatusCode)
	}
}
