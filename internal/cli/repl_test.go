package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/term"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

type scriptedTerm struct {
	lines []string
	i     int
}

func (s *scriptedTerm) ReadLine() (string, error) {
	if s.i >= len(s.lines) {
		return "", io.EOF
	}
	l := s.lines[s.i]
	s.i++
	return l, nil
}

func (s *scriptedTerm) SetPrompt(string) {}

func replTTY(t *testing.T) {
	t.Helper()
	oldOut, oldIn := stdoutIsTTY, stdinIsTTY
	stdoutIsTTY = func() bool { return true }
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY, stdinIsTTY = oldOut, oldIn })
}

func replServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			_, _ = w.Write([]byte(`{"explain":{"Matching events":"143"},"events":[
				{"@time":"2026-07-21 14:31:58.000 UTC","@status":"error","@raw":"POST /api/v1/checkout 502"},
				{"@time":"2026-07-21 14:31:57.000 UTC","@status":"error","@raw":"POST /api/v1/checkout 500"}
			]}`))
		case "/logs":
			_, _ = w.Write([]byte(`{"logs":[
				{"log":"web","collection":"prod","log_id":"11111111-1111-1111-1111-111111111111"},
				{"log":"app","collection":"prod","log_id":"22222222-2222-2222-2222-222222222222"}
			]}`))
		case "/top-keys":
			_, _ = w.Write([]byte(`{"11111111-1111-1111-1111-111111111111":{"status":{"rank":9,"type":"NUMBER","field_type":"JSON_KVP"}}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
}

func runRepl(t *testing.T, srv *httptest.Server, lines []string) (string, string) {
	t.Helper()
	replTTY(t)
	oldTerm := newReplTerm
	newReplTerm = func(*App) (replTerm, func(), error) {
		return &scriptedTerm{lines: lines}, func() {}, nil
	}
	t.Cleanup(func() { newReplTerm = oldTerm })

	root := NewRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"repl", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String()
}

func TestReplRefusesNonTTY(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"repl", "-d", "11111111-1111-1111-1111-111111111111", "--api-key", "k"})
	err := root.Execute()
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_repl_requires_tty" || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage_repl_requires_tty exit 2, got %v", err)
	}
}

func TestReplQueryAndPaging(t *testing.T) {
	srv := replServer(t)
	defer srv.Close()
	out, _ := runRepl(t, srv, []string{"status >= 500", `\more`, `\q`})
	if !strings.Contains(out, "143 events — showing 2 most recent") {
		t.Fatalf("result header missing:\n%s", out)
	}
	// color is on under the TTY stub: match around the dim escape
	if !strings.Contains(out, "error POST /api/v1/checkout 502") || !strings.Contains(out, "14:31:58") {
		t.Fatalf("result line missing:\n%s", out)
	}
	if !strings.Contains(out, "No more results.") {
		t.Fatalf("\\more past the end must say so:\n%s", out)
	}
}

func TestReplMetaCommands(t *testing.T) {
	srv := replServer(t)
	defer srv.Close()
	out, errb := runRepl(t, srv, []string{
		`\since 1h`, `\limit 50`, `\d`, `\d prod/app`, `\fields`, `\bogus`, `\help`, `\q`,
	})
	for _, want := range []string{
		"window: last 1 hour",
		"limit: 50",
		"prod/app", // \d list contains it
		"dataset: prod/app",
		"status", // \fields
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(errb, "unknown meta-command") {
		t.Fatalf("unknown meta must error: %q", errb)
	}
	if !strings.Contains(out, `\tail`) { // help text mentions it
		t.Fatalf("help missing:\n%s", out)
	}
}

func TestReplErrorKeepsSessionAlive(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path %s", r.URL.Path)
		}
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"bad query"}`))
			return
		}
		_, _ = w.Write([]byte(`{"events":[{"@time":"2026-07-21 14:31:58.000 UTC","@raw":"ok"}]}`))
	}))
	defer srv.Close()
	out, errb := runRepl(t, srv, []string{"bad ===", "good", `\q`})
	if !strings.Contains(errb, "Error:") {
		t.Fatalf("first query error not reported: %q", errb)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("session must survive an error:\n%s", out)
	}
}

