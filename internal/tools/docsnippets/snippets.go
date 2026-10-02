// Package docsnippets runs the docs' tested shell snippets: every
// ```console test fenced block under docs/src/content/docs is extracted,
// each "$ " command runs through sh -c against the docs mock server
// (internal/tools/docsmock/mock) with the bronto binary built from this
// repo, and its stdout is compared with the lines that follow it.
//
// The block format is the contract in docs/AGENTS.md ("Code blocks"):
//
//   - a line starting with "$ " is a command; a trailing "\" continues it
//     on the next line
//   - the lines after it, up to the next "$ ", are the expected stdout
//   - lines compare exactly after trailing whitespace is trimmed; a line
//     that is exactly "..." matches any number of lines (zero or more)
//   - a command with no expected lines only has to exit with the expected
//     code
//   - "test exit=N" in the fence meta expects exit code N for every
//     command in the block (default 0)
//   - "test tty" in the fence meta runs every command with stdout on a
//     pseudo-terminal, so bronto behaves as it does for a reader typing at
//     a terminal (table output by default, TTY-only hints); without it
//     stdout is a pipe and bronto prints its machine (JSON/JSONL) defaults
//
// The machinery lives here (non-test code) so its own tests can drive it
// against fixture markdown; TestDocSnippets is the real entry point.
package docsnippets

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Snippet is one ```console test block.
type Snippet struct {
	File     string // path as given to Extract's root, joined with the relative path
	Line     int    // 1-based line of the opening fence
	ExitCode int    // expected exit code for every command (test exit=N)
	TTY      bool   // run commands with stdout on a pseudo-terminal (test tty)
	Commands []Command
}

// Command is one "$ " line of a snippet and its expected stdout.
type Command struct {
	Line int // 1-based line of the "$ " line
	Cmd  string
	Want []string
}

// Name is the snippet's file:line, used for subtests and failure output.
func (s Snippet) Name() string { return fmt.Sprintf("%s:%d", s.File, s.Line) }

var (
	fenceOpen = regexp.MustCompile("^([ \t]*)(`{3,}|~{3,})[ \t]*console(?:[ \t]+(.*))?$")
	exitMeta  = regexp.MustCompile(`(?:^|\s)exit=(\d+)(?:\s|$)`)
)

// Extract walks root for .md and .mdx files and returns every
// ```console test block, in path order. A missing root yields no
// snippets (the docs may not exist yet), not an error.
func Extract(root string) ([]Snippet, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".mdx")) {
			files = append(files, path)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []Snippet
	for _, f := range files {
		b, err := os.ReadFile(f) // #nosec G304 -- walking the repo's own docs tree
		if err != nil {
			return nil, err
		}
		snips, err := Parse(f, b)
		if err != nil {
			return nil, err
		}
		out = append(out, snips...)
	}
	return out, nil
}

// Parse extracts the ```console test blocks from one markdown document.
func Parse(name string, doc []byte) ([]Snippet, error) {
	var out []Snippet
	sc := bufio.NewScanner(bytes.NewReader(doc))
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	lineNo := 0
	var cur *Snippet
	var indent, fence string
	var body []string
	var bodyStart int
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if cur == nil {
			m := fenceOpen.FindStringSubmatch(line)
			if m == nil || !hasWord(m[3], "test") {
				continue
			}
			cur = &Snippet{File: name, Line: lineNo, TTY: hasWord(m[3], "tty")}
			if em := exitMeta.FindStringSubmatch(m[3]); em != nil {
				cur.ExitCode, _ = strconv.Atoi(em[1])
			}
			indent, fence, body, bodyStart = m[1], m[2], nil, lineNo+1
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, fence[:3]) && strings.Trim(trimmed, fence[:1]) == "" && len(trimmed) >= len(fence) {
			cmds, err := parseBody(name, bodyStart, body)
			if err != nil {
				return nil, err
			}
			cur.Commands = cmds
			out = append(out, *cur)
			cur = nil
			continue
		}
		body = append(body, strings.TrimPrefix(line, indent))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if cur != nil {
		return nil, fmt.Errorf("%s:%d: unterminated ```console test block", name, cur.Line)
	}
	return out, nil
}

