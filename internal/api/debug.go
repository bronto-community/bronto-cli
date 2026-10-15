package api

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bronto-community/bronto-cli/internal/termsafe"
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
			reqBody = readDebugBody(rc, req.Header.Get("Content-Encoding"))
			_ = rc.Close()
		}
	}

	start := time.Now()
	resp, err := d.Base.RoundTrip(req)
	elapsed := time.Since(start).Round(time.Millisecond)

	d.mu.Lock()
	defer d.mu.Unlock()
	_, _ = fmt.Fprintf(d.W, "* %s %s\n", req.Method, debugURL(req.URL))
	keyAPI := isAPIKeysPath(req.URL.Path)
	if len(reqBody) > 0 {
		_, _ = fmt.Fprintf(d.W, "* > %s\n", termsafe.Text(redactBody(truncateBody(reqBody), keyAPI)))
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
			_, _ = fmt.Fprintf(d.W, "* < %s\n", termsafe.Text(redactBody(truncateBody(peek), keyAPI)))
		}
	}
	return resp, nil
}

// readDebugBody reads up to debugBodyCap+1 bytes of a request body for the
// trace. A gzip body (bronto send compresses by default) is decompressed
// so the trace shows the events rather than binary noise; if that fails,
// a placeholder says what was sent.
func readDebugBody(r io.Reader, encoding string) []byte {
	if !strings.EqualFold(encoding, "gzip") {
		b, _ := io.ReadAll(io.LimitReader(r, debugBodyCap+1))
		return b
	}
	raw, _ := io.ReadAll(r)
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err == nil {
		b, err := io.ReadAll(io.LimitReader(zr, debugBodyCap+1))
		if err == nil || len(b) > debugBodyCap {
			return b
		}
	}
	return []byte(fmt.Sprintf("<gzip body, %d bytes>", len(raw)))
}

// secretQueryKey matches query parameter names whose values are secrets:
// anything mentioning a token, key, secret, password, signature or
// credential (token=, api_key=, X-Amz-Signature=, sig=, …).
var secretQueryKey = regexp.MustCompile(`(?i)token|key|secret|passw|signature|^sig$|credential|auth`)

// debugURL renders u for the trace with the userinfo password and the
// values of secret-looking query parameters replaced. Unlike RedactURL
// (telemetry, which drops every query value), other parameters stay
// readable: seeing limit=10 or the where clause is the point of --debug.
func debugURL(u *url.URL) string {
	c := *u
	if c.RawQuery != "" {
		parts := strings.Split(c.RawQuery, "&")
		for i, kv := range parts {
			k, _, hasVal := strings.Cut(kv, "=")
			name, err := url.QueryUnescape(k)
			if err != nil {
				name = k
			}
			if hasVal && secretQueryKey.MatchString(name) {
				parts[i] = k + "=REDACTED"
			}
		}
		c.RawQuery = strings.Join(parts, "&")
	}
	return c.Redacted()
}

func truncateBody(b []byte) string {
	if len(b) > debugBodyCap {
		return string(b[:debugBodyCap]) + "…(truncated)"
	}
	return string(b)
}

// secretField matches a JSON string member whose name holds secrets
// (api_key, *_secret, *password, *private_key, or any name ending in
// "token"; case-insensitive, so accessToken too), in
// raw (possibly truncated, so not always parseable) body text. The value
// may be cut off by the body cap, so a missing closing quote still
// matches up to the end of the text.
var secretField = regexp.MustCompile(`"((?i:(?:[a-z0-9_]*_)?(?:api_key|secret|password|private_key)|[a-z0-9_]*token))"(\s*:\s*)"(?:[^"\\]|\\.)*"?`)

// keyField matches a "key" member. It is redacted only on /api-keys,
// where it holds key material; elsewhere "key" names a log field (top
// keys, fields), which a debug trace should show.
var keyField = regexp.MustCompile(`"(key)"(\s*:\s*)"(?:[^"\\]|\\.)*"?`)

// isAPIKeysPath reports whether path addresses the API keys resource,
// with any base_url path prefix (/proxy/api-keys) and any key id after it.
func isAPIKeysPath(path string) bool {
	return strings.Contains(path+"/", "/api-keys/")
}

// redactBody replaces the values of secret-bearing JSON string members
// with "REDACTED". keyAPI also redacts "key" (true for /api-keys paths).
func redactBody(body string, keyAPI bool) string {
	body = secretField.ReplaceAllString(body, `"$1"$2"REDACTED"`)
	if keyAPI {
		body = keyField.ReplaceAllString(body, `"$1"$2"REDACTED"`)
	}
	return body
}
