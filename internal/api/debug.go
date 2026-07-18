package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// debugBodyCap bounds how much of a request/response body the debug trace
// prints — enough to see the shape, not enough to flood a terminal.
const debugBodyCap = 2048

// DebugTransport wraps a RoundTripper and traces every request/response to
// W (typically stderr): method, URL, status, duration, and truncated
// bodies. Headers are never printed, so the API key cannot leak into the
// trace. Install as Transport.Base (INSIDE the retry loop) so each retry
// attempt traces individually.
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
	if len(reqBody) > 0 {
		_, _ = fmt.Fprintf(d.W, "* > %s\n", truncateBody(reqBody))
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
			_, _ = fmt.Fprintf(d.W, "* < %s\n", truncateBody(peek))
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