func hasWord(meta, word string) bool {
	for _, f := range strings.Fields(meta) {
		if f == word {
			return true
		}
	}
	return false
}

func parseBody(name string, firstLine int, body []string) ([]Command, error) {
	var cmds []Command
	for i := 0; i < len(body); i++ {
		line := body[i]
		switch {
		case strings.HasPrefix(line, "$ "):
			c := Command{Line: firstLine + i, Cmd: strings.TrimPrefix(line, "$ ")}
			for strings.HasSuffix(c.Cmd, "\\") && i+1 < len(body) {
				i++
				c.Cmd += "\n" + body[i] // sh joins the backslash-newline itself
			}
			cmds = append(cmds, c)
		case len(cmds) == 0:
			if strings.TrimSpace(line) != "" {
				return nil, fmt.Errorf("%s:%d: output before the first \"$ \" command in a console test block", name, firstLine+i)
			}
		default:
			cmds[len(cmds)-1].Want = append(cmds[len(cmds)-1].Want, line)
		}
	}
	if len(cmds) == 0 {
		return nil, fmt.Errorf("%s:%d: console test block without a \"$ \" command", name, firstLine-1)
	}
	return cmds, nil
}

// normalize trims trailing whitespace per line and drops trailing blank
// lines.
func normalize(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(l, " \t\r")
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// Match reports whether got satisfies want, where a want line that is
// exactly "..." matches zero or more got lines.
func Match(want, got []string) bool {
	want, got = normalize(want), normalize(got)
	// memo[i][j]: want[i:] matches got[j:]
	memo := map[[2]int]bool{}
	var match func(i, j int) bool
	match = func(i, j int) bool {
		key := [2]int{i, j}
		if v, ok := memo[key]; ok {
			return v
		}
		var res bool
		switch {
		case i == len(want):
			res = j == len(got)
		case want[i] == "...":
			res = match(i+1, j) || (j < len(got) && match(i, j+1))
		default:
			res = j < len(got) && want[i] == got[j] && match(i+1, j+1)
		}
		memo[key] = res
		return res
	}
	return match(0, 0)
}

// Diff renders a line diff of want vs got ("-" expected, "+" actual),
// aligned on a longest common subsequence; "..." lines are shown as-is.
func Diff(want, got []string) string {
	want, got = normalize(want), normalize(got)
	n, m := len(want), len(got)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if want[i] == got[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var b strings.Builder
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && want[i] == got[j]:
			b.WriteString("  " + want[i] + "\n")
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			b.WriteString("+ " + got[j] + "\n")
			j++
		default:
			b.WriteString("- " + want[i] + "\n")
			i++
		}
	}
	return b.String()
}

// Runner executes snippet commands in an isolated environment.
type Runner struct {
	// Env is the complete environment for every command (nothing is
	// inherited). HOME and BRONTO_CONFIG_DIR are replaced per snippet
	// with fresh temp dirs.
	Env []string
	// TempDir creates a fresh directory (t.TempDir in tests).
	TempDir func() string
	// Reset is called before every command (the mock's Reset), so tail
	// cursors and other mock state start fresh.
	Reset func()
	// Replace rewrites stdout before comparison — e.g. the mock's random
	// base URL to the canonical https://api.eu.bronto.io the docs show.
	Replace []Replacement
	// Timeout bounds one command (default 30s).
	Timeout time.Duration
}

// Replacement is one Old -> New stdout rewrite.
type Replacement struct{ Old, New string }

// Result is one command's outcome.
type Result struct {
	Command  Command
	Stdout   string
	Stderr   string
	ExitCode int
}

// Failure describes one command that did not meet its snippet's
// expectations.
type Failure struct {
	Snippet Snippet
	Result  Result
	Reason  string // "exit code" or "output"
	Detail  string
}

