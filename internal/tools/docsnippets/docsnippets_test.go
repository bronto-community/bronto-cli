package docsnippets

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bronto-community/bronto-cli/internal/tools/docsmock/mock"
)

// repoRoot is the module root, three levels up from this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

var (
	buildOnce sync.Once
	binDir    string
	buildErr  error
)

// brontoBinDir builds ./cmd/bronto once per test binary and returns the
// directory holding it, to put first on the snippets' PATH. BRONTO_DOCS_BIN
// points at an existing binary instead (e.g. the one `make build` made).
func brontoBinDir(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("BRONTO_DOCS_BIN"); bin != "" {
		abs, err := filepath.Abs(bin)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := os.Symlink(abs, filepath.Join(dir, "bronto")); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	buildOnce.Do(func() {
		binDir, buildErr = os.MkdirTemp("", "bronto-docsnippets-")
		if buildErr != nil {
			return
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(binDir, "bronto"), "./cmd/bronto")
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build ./cmd/bronto: %w\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binDir
}

func TestMain(m *testing.M) {
	code := m.Run()
	if binDir != "" {
		_ = os.RemoveAll(binDir)
	}
	os.Exit(code)
}

// newRunner starts the docs mock in-process and returns a Runner wired to
// it: the environment the docs contract promises (docs/AGENTS.md) and
// nothing from the developer's shell except PATH and TMPDIR.
func newRunner(t *testing.T) *Runner {
	t.Helper()
	srv, err := mock.New(filepath.Join(repoRoot(t), "docs", "testdata", "mock"), mock.WithLogf(t.Logf))
	if err != nil {
		t.Fatal(err)
	}
	// The mock's URLs are padded to the exact length of the public URLs
	// they stand in for, so that after Replace rewrites them a table that
	// shows them (config list) keeps the column widths a reader gets.
	// pad is set after the server starts and read by handlers serving the
	// bronto subprocesses; atomic so the race detector sees the ordering.
	var padv atomic.Value
	padv.Store("")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pad := padv.Load().(string); pad != "" {
			if rest, ok := strings.CutPrefix(r.URL.Path, pad); ok && strings.HasPrefix(rest, "/") {
				r.URL.Path = rest
			}
		}
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	const publicBase, publicIngest = "https://api.eu.bronto.io", "https://ingestion.eu.bronto.io"
	var pad string
	if n := len(publicBase) - len(ts.URL); n >= 2 {
		pad = "/" + strings.Repeat("_", n-1)
	}
	padv.Store(pad)
	baseURL := ts.URL + pad
	ingestURL := ts.URL + mock.IngestPath
	if n := len(publicIngest) - len(ingestURL); n >= 1 {
		ingestURL += "/" + strings.Repeat("_", n-1) // the mock accepts IngestPath + "/..."
	}
	env := []string{
		"PATH=" + brontoBinDir(t) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"BRONTO_BASE_URL=" + baseURL,
		"BRONTO_INGEST_URL=" + ingestURL,
		"BRONTO_API_KEY=" + mock.APIKey,
		"BRONTO_REGION=eu",
		"NO_COLOR=1",
		"COLUMNS=100",
		"LINES=40",
		"TERM=dumb",
		"TZ=UTC",
		// Linux: no Secret Service, so a keychain write/read falls back to
		// the per-snippet credentials file. macOS: the per-snippet HOME
		// takes the login keychain off the search list.
		"DBUS_SESSION_BUS_ADDRESS=disabled:",
	}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		env = append(env, "TMPDIR="+tmp)
	}
	return &Runner{
		Env:     env,
		TempDir: t.TempDir,
		Reset:   srv.Reset,
		Replace: []Replacement{
			{Old: ingestURL, New: publicIngest},
			{Old: baseURL, New: publicBase},
			{Old: ts.URL, New: publicBase},
		},
	}
}

// TestDocSnippets runs every ```console test block in the docs site
// against the mock. DOCS_SNIPPETS_PRINT=1 prints each block with the
// actual output filled in, ready to paste back into the page.
func TestDocSnippets(t *testing.T) {
	root := filepath.Join(repoRoot(t), "docs", "src", "content", "docs")
	snippets, err := Extract(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(snippets) == 0 {
		t.Logf("no ```console test blocks under %s yet", root)
		return
	}
	r := newRunner(t)
	printOut := os.Getenv("DOCS_SNIPPETS_PRINT") != ""
	for _, s := range snippets {
		rel, err := filepath.Rel(repoRoot(t), s.File)
		if err == nil {
			s.File = rel
		}
		t.Run(s.Name(), func(t *testing.T) {
			results, failures := r.Run(s)
			if printOut {
				fmt.Print(Format(s, results))
			}
			for _, f := range failures {
				t.Error("\n" + f.String())
			}
		})
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		want, got string
		ok        bool
	}{
		{"a\nb", "a\nb", true},
		{"a\nb", "a  \nb\n\n", true}, // trailing whitespace and blank lines
		{"a\nb", "a\nc", false},
		{"a\n...", "a\nb\nc", true},
		{"a\n...", "a", true}, // ... matches zero lines
		{"...\nc", "a\nb\nc", true},
		{"a\n...\nc", "a\nc", true},
		{"a\n...\nc", "a\nb", false},
		{"a\n...\nc\n...", "a\nx\nc\ny\nz", true},
		{"a", "a\nb", false},
		{"a b", "a  b", false}, // inner whitespace is significant
	}
	for _, c := range cases {
		if got := Match(strings.Split(c.want, "\n"), strings.Split(c.got, "\n")); got != c.ok {
			t.Errorf("Match(%q, %q) = %v, want %v", c.want, c.got, got, c.ok)
		}
	}
}

func TestParse(t *testing.T) {
	doc := "intro\n" +
		"```console test\n" +
		"$ echo a \\\n" +
		"  b\n" +
		"a b\n" +
		"$ true\n" +
		"```\n" +
		"```sh\n$ not a test\n```\n" +
		"  ```console test tty exit=2\n" +
		"  $ exit 2\n" +
		"  ```\n" +
		"````console test\n$ echo '```'\n```\n````\n"
	snips, err := Parse("doc.md", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(snips) != 3 {
		t.Fatalf("got %d snippets, want 3: %+v", len(snips), snips)
	}
	if s := snips[0]; s.Line != 2 || len(s.Commands) != 2 || s.Commands[0].Cmd != "echo a \\\n  b" ||
		len(s.Commands[0].Want) != 1 || s.Commands[1].Line != 6 {
		t.Errorf("snippet 0 = %+v", s)
	}
	if s := snips[1]; s.ExitCode != 2 || !s.TTY || s.Commands[0].Cmd != "exit 2" {
		t.Errorf("snippet 1 (indented, tty exit=2) = %+v", s)
	}
	if snips[0].TTY {
		t.Error("snippet 0 has no tty meta word but parsed as TTY")
	}
	if got := Format(snips[1], nil); !strings.Contains(got, "```console test tty exit=2\n") {
		t.Errorf("Format must keep the tty and exit meta words, got:\n%s", got)
	}
	if s := snips[2]; len(s.Commands) != 1 || len(s.Commands[0].Want) != 1 || s.Commands[0].Want[0] != "```" {
		t.Errorf("snippet 2 (4-backtick fence) = %+v", s)
	}
	if _, err := Parse("bad.md", []byte("```console test\noutput first\n$ true\n```\n")); err == nil {
		t.Error("output before the first command must be an error")
	}
	if _, err := Parse("bad.md", []byte("```console test\n$ true\n")); err == nil {
		t.Error("an unterminated block must be an error")
	}
}

// TestSelfTestPasses proves the harness end to end on known-good blocks
// (real bronto commands against the mock).
func TestSelfTestPasses(t *testing.T) {
	snippets, err := Extract(filepath.Join("testdata", "selftest", "pass"))
	if err != nil {
		t.Fatal(err)
	}
	if len(snippets) < 3 {
		t.Fatalf("expected at least 3 self-test snippets, got %d", len(snippets))
	}
	r := newRunner(t)
	for _, s := range snippets {
		if _, failures := r.Run(s); len(failures) > 0 {
			for _, f := range failures {
				t.Error("\n" + f.String())
			}
		}
	}
}

// TestSelfTestGoesRed proves the harness fails where it must: a wrong
// expected output, a non-zero exit, an unexpected exit 0 under exit=N,
// and a wildcard that cannot match. Each failure names file:line and the
// command, and output failures carry a diff.
func TestSelfTestGoesRed(t *testing.T) {
	file := filepath.Join("testdata", "selftest", "fail", "fail.md")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	snippets, err := Parse(file, b)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		reason, contains string
	}{
		{"output", "- checkout-service  prod"},
		{"exit code", "exit code 2, want 0"},
		{"exit code", "exit code 0, want 3"},
		{"output", "+ prod        3"},
		{"output", "+ COLLECTION  DATASETS  NAMES"}, // tty: the table, not JSON
	}
	if len(snippets) != len(want) {
		t.Fatalf("fail.md has %d snippets, want %d", len(snippets), len(want))
	}
	r := newRunner(t)
	for i, s := range snippets {
		_, failures := r.Run(s)
		if len(failures) != 1 {
			t.Errorf("%s: got %d failures, want 1: %v", s.Name(), len(failures), failures)
			continue
		}
		f := failures[0]
		msg := f.String()
		loc := fmt.Sprintf("%s:%d: $ %s", file, s.Commands[0].Line, s.Commands[0].Cmd)
		if f.Reason != want[i].reason || !strings.Contains(msg, want[i].contains) || !strings.HasPrefix(msg, loc) {
			t.Errorf("%s: failure =\n%s\nwant reason %q containing %q and starting %q", s.Name(), msg, want[i].reason, want[i].contains, loc)
		}
	}
}

// TestBackgroundChildDoesNotHang pins the drain bound: a snippet that leaves
// a process holding stdout open must finish shortly after the shell exits,
// on a pipe and on a pseudo-terminal, instead of blocking until that
// process exits (or forever).
func TestBackgroundChildDoesNotHang(t *testing.T) {
	for _, tty := range []bool{false, true} {
		r := &Runner{Env: []string{"PATH=" + os.Getenv("PATH")}, TempDir: t.TempDir, Timeout: 20 * time.Second}
		start := time.Now()
		res := r.exec(Command{Cmd: "sleep 30 & echo started"}, r.Env, t.TempDir(), r.Timeout, tty)
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("tty=%v: took %s, want about %s", tty, d, ptyDrainGrace)
		}
		if !strings.Contains(res.Stdout, "started") {
			t.Errorf("tty=%v: stdout = %q, want it to contain %q", tty, res.Stdout, "started")
		}
	}
	if out, err := exec.Command("pgrep", "-f", "sleep 30").Output(); err == nil && len(out) > 0 {
		t.Errorf("background child survived the snippet: pids %s", strings.Fields(string(out)))
	}
}
