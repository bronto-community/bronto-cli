package bronto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"

	"github.com/bronto-community/bronto-cli/internal/api"
	"github.com/bronto-community/bronto-cli/internal/clierr"
)

type Client struct {
	http    *http.Client
	baseURL string
}

func NewClient(h *http.Client, baseURL string) *Client {
	return &Client{http: h, baseURL: baseURL}
}

func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	payload, err := json.Marshal(req.Body())
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/search",
		bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	var out SearchResponse
	if err := c.do(httpReq, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetJSON(ctx context.Context, path string, params url.Values, out any) error {
	u := c.baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

// ClassifyTransportError maps a failed round trip to the CLI's typed
// errors: context cancellation passes through unwrapped (callers detect
// ctx state), timeouts become a retryable "timeout" with the
// BRONTO_TIMEOUT hint, everything else a retryable network_error. Every
// management-API request path must use this so `bronto monitors list`
// fails exactly as helpfully as `bronto search`.
func ClassifyTransportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return err
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return clierr.New("timeout", "request timed out: "+err.Error()).WithRetryable().
			WithHint("Increase the timeout via BRONTO_TIMEOUT or 'bronto config set timeout <seconds>'.")
	}
	return clierr.New("network_error", err.Error()).WithRetryable().
		WithHint("Check your network and the API base URL / region.")
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return ClassifyTransportError(req.Context(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if apiErr := api.ErrorFromStatus(resp.StatusCode, body); apiErr != nil {
		return apiErr
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	return DecodeJSON(body, out)
}

// DecodeJSON unmarshals API payloads with json.Number preserved: Bronto
// sequence numbers exceed 2^53, so a float64 round-trip silently corrupts
// them (observed live: …516544 became …516500). Every decode of API
// response bytes must go through this, not plain json.Unmarshal.
func DecodeJSON(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(out)
}
