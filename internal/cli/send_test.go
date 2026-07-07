package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
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
