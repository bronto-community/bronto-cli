package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"regexp"
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

// originColorCode replicates renderTailLine's fnv-hash-based color pick so
// tests can assert the exact ANSI sequence without duplicating internals.
func originColorCode(origin string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(origin))
	return originColors[h.Sum32()%uint32(len(originColors))]
}

func TestRenderTailLine(t *testing.T) {
	matching := []*regexp.Regexp{regexp.MustCompile(`err\w*`)}
	nonMatching := []*regexp.Regexp{regexp.MustCompile(`nope`)}

	cases := []struct {
		name       string
		color      bool
		origin     string
		highlights []*regexp.Regexp
		want       string
	}{
		{
			name: "no color, no origin",
			want: "t1 an error",
		},
		{
			name:   "no color, with origin",
			origin: "svc-a",
			want:   "t1 svc-a an error",
		},
		{
			name:       "no color, highlight is irrelevant when color is off",
			highlights: matching,
			want:       "t1 an error",
		},
		{
			name:  "color, no origin, no highlight match",
			color: true,
			want:  "\x1b[2mt1\x1b[0m an error",
		},
		{
			name:       "color, no origin, highlight matches",
			color:      true,
			highlights: matching,
			want:       "\x1b[2mt1\x1b[0m an \x1b[1;33merror\x1b[0m",
		},
		{
			name:       "color, no origin, highlight does not match",
			color:      true,
			highlights: nonMatching,
			want:       "\x1b[2mt1\x1b[0m an error",
		},
		{
			name:   "color, with origin, no highlight",
			color:  true,
			origin: "svc-a",
			want:   "\x1b[2mt1\x1b[0m \x1b[" + originColorCode("svc-a") + "msvc-a\x1b[0m an error",
		},
		{
			name:       "color, with origin, highlight matches",
			color:      true,
			origin:     "svc-b",
			highlights: matching,
			want: "\x1b[2mt1\x1b[0m \x1b[" + originColorCode("svc-b") +
				"msvc-b\x1b[0m an \x1b[1;33merror\x1b[0m",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := map[string]any{"@time": "t1"}
			if c.origin != "" {
				ev["@origin"] = c.origin
			}
			got := renderTailLine(ev, "an error", c.highlights, c.color)
			if got != c.want {
				t.Fatalf("renderTailLine = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRenderTailLineOriginAbsentVsNil pins: a present-but-nil @origin is
// treated the same as an absent one (no origin segment rendered).
func TestRenderTailLineOriginAbsentVsNil(t *testing.T) {
	got := renderTailLine(map[string]any{"@time": "t1", "@origin": nil}, "raw", nil, false)
	if got != "t1 raw" {
		t.Fatalf("nil @origin should be treated as absent, got %q", got)
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

func TestRenderTailLineLevelColor(t *testing.T) {
	ev := map[string]any{"@time": "t1", "@status": "error", "@raw": "boom"}
	line := renderTailLine(ev, "boom", nil, true)
	if !strings.Contains(line, "\x1b[1;31mERROR\x1b[0m") {
		t.Fatalf("error level must render red: %q", line)
	}
	if plain := renderTailLine(ev, "boom", nil, false); plain != "t1 error boom" {
		t.Fatalf("plain line = %q", plain)
	}
}

func TestLevelCellColor(t *testing.T) {
	if levelCellColor("@status", "error") == "" || levelCellColor("level", "warn") == "" {
		t.Fatal("severity columns must color")
	}
	if levelCellColor("name", "error") != "" {
		t.Fatal("non-severity columns must not color")
	}
}
