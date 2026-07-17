// Package ingest sends events to Bronto's ingestion host (a separate host
// from the REST API): NDJSON bodies, routing headers, optional gzip.
package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bronto-community/bronto-cli/internal/api"
	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func URL(region, override string) string {
	if override != "" {
		return override
	}
	return fmt.Sprintf("https://ingestion.%s.bronto.io", region)
}

// LineToEvent converts one input line into an ingestion event. JSON objects
// pass through (timestamp added when absent; message backfilled with the
// raw line when absent or not a non-empty string); anything else becomes
// {"message": line, "timestamp": now}.
func LineToEvent(line string, now func() time.Time) map[string]any {
	if now == nil {
		now = time.Now
	}
	var obj map[string]any
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) > 0 && trimmed[0] == '{' && json.Unmarshal([]byte(line), &obj) == nil && obj != nil {
		if _, ok := obj["timestamp"]; !ok {
			obj["timestamp"] = now().UTC().Format(time.RFC3339)
		}
		if msg, ok := obj["message"].(string); !ok || msg == "" {
			obj["message"] = line
		}
		return obj
	}
	return map[string]any{
		"message":   line,
		"timestamp": now().UTC().Format(time.RFC3339),
	}
}

type Sender struct {
	HTTP       *http.Client
	URL        string
	Dataset    string
	Collection string
	Tags       string
	Gzip       bool
}

func (s *Sender) Send(ctx context.Context, events []map[string]any) error {
	if len(events) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf) // Encode appends \n after each object -> NDJSON
	enc.SetEscapeHTML(false)     // messages are log text, not embedded in HTML; don't mangle <>&"'
	for _, ev := range events {
		msg, ok := ev["message"].(string)
		if !ok || msg == "" {
			return clierr.New("usage_missing_message",
				"every event needs a non-empty \"message\" field")
		}
		if err := enc.Encode(ev); err != nil {
			return err
		}
	}
	body := buf.Bytes()
	if s.Gzip {
		var zbuf bytes.Buffer
		zw := gzip.NewWriter(&zbuf)
		if _, err := zw.Write(body); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		body = zbuf.Bytes()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Gzip {
		req.Header.Set("Content-Encoding", "gzip")
	}
	if s.Dataset != "" {
		req.Header.Set("x-bronto-dataset", s.Dataset)
	}
	if s.Collection != "" {
		req.Header.Set("x-bronto-collection", s.Collection)
	}
	if s.Tags != "" {
		req.Header.Set("x-bronto-tags", s.Tags)
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return clierr.New("network_error", err.Error()).WithRetryable().
			WithHint("Check your network and the ingestion URL / region.")
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		return clierr.New("payload_too_large",
			fmt.Sprintf("ingestion rejected a %d-byte batch (10 MB cap)", len(body))).
			WithHint("Reduce --batch-size.")
	}
	return errOrNil(api.ErrorFromStatus(resp.StatusCode, respBody))
}

// errOrNil avoids returning a non-nil interface holding a nil *clierr.Error.
func errOrNil(e *clierr.Error) error {
	if e == nil {
		return nil
	}
	return e
}

// Batcher accumulates events until an event-count or byte cap is reached.
type Batcher struct {
	MaxEvents int
	MaxBytes  int
	events    []map[string]any
	bytes     int
}

func (b *Batcher) Add(ev map[string]any) bool {
	b.events = append(b.events, ev)
	if enc, err := json.Marshal(ev); err == nil {
		b.bytes += len(enc) + 1
	}
	return len(b.events) >= b.MaxEvents || b.bytes >= b.MaxBytes
}

func (b *Batcher) Drain() []map[string]any {
	out := b.events
	b.events = nil
	b.bytes = 0
	return out
}

func (b *Batcher) Len() int { return len(b.events) }
