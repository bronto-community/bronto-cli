package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func TestTailNoFollowSinglePollDedupSorted(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"events":[
			{"@sequence":2,"@raw":"second","@time":"t2"},
			{"@sequence":1,"@raw":"first","@time":"t1"},
			{"@sequence":1,"@raw":"first","@time":"t1"}
		]}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"tail", "--no-follow", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("polls = %d, want 1", calls.Load())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 deduped lines, got %q", out.String())
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil || first["@raw"] != "first" {
		t.Fatalf("ordering wrong: %q", lines[0])
	}
}

func TestTailIncludeExcludeFilters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"events":[
			{"@sequence":1,"@raw":"error in api"},
			{"@sequence":2,"@raw":"error in healthz"},
			{"@sequence":3,"@raw":"all good"}
		]}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"tail", "--no-follow", "--include", "error", "--exclude", "healthz",
		"-d", "11111111-1111-1111-1111-111111111111", "--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); strings.Count(got, "\n") != 0 || !strings.Contains(got, "error in api") {
		t.Fatalf("filtered output = %q", got)
	}
}

func TestTailInvalidRegexIsUsageError(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"tail", "--no-follow", "--include", "([", "-d", "x", "--api-key", "k"})
	if err := root.Execute(); err == nil {
		t.Fatal("invalid regex must error")
	}
}

func TestTailFollowStopsOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"tail", "-d", "11111111-1111-1111-1111-111111111111",
		"--interval", "1s", "--base-url", srv.URL, "--api-key", "k"})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled tail must exit clean: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tail did not stop on context cancellation")
	}
}

// TestTailAppliesJQFilter pins: tail builds its printer through
// App.PrinterFor (not output.NewPrinter directly), so --jq is honored
// instead of silently ignored — output lines are the jq results, not the
// full event objects.
func TestTailAppliesJQFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"events":[
			{"@sequence":1,"@raw":"first","@time":"t1"},
			{"@sequence":2,"@raw":"second","@time":"t2"}
		]}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"tail", "--no-follow", "--jq", `."@raw"`,
		"-d", "11111111-1111-1111-1111-111111111111", "--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %q", out.String())
	}
	for i, want := range []string{`"first"`, `"second"`} {
		if lines[i] != want {
			t.Fatalf("line %d = %q, want %q (jq filter must apply, not the full row)", i, lines[i], want)
		}
	}
}

func TestTailRejectsNonStreamingFormats(t *testing.T) {
	for _, f := range []string{"json", "csv"} {
		root := NewRootCmd()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"tail", "--no-follow", "-o", f,
			"-d", "11111111-1111-1111-1111-111111111111", "--api-key", "k"})
		err := root.Execute()
		if err == nil || clierr.ExitCode(err) != 2 {
			t.Fatalf("-o %s: want usage exit 2, got %v (%d)", f, err, clierr.ExitCode(err))
		}
	}
}
