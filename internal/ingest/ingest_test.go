package ingest

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

var fixedNow = func() time.Time { return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC) }

func TestURL(t *testing.T) {
	if got := URL("eu", ""); got != "https://ingestion.eu.bronto.io" {
		t.Fatalf("got %q", got)
	}
	if got := URL("eu", "http://localhost:1"); got != "http://localhost:1" {
		t.Fatalf("override: %q", got)
	}
}

func TestLineToEvent(t *testing.T) {
	ev := LineToEvent("plain log line", fixedNow)
	if ev["message"] != "plain log line" || ev["timestamp"] != "2026-07-07T12:00:00Z" {
		t.Fatalf("plain: %v", ev)
	}
	ev2 := LineToEvent(`{"message":"structured","level":"warn","timestamp":"2026-01-01T00:00:00Z"}`, fixedNow)
	if ev2["level"] != "warn" || ev2["timestamp"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("passthrough: %v", ev2)
	}
	ev3 := LineToEvent(`{"level":"info"}`, fixedNow) // JSON object without message
	if ev3["message"] != `{"level":"info"}` || ev3["timestamp"] == nil {
		t.Fatalf("no-message object: %v", ev3)
	}
	ev4 := LineToEvent(`[1,2]`, fixedNow) // JSON but not an object -> plain
	if ev4["message"] != "[1,2]" {
		t.Fatalf("non-object: %v", ev4)
	}
	line5 := `{"message": 123}`
	ev5 := LineToEvent(line5, fixedNow) // non-string message -> backfilled with raw line
	if ev5["message"] != line5 {
		t.Fatalf("non-string message: %v", ev5)
	}
}

func TestSendNDJSONHeadersAndGzip(t *testing.T) {
	var gotBody string
	var gotHdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHdr = r.Header.Clone()
		var rd io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			rd = gz
		}
		b, _ := io.ReadAll(rd)
		gotBody = string(b)
	}))
	defer srv.Close()

	s := &Sender{HTTP: srv.Client(), URL: srv.URL,
		Dataset: "app-logs", Collection: "prod", Tags: "env=prod,team=core", Gzip: true}
	err := s.Send(context.Background(), []map[string]any{
		{"message": "one", "timestamp": "t1"},
		{"message": "two", "timestamp": "t2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(gotBody), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson lines = %d: %q", len(lines), gotBody)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil || first["message"] != "one" {
		t.Fatalf("line0 = %q", lines[0])
	}
	if gotHdr.Get("x-bronto-dataset") != "app-logs" ||
		gotHdr.Get("x-bronto-collection") != "prod" ||
		gotHdr.Get("x-bronto-tags") != "env=prod,team=core" ||
		gotHdr.Get("Content-Type") != "application/json" ||
		gotHdr.Get("Content-Encoding") != "gzip" {
		t.Fatalf("headers = %v", gotHdr)
	}
}

func TestSendRejectsMissingMessage(t *testing.T) {
	s := &Sender{HTTP: http.DefaultClient, URL: "http://127.0.0.1:1"}
	err := s.Send(context.Background(), []map[string]any{{"level": "info"}})
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2 before any network, got %v", err)
	}
}

func TestSendMapsStatuses(t *testing.T) {
	for _, c := range []struct {
		status int
		code   string
		exit   int
	}{
		{413, "payload_too_large", 1},
		{429, "rate_limited", 5},
		{401, "auth_invalid_key", 3},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
		}))
		s := &Sender{HTTP: srv.Client(), URL: srv.URL}
		err := s.Send(context.Background(), []map[string]any{{"message": "x"}})
		srv.Close()
		if err == nil || clierr.ExitCode(err) != c.exit {
			t.Fatalf("status %d: got %v (exit %d), want exit %d", c.status, err, clierr.ExitCode(err), c.exit)
		}
	}
}

func TestSendEmptyIsNoop(t *testing.T) {
	s := &Sender{HTTP: http.DefaultClient, URL: "http://127.0.0.1:1"} // would fail if dialed
	if err := s.Send(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestBatcher(t *testing.T) {
	b := &Batcher{MaxEvents: 2, MaxBytes: 1 << 20}
	if full := b.Add(map[string]any{"message": "a"}); full {
		t.Fatal("not full at 1/2")
	}
	if full := b.Add(map[string]any{"message": "b"}); !full {
		t.Fatal("full at 2/2")
	}
	got := b.Drain()
	if len(got) != 2 || b.Len() != 0 {
		t.Fatalf("drain = %d, len = %d", len(got), b.Len())
	}
	// byte-based flush
	bb := &Batcher{MaxEvents: 1000, MaxBytes: 30}
	bb.Add(map[string]any{"message": "aaaaaaaaaaaaaaa"}) // ~30 bytes encoded
	if full := bb.Add(map[string]any{"message": "b"}); !full {
		t.Fatal("byte cap must trigger")
	}
}
