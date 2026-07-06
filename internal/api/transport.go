package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

// Transport adds auth + User-Agent headers and retries idempotent requests
// on 429/502/503/504, honoring Retry-After.
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
	sleep := t.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	req.Header.Set("X-BRONTO-API-KEY", t.APIKey)
	req.Header.Set("User-Agent", t.UserAgent)

	idempotent := req.Method == http.MethodGet || req.Method == http.MethodHead
	var resp *http.Response
	var err error
	for attempt := 0; ; attempt++ {
		resp, err = base.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if !idempotent || attempt >= t.MaxRetries || !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		resp.Body.Close()
		sleep(retryDelay(resp, attempt))
	}
}

func retryableStatus(s int) bool {
	return s == http.StatusTooManyRequests || s == http.StatusBadGateway ||
		s == http.StatusServiceUnavailable || s == http.StatusGatewayTimeout
}

func retryDelay(resp *http.Response, attempt int) time.Duration {
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
		Timeout: 30 * time.Second,
		Transport: &Transport{
			APIKey:     apiKey,
			UserAgent:  "bronto-cli/" + version,
			MaxRetries: 2,
		},
	}
}

// ErrorFromStatus maps a non-2xx API response to a typed error. Nil for 2xx.
func ErrorFromStatus(status int, body []byte) *clierr.Error {
	if status >= 200 && status < 300 {
		return nil
	}
	msg := fmt.Sprintf("Bronto API returned %d", status)
	var apiMsg struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &apiMsg) == nil && apiMsg.Message != "" {
		msg = fmt.Sprintf("Bronto API returned %d: %s", status, apiMsg.Message)
	}
	switch {
	case status == http.StatusUnauthorized:
		return clierr.New("auth_invalid_key", msg).
			WithHint("Check BRONTO_API_KEY or run 'bronto auth status'.")
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
