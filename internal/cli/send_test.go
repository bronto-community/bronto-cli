package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func TestSendOneShotMessage(t *testing.T) {
	var body string
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	defer srv.Close()

	root := NewRootCmd()
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	root.SetArgs([]string{"send", "-d", "app", "-m", "hello world", "--no-gzip",
		"--ingest-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &ev); err != nil || ev["message"] != "hello world" {
		t.Fatalf("body = %q", body)
	}
	if hdr.Get("x-bronto-dataset") != "app" || hdr.Get("X-Bronto-Api-Key") == "" {
		t.Fatalf("headers = %v", hdr)
	}
	if !strings.Contains(errBuf.String(), "Sent 1 event") {
		t.Fatalf("summary = %q", errBuf.String())
	}
}

func TestSendStdinBatches(t *testing.T) {
	var batches atomic.Int32
	var lines atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batches.Add(1)
		b, _ := io.ReadAll(r.Body)
		lines.Add(int32(strings.Count(strings.TrimSpace(string(b)), "\n") + 1))
	}))
	defer srv.Close()

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader("l1\nl2\nl3\n"))
	root.SetArgs([]string{"send", "-d", "app", "--batch-size", "2", "--no-gzip",
		"--ingest-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if batches.Load() != 2 || lines.Load() != 3 { // 2+1
		t.Fatalf("batches=%d lines=%d, want 2/3", batches.Load(), lines.Load())
	}
}

func TestSendStructuredPassthrough(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	defer srv.Close()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader(`{"message":"m","level":"warn"}` + "\n"))
	root.SetArgs([]string{"send", "-d", "app", "--no-gzip", "--ingest-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	_ = json.Unmarshal([]byte(strings.TrimSpace(body)), &ev)
	if ev["level"] != "warn" {
		t.Fatalf("passthrough lost fields: %q", body)
	}
}

func TestSendEmptyMessageFlagIsUsageError(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"send", "-d", "app", "-m", "", "--api-key", "k"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2 (usage_missing_message) for -m \"\", got %v", err)
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_missing_message" {
		t.Fatalf("want usage_missing_message, got %v", err)
	}
}

func TestSendInvalidTag(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"send", "-d", "app", "-m", "x", "--tag", "noequals", "--api-key", "k"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v", err)
	}
}

func TestSendAuthErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"send", "-d", "app", "-m", "x", "--ingest-url", srv.URL, "--api-key", "bad"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 3 {
		t.Fatalf("want exit 3, got %v", err)
	}
}

func TestSendOversizedLineSurfacesReadError(t *testing.T) {
	var lines atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lines.Add(int32(strings.Count(strings.TrimSpace(string(b)), "\n") + 1))
	}))
	defer srv.Close()

	huge := strings.Repeat("x", 2<<20) // 2 MiB line > 1 MiB scanner buffer
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader("ok-line\n" + huge + "\n"))
	root.SetArgs([]string{"send", "-d", "app", "--no-gzip", "--flush-interval", "100ms",
		"--ingest-url", srv.URL, "--api-key", "k"})
	err := root.Execute()
	if err == nil {
		t.Fatal("oversized line must surface a read error, not exit 0")
	}
	if clierr.ExitCode(err) == 0 {
		t.Fatalf("exit = %d", clierr.ExitCode(err))
	}
	// the good line before the error must still have been delivered
	if lines.Load() != 1 {
		t.Fatalf("delivered lines = %d, want 1", lines.Load())
	}
}

// runSendInterrupted streams one line into send through a pipe that stays
// open, then cancels the command context like Ctrl-C/SIGTERM would. The
// hour-long --flush-interval keeps the ticker from flushing first.
func runSendInterrupted(t *testing.T, ingestURL string) (string, error) {
	t.Helper()
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := NewRootCmd()
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	root.SetIn(pr)
	root.SetArgs([]string{"send", "-d", "app", "--no-gzip", "--flush-interval", "1h",
		"--ingest-url", ingestURL, "--api-key", "k"})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	if _, err := pw.Write([]byte("last-line\n")); err != nil {
		t.Fatal(err)
	}
	// The pipe write returns once the scanner has the line; give the loop
	// a moment to add it to the batch before the interrupt.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		return errBuf.String(), err
	case <-time.After(10 * time.Second):
		t.Fatal("send did not stop after the interrupt")
		return "", nil
	}
}

func TestSendInterruptFlushesLastBatch(t *testing.T) {
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body.Store(string(b))
	}))
	defer srv.Close()

	stderr, err := runSendInterrupted(t, srv.URL)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("an interrupted send must return the cancellation (exit 130), got %v", err)
	}
	if got, _ := body.Load().(string); !strings.Contains(got, "last-line") {
		t.Fatalf("pending batch was not flushed after the interrupt: %q", got)
	}
	if !strings.Contains(stderr, "Sent 1 event(s) in 1 batch(es).") {
		t.Fatalf("summary = %q", stderr)
	}
}

func TestSendInterruptFlushFailureIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()

	stderr, err := runSendInterrupted(t, srv.URL)
	var se *ShutdownError
	if !errors.As(err, &se) {
		t.Fatalf("a failed final flush must be a ShutdownError, got %v", err)
	}
	if clierr.ExitCode(err) != 3 {
		t.Fatalf("exit = %d, want the auth error's 3", clierr.ExitCode(err))
	}
	if !strings.Contains(stderr, "Sent 0 event(s) in 0 batch(es) before the interrupt; the last batch was not delivered.") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// An interrupt during the EOF flush: the first request is cut off, the
// detached retry delivers the batch, and the run still exits 130.
func TestSendInterruptDuringEOFFlushExits130(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if calls.Add(1) == 1 {
			once.Do(func() { close(started) })
			<-r.Context().Done() // hold the first request until the client gives up
			return
		}
		body.Store(string(b))
	}))
	defer srv.Close()

	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(pr)
	root.SetArgs([]string{"send", "-d", "app", "--no-gzip", "--flush-interval", "1h",
		"--ingest-url", srv.URL, "--api-key", "k"})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	if _, err := pw.Write([]byte("eof-line\n")); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close() // EOF: the final flush starts
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the EOF flush never reached the server")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want the cancellation (exit 130), got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("send did not stop")
	}
	if got, _ := body.Load().(string); !strings.Contains(got, "eof-line") {
		t.Fatalf("the detached retry did not deliver the batch: %q", got)
	}
}
