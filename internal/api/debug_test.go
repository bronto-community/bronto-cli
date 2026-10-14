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

func TestRedactBody(t *testing.T) {
	cases := []struct {
		name, in string
		keyAPI   bool
		want     string
	}{
		{"api key", `[{"name":"ci","api_key":"bronto_abc123","id":"1"}]`, false,
			`[{"name":"ci","api_key":"REDACTED","id":"1"}]`},
		{"prefixed and spaced", `{"client_secret" : "s3cr3t", "ask_api_key":"x"}`, false,
			`{"client_secret" : "REDACTED", "ask_api_key":"REDACTED"}`},
		{"tokens and passwords", `{"access_token":"a","refresh_token":"b","password":"c","token":"d"}`, false,
			`{"access_token":"REDACTED","refresh_token":"REDACTED","password":"REDACTED","token":"REDACTED"}`},
		{"escaped quote in value", `{"api_key":"ab\"cd","n":1}`, false, `{"api_key":"REDACTED","n":1}`},
		{"value cut off by the body cap", `{"name":"ci","api_key":"bronto_abc`, false, `{"name":"ci","api_key":"REDACTED"`},
		{"key on /api-keys", `{"key":"bronto_abc123"}`, true, `{"key":"REDACTED"}`},
		{"key elsewhere is a field name", `[{"key":"status","count":3}]`, false, `[{"key":"status","count":3}]`},
		{"lookalike names stay", `{"token_count":"5","keys":"a","secretary":"b"}`, false, `{"token_count":"5","keys":"a","secretary":"b"}`},
		{"non-string values stay", `{"api_key":null}`, false, `{"api_key":null}`},
	}
	for _, c := range cases {
		if got := redactBody(c.in, c.keyAPI); got != c.want {
			t.Errorf("%s: redactBody(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestDebugTransportRedactsKeyMaterial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"ci","api_key":"bronto_live_key_1","key":"bronto_live_key_2"}]`))
	}))
	defer srv.Close()

	var trace strings.Builder
	client := &http.Client{Transport: &DebugTransport{Base: http.DefaultTransport, W: &trace}}
	resp, err := client.Get(srv.URL + "/api-keys")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if !strings.Contains(string(body), "bronto_live_key_1") {
		t.Fatalf("redaction must only affect the trace, not the body the CLI reads: %q", body)
	}
	if out := trace.String(); strings.Contains(out, "bronto_live_key") || !strings.Contains(out, `"api_key":"REDACTED"`) {
		t.Fatalf("key material in the debug trace: %s", out)
	}
}