func (f Failure) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d: $ %s\n", f.Snippet.File, f.Result.Command.Line, f.Result.Command.Cmd)
	b.WriteString(f.Detail)
	if s := strings.TrimSpace(f.Result.Stderr); s != "" {
		fmt.Fprintf(&b, "stderr:\n%s\n", indentLines(s))
	}
	return b.String()
}

func indentLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

// Run executes every command of s in order (one fresh HOME/config dir
// per snippet, shared by its commands) and returns the results and any
// failures. Commands after a failure still run, so one report shows
// everything that is off in a block.
func (r *Runner) Run(s Snippet) ([]Result, []Failure) {
	home := r.TempDir()
	cfg := filepath.Join(home, ".config")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		return nil, []Failure{{Snippet: s, Reason: "setup", Detail: err.Error() + "\n"}}
	}
	env := make([]string, 0, len(r.Env)+3)
	for _, kv := range r.Env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "HOME" || k == "BRONTO_CONFIG_DIR" || k == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "HOME="+home, "BRONTO_CONFIG_DIR="+cfg, "XDG_CONFIG_HOME="+cfg)

	timeout := r.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	var results []Result
	var failures []Failure
	for _, c := range s.Commands {
		if r.Reset != nil {
			r.Reset()
		}
		res := r.exec(c, env, home, timeout, s.TTY)
		results = append(results, res)
		if res.ExitCode != s.ExitCode {
			failures = append(failures, Failure{Snippet: s, Result: res, Reason: "exit code",
				Detail: fmt.Sprintf("exit code %d, want %d\nstdout:\n%s\n", res.ExitCode, s.ExitCode, indentLines(strings.TrimRight(res.Stdout, "\n")))})
			continue
		}
		if len(c.Want) == 0 {
			continue
		}
		got := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
		if res.Stdout == "" {
			got = nil
		}
		if !Match(c.Want, got) {
			failures = append(failures, Failure{Snippet: s, Result: res, Reason: "output",
				Detail: "stdout mismatch (- want, + got):\n" + Diff(c.Want, got)})
		}
	}
	return results, failures
}

func (r *Runner) exec(c Command, env []string, dir string, timeout time.Duration, tty bool) Result {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", c.Cmd) // #nosec G204 -- running the repo's own docs snippets is the point
	cmd.Env = env
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stderr = &stderr
	var err error
	if tty {
		err = runOnPTY(cmd, &stdout, env)
	} else {
		cmd.Stdout = &stdout
		err = cmd.Run()
	}
	res := Result{Command: c, Stdout: stdout.String(), Stderr: stderr.String()}
	for _, rep := range r.Replace {
		if rep.Old != "" {
			res.Stdout = strings.ReplaceAll(res.Stdout, rep.Old, rep.New)
			res.Stderr = strings.ReplaceAll(res.Stderr, rep.Old, rep.New)
		}
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		res.ExitCode = -1
		res.Stderr += "\n" + err.Error()
	}
	if ctx.Err() != nil {
		res.ExitCode = -1
		res.Stderr += fmt.Sprintf("\ncommand timed out after %s", timeout)
	}
	return res
}

// Format renders results as a ready-to-paste ```console test block (used
// by DOCS_SNIPPETS_PRINT=1 to fill in expected outputs).
func Format(s Snippet, results []Result) string {
	var b strings.Builder
	meta := "console test"
	if s.TTY {
		meta += " tty"
	}
	if s.ExitCode != 0 {
		meta += fmt.Sprintf(" exit=%d", s.ExitCode)
	}
	fmt.Fprintf(&b, "--- %s\n```%s\n", s.Name(), meta)
	for _, r := range results {
		fmt.Fprintf(&b, "$ %s\n", r.Command.Cmd)
		for _, l := range normalize(strings.Split(strings.TrimRight(r.Stdout, "\n"), "\n")) {
			b.WriteString(l + "\n")
		}
		if r.ExitCode != s.ExitCode {
			fmt.Fprintf(&b, "# (exit %d)\n", r.ExitCode)
		}
	}
	b.WriteString("```\n")
	return b.String()
}
