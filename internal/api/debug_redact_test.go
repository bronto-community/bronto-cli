package api

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDebugURLRedactsSecretQueryValues(t *testing.T) {
	u, _ := url.Parse("https://user:pw@api.eu.bronto.io/x?token=T1&api_key=K2&limit=10&X-Amz-Signature=S3&where=a%3Db")
	got := debugURL(u)
	for _, leak := range []string{"T1", "K2", "S3", ":pw@"} {
		if strings.Contains(got, leak) {
			t.Fatalf("%q leaked in %s", leak, got)
		}
	}
	for _, keep := range []string{"limit=10", "where=a%3Db", "token=REDACTED", "user:xxxxx@"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("%q missing from %s", keep, got)
		}
	}
}

func TestDebugTransportDecodesGzipRequestBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	var zbuf bytes.Buffer
	zw := gzip.NewWriter(&zbuf)
	_, _ = zw.Write([]byte(`{"message":"hello"}` + "\n"))
	_ = zw.Close()

	var trace strings.Builder
	client := &http.Client{Transport: &DebugTransport{Base: http.DefaultTransport, W: &trace}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/?token=T1", bytes.NewReader(zbuf.Bytes()))
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	out := trace.String()
	if !strings.Contains(out, `{"message":"hello"}`) || strings.Contains(out, "T1") {
		t.Fatalf("trace = %s", out)
	}

	// A body that claims gzip but isn't prints a placeholder, not bytes.
	if got := string(readDebugBody(strings.NewReader("\x1f\x8bjunk"), "gzip")); got != "<gzip body, 6 bytes>" {
		t.Fatalf("placeholder = %q", got)
	}
}
