package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// skillDocFiles lists the repo-root docs the doc-rot guard covers.
var skillDocFiles = []string{"skill.md", "README.md", "llms.txt"}

// brontoInvocation captures the first token after a "bronto " prefix in a
// code span, e.g. "bronto auth login" -> "auth", "bronto --help" -> "--help".
var brontoInvocation = regexp.MustCompile(`^bronto\s+(\S+)`)

// inlineCodeSpan matches a single-backtick inline code span's content.
var inlineCodeSpan = regexp.MustCompile("`([^`]+)`")

// ignoreMarker, when present anywhere on a line, exempts every bronto-
// prefixed code span on that line from the registered-command/flag check.
// Use it for illustrative examples (e.g. a plugin invocation) whose first
// token is deliberately not a real command.
const ignoreMarker = "skilldoc:ignore"

// TestSkillDocCommandsAreReal is the mechanical doc-rot guard: it scans
// each file in skillDocFiles for backtick code spans — both inline `...`
// and fenced ``` blocks (one span per line) — that start with "bronto ",
// and asserts the first token after "bronto" is either:
//
//   - a registered top-level command (from NewRootCmd().Commands()), or
//   - a registered long flag, "--x" form (from the root's full flag set,
//     which includes persistent flags plus cobra's auto-added
//     help/version).
//
// When the first token resolves to a command GROUP (has subcommands and
// is not itself runnable, e.g. "traces" or "auth"), checkDeeperTokens
// additionally walks subsequent bare-word tokens as long as they keep
// resolving to a registered subcommand, so "bronto traces frobnicate"
// fails even though "traces" alone is valid — it stops at the first
// leaf/runnable command, flag, placeholder, or quoted-string start,
// since anything past that point is an ordinary argument, not a
// subcommand.
//
// Two kinds of tokens are exempt everywhere: placeholders written as
// "<...>" (e.g. "bronto <resource> list"), and any span on a line
// carrying the "skilldoc:ignore" marker comment.
//
// The matcher is deliberately simple: a doc author who wants a "bronto
// foo" example excluded from the check either uses a "<...>" placeholder
// or adds "<!-- skilldoc:ignore: why -->" on the same line.
func TestSkillDocCommandsAreReal(t *testing.T) {
	root := NewRootCmd()
	root.InitDefaultHelpFlag()
	root.InitDefaultVersionFlag()

	commands := map[string]*cobra.Command{}
	for _, c := range root.Commands() {
		commands[c.Name()] = c
	}
	flags := map[string]bool{}
	root.Flags().VisitAll(func(f *pflag.Flag) { flags["--"+f.Name] = true })

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	for _, docFile := range skillDocFiles {
		path := filepath.Join(repoRoot, docFile)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", docFile, err)
		}
		for _, span := range brontoCodeSpans(string(data)) {
			if span.ignore {
				continue
			}
			m := brontoInvocation.FindStringSubmatch(span.text)
			if m == nil {
				continue
			}
			token := m[1]
			if strings.HasPrefix(token, "<") && strings.HasSuffix(token, ">") {
				continue
			}
			if strings.HasPrefix(token, "--") {
				if !flags[token] {
					t.Errorf("%s: %q is not a registered flag (from code span %q)", docFile, token, span.text)
				}
				continue
			}
			cmd, ok := commands[token]
			if !ok {
				t.Errorf("%s: %q is not a registered command (from code span %q)", docFile, token, span.text)
				continue
			}
			checkDeeperTokens(t, docFile, span, cmd)
		}
	}
}

// checkDeeperTokens resolves the code span's tokens past the first one,
// descending through the command tree via cobra's own Command.Find as
// long as the current command is a non-runnable group (HasSubCommands
// and not Runnable). It stops — treating everything from there on as
// ordinary positional args/flags rather than further subcommands — at
// the first token that is a flag ("-..."), a "<placeholder>", the start
// of a quoted string (double or single quote), or once the resolved command is
// runnable (a leaf, or a group with its own default action).
func checkDeeperTokens(t *testing.T, docFile string, span codeSpan, cmd *cobra.Command) {
	t.Helper()
	words := strings.Fields(span.text)
	if len(words) < 3 { // "bronto" + first token + at least one more
		return
	}
	cur := cmd
	for _, w := range words[2:] {
		if !cur.HasSubCommands() || cur.Runnable() {
			return
		}
		if strings.HasPrefix(w, "-") || strings.HasPrefix(w, "<") ||
			strings.HasPrefix(w, `"`) || strings.HasPrefix(w, "'") {
			return
		}
		next, _, _ := cur.Find([]string{w})
		if next == cur {
			t.Errorf("%s: %q is not a registered subcommand of %q (from code span %q)",
				docFile, w, cur.CommandPath(), span.text)
			return
		}
		cur = next
	}
}

type codeSpan struct {
	text   string
	ignore bool
}

// brontoCodeSpans extracts every "bronto "-prefixed code span from doc:
// each line of a fenced ``` block is treated as one span, and each
// single-backtick `...` span outside a fence is checked independently. A
// span's ignore bit is set when its source line contains ignoreMarker.
func brontoCodeSpans(doc string) []codeSpan {
	var spans []codeSpan
	inFence := false
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		ignore := strings.Contains(line, ignoreMarker)
		if inFence {
			if strings.HasPrefix(trimmed, "bronto ") {
				spans = append(spans, codeSpan{text: trimmed, ignore: ignore})
			}
			continue
		}
		for _, m := range inlineCodeSpan.FindAllStringSubmatch(line, -1) {
			if strings.HasPrefix(m[1], "bronto ") {
				spans = append(spans, codeSpan{text: m[1], ignore: ignore})
			}
		}
	}
	return spans
}

// TestSkillDocCoversAllCommands is the other half of doc-rot protection:
// TestSkillDocCommandsAreReal catches PHANTOM commands in the docs; this
// catches MISSING ones — every registered top-level command must at least
// be named in skill.md, so new commands can't ship invisible to agents.
// (The 2026-07-19 fresh-eyes review found eleven resources absent.)
func TestSkillDocCoversAllCommands(t *testing.T) {
	root := NewRootCmd()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "skill.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	for _, c := range root.Commands() {
		name := c.Name()
		switch name {
		case "help", "completion": // cobra builtins
			continue
		}
		if c.Hidden {
			continue // hidden easter eggs are deliberately undocumented
		}
		if !strings.Contains(doc, name) {
			t.Errorf("skill.md never mentions the %q command — agents won't know it exists", name)
		}
	}
}
