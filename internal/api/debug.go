package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// debugBodyCap bounds how much of a request/response body the debug trace
// prints — enough to see the shape, not enough to flood a terminal.
const debugBodyCap = 2048

// DebugTransport wraps a RoundTripper and traces every request/response to
// W (typically stderr): method, URL, status, duration, and truncated
// bodies. Headers are never printed, so the API key cannot leak into the
// trace, and secret-bearing body fields are redacted (redactBody): API
// responses carry key material, e.g. GET /api-keys returns every key.
// Install as Transport.Base (INSIDE the retry loop) so each retry attempt
// traces individually.
type DebugTransport struct {
	Base http.RoundTripper
	W    io.Writer

	mu sync.Mutex // serialize traces from concurrent requests (tail polls)
}

func (d *DebugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil && req.GetBody != nil {
		if rc, err := req.GetBody(); err == nil {
			reqBody, _ = io.ReadAll(io.LimitReader(rc, debugBodyCap+1))
			_ = rc.Close()
		}
	}

	start := time.Now()
	resp, err := d.Base.RoundTrip(req)
	elapsed := time.Since(start).Round(time.Millisecond)

	d.mu.Lock()
	defer d.mu.Unlock()
	_, _ = fmt.Fprintf(d.W, "* %s %s\n", req.Method, req.URL.Redacted())
	keyAPI := strings.HasPrefix(req.URL.Path, "/api-keys")
	if len(reqBody) > 0 {
		_, _ = fmt.Fprintf(d.W, "* > %s\n", redactBody(truncateBody(reqBody), keyAPI))
	}
	if err != nil {
		_, _ = fmt.Fprintf(d.W, "* ! %v (%s)\n", err, elapsed)
		return resp, err
	}
	_, _ = fmt.Fprintf(d.W, "* < %d (%s)\n", resp.StatusCode, elapsed)
	// Peek the response body without consuming it for the real reader.
	if resp.Body != nil {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, debugBodyCap+1))
		rest := resp.Body
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(peek), rest), rest}
		if len(peek) > 0 {
			_, _ = fmt.Fprintf(d.W, "* < %s\n", redactBody(truncateBody(peek), keyAPI))
		}
	}
	return resp, nil
}

func truncateBody(b []byte) string {
	if len(b) > debugBodyCap {
		return string(b[:debugBodyCap]) + "…(truncated)"
	}
	return string(b)
}

// secretField matches a JSON string member whose name holds secrets, in
// raw (possibly truncated, so not always parseable) body text. The value
// may be cut off by the body cap, so a missing closing quote still
// matches up to the end of the text.
var secretField = regexp.MustCompile(`"((?:[A-Za-z_]*_)?(?:api_key|secret|password|token|private_key))"(\s*:\s*)"(?:[^"\\]|\\.)*"?`)

// keyField matches a "key" member. It is redacted only on /api-keys,
// where it holds key material; elsewhere "key" names a log field (top
// keys, fields), which a debug trace should show.
var keyField = regexp.MustCompile(`"(key)"(\s*:\s*)"(?:[^"\\]|\\.)*"?`)

// redactBody replaces the values of secret-bearing JSON string members
// with "REDACTED". keyAPI also redacts "key" (true for /api-keys paths).
func redactBody(body string, keyAPI bool) string {
	body = secretField.ReplaceAllString(body, `"$1"$2"REDACTED"`)
	if keyAPI {
		body = keyField.ReplaceAllString(body, `"$1"$2"REDACTED"`)
	}
	return body
}
