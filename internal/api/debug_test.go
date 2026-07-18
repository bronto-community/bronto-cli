package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDebugTransportTracesAndPreservesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var trace strings.Builder
	client := &http.Client{Transport: &Transport{
		APIKey:    "sekrit",
		UserAgent: "test",
		Base:      &DebugTransport{Base: http.DefaultTransport, W: &trace},
	}}
	resp, err := client.Get(srv.URL + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if string(body) != `{"ok":true}` {
		t.Fatalf("body consumed by debug peek: %q", body)
	}
	out := trace.String()
	if !strings.Contains(out, "GET "+srv.URL+"/logs") || !strings.Contains(out, "< 200") || !strings.Contains(out, `{"ok":true}`) {
		t.Fatalf("trace missing expected lines: %s", out)
	}
	if strings.Contains(out, "sekrit") {
		t.Fatalf("API key leaked into the debug trace: %s", out)
	}
}

func TestDebugTransportBodyTruncation(t *testing.T) {
	big := strings.Repeat("x", debugBodyCap+100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()

	var trace strings.Builder
	client := &http.Client{Transport: &DebugTransport{Base: http.DefaultTransport, W: &trace}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if len(body) != len(big) {
		t.Fatalf("full body must reach the caller: got %d want %d", len(body), len(big))
	}
	if !strings.Contains(trace.String(), "…(truncated)") {
		t.Fatal("oversized body must be truncated in the trace")
	}
}
