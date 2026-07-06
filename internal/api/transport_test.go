package api

import (
	"net/http"
	"net/http/httptest"
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
	resp.Body.Close()
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
	resp.Body.Close()
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
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Fatalf("POST retried: calls = %d, want 1", calls.Load())
	}
}

func TestErrorFromStatus(t *testing.T) {
	cases := []struct {
		status   int
		code     string
		exit     int
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
