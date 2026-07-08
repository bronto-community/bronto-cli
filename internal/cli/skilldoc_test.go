package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// skillDocFiles lists the repo-root docs the doc-rot guard covers. Task 5
// (README + CONTRIBUTING) is expected to append "README.md" here.
var skillDocFiles = []string{"skill.md"}

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
//   - a registered top-level command (from NewRootCmd().Commands(); only
//     the first token is checked, so "bronto auth login" checks "auth",
//     not "login"), or
//   - a registered long flag, "--x" form (from the root's full flag set,
//     which includes persistent flags plus cobra's auto-added
//     help/version).
//
// Two kinds of tokens are exempt: placeholders written as "<...>" (e.g.
// "bronto <resource> list"), and any span on a line carrying the
// "skilldoc:ignore" marker comment.
//
// The matcher is deliberately simple: a doc author who wants a "bronto
// foo" example excluded from the check either uses a "<...>" placeholder
// or adds "<!-- skilldoc:ignore: why -->" on the same line.
func TestSkillDocCommandsAreReal(t *testing.T) {
	root := NewRootCmd()
	root.InitDefaultHelpFlag()
	root.InitDefaultVersionFlag()

	commands := map[string]bool{}
	for _, c := range root.Commands() {
		commands[c.Name()] = true
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
			if !commands[token] {
				t.Errorf("%s: %q is not a registered command (from code span %q)", docFile, token, span.text)
			}
		}
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
