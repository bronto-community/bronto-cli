package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// IdempotentHint marks a POST as safe to retry (search queries are
// reads in POST clothing). The header is internal — stripped before the
// request leaves the transport.
const IdempotentHint = "X-Bronto-Cli-Idempotent"

// Transport adds auth + User-Agent headers and retries idempotent requests
// on 429/502/503/504, honoring Retry-After. GET/HEAD are always eligible;
// POSTs opt in via IdempotentHint.
type Transport struct {
	APIKey     string
	UserAgent  string
	Base       http.RoundTripper
	MaxRetries int
	Sleep      func(time.Duration) // injectable for tests; nil = time.Sleep
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	idempotent := req.Method == http.MethodGet || req.Method == http.MethodHead ||
		req.Header.Get(IdempotentHint) == "true"
	replayable := req.Body == nil || req.GetBody != nil

	body := req.Body
	var resp *http.Response
	var err error
	for attempt := 0; ; attempt++ {
		attemptReq := req.Clone(req.Context())
		attemptReq.Body = body
		attemptReq.Header.Del(IdempotentHint)
		attemptReq.Header.Set("X-BRONTO-API-KEY", t.APIKey)
		attemptReq.Header.Set("User-Agent", t.UserAgent)

		resp, err = base.RoundTrip(attemptReq)
		if err != nil {
			return nil, err
		}
		if !idempotent || !replayable || attempt >= t.MaxRetries || !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		_ = resp.Body.Close()

		if req.GetBody != nil {
			body, err = req.GetBody()
			if err != nil {
				return nil, err
			}
		}

		if err := t.wait(req, retryDelay(resp, attempt)); err != nil {
			return nil, err
		}
	}
}

// wait blocks for d, or returns early with the context's error if req's
// context is canceled first. If Sleep is set (tests), it is used unconditionally
// and is not context-aware.
func (t *Transport) wait(req *http.Request, d time.Duration) error {
	if t.Sleep != nil {
		t.Sleep(d)
		return nil
	}
	select {
	case <-time.After(d):
		return nil
	case <-req.Context().Done():
		return req.Context().Err()
	}
}

func retryableStatus(s int) bool {
	return s == http.StatusTooManyRequests || s == http.StatusBadGateway ||
		s == http.StatusServiceUnavailable || s == http.StatusGatewayTimeout
}

func retryDelay(resp *http.Response, attempt int) time.Duration {
	d := computeRetryDelay(resp, attempt)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func computeRetryDelay(resp *http.Response, attempt int) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			if secs < 0 {
				return 0
			}
			return time.Duration(secs) * time.Second
		}
		if t, err := http.ParseTime(ra); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
			return 0
		}
	}
	return time.Duration(500*(1<<attempt)) * time.Millisecond
}

func NewHTTPClient(apiKey, version string) *http.Client {
	return &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: refuseCrossHostRedirect,
		Transport: &Transport{
			APIKey:     apiKey,
			UserAgent:  "bronto-cli/" + version,
			MaxRetries: 2,
		},
	}
}

// refuseCrossHostRedirect stops the client before it follows a redirect to
// a different host. The API key is attached per-hop inside
// Transport.RoundTrip as the custom X-BRONTO-API-KEY header, which
// net/http does NOT strip on cross-domain redirects (its stripping only
// covers Authorization/Cookie/etc. set on the original request), so a
// redirect to an attacker host would otherwise carry the key. Same-host
// redirects (the only kind Bronto's API issues) still follow normally.
// 2026-07-23 audit.
func refuseCrossHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("refusing to follow cross-host redirect to %q (would leak the API key); "+
			"if this endpoint is legitimate, set base_url to it directly", req.URL.Host)
	}
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return nil
}

// ErrorFromStatus maps a non-2xx API response to a typed error. Nil for 2xx.
func ErrorFromStatus(status int, body []byte) *clierr.Error {
	if status >= 200 && status < 300 {
		return nil
	}
	msg := fmt.Sprintf("Bronto API returned %d", status)
	var apiMsg struct {
		Message string `json:"message"`
		Details string `json:"details"`
	}
	if err := json.Unmarshal(body, &apiMsg); err == nil && (apiMsg.Message != "" || apiMsg.Details != "") {
		// Live Bronto errors carry "details"; some shapes use "message".
		// Either beats echoing the whole upstream JSON blob at the user.
		detail := apiMsg.Message
		if detail == "" {
			detail = apiMsg.Details
		}
		msg = fmt.Sprintf("Bronto API returned %d: %s", status, detail)
	} else if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		// No recognizable message field: surface a snippet of the raw body —
		// validation errors often arrive in other shapes, and hiding them
		// leaves users (and CI) debugging blind.
		if len(trimmed) > 300 {
			trimmed = trimmed[:300] + "…"
		}
		msg = fmt.Sprintf("Bronto API returned %d: %s", status, trimmed)
	}
	switch {
	case status == http.StatusUnauthorized:
		return clierr.New("auth_invalid_key", msg).
			WithHint("Run 'bronto auth login' to store a valid management key, or set BRONTO_API_KEY. 'bronto auth status' shows what is configured.")
	case status == http.StatusForbidden:
		return clierr.New("auth_insufficient_role", msg).
			WithHint("You are likely using an ingestion key. This CLI needs a management key (Settings → API Keys in the Bronto UI).").
			WithDocs("https://docs.bronto.io/api-reference/api-keys/overview")
	case status == http.StatusNotFound:
		return clierr.New("resource_not_found", msg)
	case status == http.StatusTooManyRequests:
		return clierr.New("rate_limited", msg).WithRetryable()
	case status >= 500:
		return clierr.New("api_server_error", msg).WithRetryable()
	default:
		return clierr.New("api_error", msg)
	}
}
