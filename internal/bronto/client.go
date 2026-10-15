package bronto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/bronto-community/bronto-cli/internal/api"
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
	// Search is a read: mark it retry-safe so transient 429s (rate
	// limits) and 5xx blips retry with backoff instead of killing a tail
	// mid-stream. NewRequest with a bytes.Reader sets GetBody, so the
	// transport can replay the payload.
	httpReq.Header.Set(api.IdempotentHint, "true")
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

func (c *Client) do(req *http.Request, out any) error {
	resp, err := api.Do(c.http, req)
	if err != nil {
		return err
	}
	if out == nil || len(resp.Body) == 0 {
		return nil
	}
	return DecodeJSON(resp.Body, out)
}

// DecodeJSON unmarshals API payloads with json.Number preserved: Bronto
// sequence numbers exceed 2^53, so a float64 round-trip silently corrupts
// them (observed live: …516544 became …516500). Every decode of API
// response bytes must go through this, not plain json.Unmarshal.
//
// Like json.Unmarshal, it rejects anything but whitespace after the value:
// a bare json.Decoder stops after the first value, so `-f window=20
// minutes` decoded as the number 20 and the rest was silently dropped.
func DecodeJSON(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("invalid JSON: unexpected data after the top-level value")
	}
	return nil
}