func TestReplTailStopsOnCancelledContext(t *testing.T) {
	srv := replServer(t)
	defer srv.Close()
	oldCtx := replCtx
	replCtx = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // pre-cancelled: \tail must return to the prompt immediately
		return ctx, cancel
	}
	t.Cleanup(func() { replCtx = oldCtx })
	out, _ := runRepl(t, srv, []string{`\tail`, `\q`})
	if !strings.Contains(out, "(tail stopped)") {
		t.Fatalf("tail must stop on cancel:\n%s", out)
	}
}

func TestReplHistoryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	getenv := func(k string) string {
		if k == "BRONTO_CONFIG_DIR" {
			return dir
		}
		return ""
	}
	tm := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{strings.NewReader(""), io.Discard}, "> ")
	tm.History.Add("first")
	tm.History.Add("second")
	saveReplHistory(tm, getenv)

	b, err := os.ReadFile(filepath.Join(dir, "bronto", "repl_history"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "first\nsecond\n" {
		t.Fatalf("history file = %q", string(b))
	}

	tm2 := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{strings.NewReader(""), io.Discard}, "> ")
	loadReplHistory(tm2, getenv)
	if tm2.History.Len() != 2 || tm2.History.At(0) != "second" {
		t.Fatalf("reloaded history wrong: len=%d at0=%q", tm2.History.Len(), tm2.History.At(0))
	}
}

func TestRenderReplLine(t *testing.T) {
	ev := map[string]any{
		"@time": "2026-07-21 14:31:58.123 UTC", "@status": "error",
		"@raw": strings.Repeat("x", 300),
	}
	line := renderReplLine(ev, false)
	if !strings.HasPrefix(line, "14:31:58 error x") {
		t.Fatalf("line = %q", line)
	}
	if !strings.HasSuffix(line, "…") || len([]rune(line)) > 220 {
		t.Fatalf("raw not capped: len=%d", len([]rune(line)))
	}
	colored := renderReplLine(ev, true)
	if !strings.Contains(colored, "\x1b[2m14:31:58\x1b[0m") {
		t.Fatalf("time not dimmed: %q", colored)
	}
}

func TestReplRawTermAndHistoryPath(t *testing.T) {
	tm := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{strings.NewReader("hello\r"), io.Discard}, "> ")
	rt := &rawTerm{t: tm, raw: func() (func(), error) { return func() {}, nil }}
	rt.SetPrompt("p> ")
	line, err := rt.ReadLine()
	if err != nil || line != "hello" {
		t.Fatalf("line=%q err=%v", line, err)
	}

	if p := replHistoryPath(func(string) string { return "" }); !strings.HasSuffix(p, filepath.Join("bronto", "repl_history")) {
		t.Fatalf("default path = %q", p)
	}
}

func TestReplEmptyLinesEOFAndVariants(t *testing.T) {
	srv := replServer(t)
	defer srv.Close()
	// no \q: the scripted term returns io.EOF, which must exit cleanly.
	out, _ := runRepl(t, srv, []string{"", "   ", `\quit`})
	if out == "" {
		t.Fatal("no output")
	}
	out2, errb := runRepl(t, srv, []string{`\since`, `\limit`, `\limit x`, "quit"})
	_ = out2
	for _, want := range []string{"takes one duration", "takes one number", "limit must be between"} {
		if !strings.Contains(errb, want) {
			t.Fatalf("missing %q in %q", want, errb)
		}
	}
}

func TestReplQueryAborted(t *testing.T) {
	srv := replServer(t)
	defer srv.Close()
	oldCtx := replCtx
	replCtx = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, cancel
	}
	t.Cleanup(func() { replCtx = oldCtx })
	out, _ := runRepl(t, srv, []string{"status >= 500", `\q`})
	if !strings.Contains(out, "(query aborted)") {
		t.Fatalf("abort note missing:\n%s", out)
	}
}

func TestReplTailStreamsThenStops(t *testing.T) {
	oldEvery := replTailEvery
	replTailEvery = time.Millisecond
	t.Cleanup(func() { replTailEvery = oldEvery })
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path %s", r.URL.Path)
			return
		}
		calls++
		if calls > 1 { // second poll: stop the tail deterministically
			cancel()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"events":[{"@sequence":1,"@time":"t1","@raw":"tailed-line","@origin":"web"}]}`))
	}))
	defer srv.Close()
	oldCtx := replCtx
	replCtx = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { replCtx = oldCtx })
	out, _ := runRepl(t, srv, []string{`\tail`, `\q`})
	if !strings.Contains(out, "tailed-line") || !strings.Contains(out, "(tail stopped)") {
		t.Fatalf("tail output wrong:\n%s", out)
	}
}
