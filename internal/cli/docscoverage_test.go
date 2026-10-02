package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// docsCoverageAllowlist is the repo-relative exceptions file for
// TestDocsGuideCoverage: one command path per line (without the leading
// "bronto"), each followed by a mandatory "# reason".
const docsCoverageAllowlist = "docs/coverage-allowlist.txt"

// uniformResourceVerbs are the generic CRUD verbs every registry resource
// shares. They are taught once, in the managing-resources guide, through
// the "bronto <resource> <verb>" pattern, so per-resource mentions are not
// required (the generated reference documents each one).
var uniformResourceVerbs = map[string]bool{"list": true, "get": true, "create": true, "update": true, "delete": true}

func docsRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

func docsRootCmd() *cobra.Command {
	root := NewRootCmd()
	root.InitDefaultHelpFlag()
	root.InitDefaultVersionFlag()
	root.InitDefaultCompletionCmd()
	return root
}

// docCommandPath is a command's path without the leading "bronto" — the
// key format used by docs/coverage-allowlist.txt and
// docs/reference-links.yaml.
func docCommandPath(c *cobra.Command) string {
	return strings.TrimPrefix(strings.TrimPrefix(c.CommandPath(), c.Root().Name()), " ")
}

// availableCommands returns every documented (non-hidden, non-help)
// descendant of root, depth-first in cobra's sorted order.
func availableCommands(root *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, s := range c.Commands() {
			if s.IsAvailableCommand() {
				out = append(out, s)
				walk(s)
			}
		}
	}
	walk(root)
	return out
}

// guideRequired reports whether a command must be taught in a hand-written
// page: every top-level command, plus every subcommand that is not one of
// the uniform resource verbs.
func guideRequired(c *cobra.Command) bool {
	return c.Parent() == c.Root() || !uniformResourceVerbs[c.Name()]
}

// spanCommand resolves a "bronto ..." code span to the deepest command it
// names: subcommand descent stops at the first flag, placeholder, quoted
// string, or word that is not a subcommand. Returns nil when the first
// token is not a command (flags like "bronto --version", placeholders).
func spanCommand(root *cobra.Command, text string) *cobra.Command {
	words := strings.Fields(text)
	if len(words) < 2 || words[0] != "bronto" {
		return nil
	}
	var cur *cobra.Command
	for _, w := range words[1:] {
		if strings.HasPrefix(w, "-") || strings.HasPrefix(w, "<") || strings.HasPrefix(w, "'") || strings.HasPrefix(w, "\"") {
			break
		}
		parent := root
		if cur != nil {
			parent = cur
		}
		var next *cobra.Command
		for _, s := range parent.Commands() {
			if s.Name() == w || s.HasAlias(w) {
				next = s
				break
			}
		}
		if next == nil {
			break
		}
		cur = next
	}
	return cur
}

// guideHint suggests where a missing command would naturally be taught.
func guideHint(path string) string {
	top := strings.Fields(path)[0]
	switch top {
	case "search", "query", "fields", "context", "repl", "saved-searches", "log-views":
		return "a searching guide under guides/"
	case "tail":
		return "the live-tail guide under guides/"
	case "traces":
		return "the traces guide under guides/"
	case "ask":
		return "the ask (LLM-assisted queries) guide under guides/"
	case "send":
		return "the sending-data guide under guides/"
	case "exports":
		return "the exports guide under guides/"
	case "usage", "metrics":
		return "a guide under guides/ (usage and metrics)"
	case "auth", "login":
		return "getting-started/ (authentication)"
	case "config", "completion":
		return "configuration/ (config files, profiles, shell completion)"
	case "api", "plugins":
		return "extend/ (the bronto api escape hatch, plugins)"
	case "ping", "version":
		return "getting-started/ or troubleshooting.mdx"
	}
	return "guides/resources.mdx (managing resources)"
}

type allowEntry struct {
	path, reason string
	line         int
}

func readCoverageAllowlist(t *testing.T, repoRoot string) []allowEntry {
	t.Helper()
	f, err := os.Open(filepath.Join(repoRoot, filepath.FromSlash(docsCoverageAllowlist)))
	if err != nil {
		t.Fatalf("reading %s: %v", docsCoverageAllowlist, err)
	}
	defer func() { _ = f.Close() }()
	var entries []allowEntry
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		path, reason, _ := strings.Cut(line, "#")
		path = strings.Join(strings.Fields(strings.TrimPrefix(strings.TrimSpace(path), "bronto ")), " ")
		reason = strings.TrimSpace(reason)
		if reason == "" {
			t.Errorf("%s:%d: %q has no reason — write it as '<command path>  # why it needs no guide'", docsCoverageAllowlist, n, path)
		}
		entries = append(entries, allowEntry{path: path, reason: reason, line: n})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}

// docsMentionedCommands returns the set of command paths (and all their
// parent paths) named by a "bronto ..." code span in any hand-written
// docs-site page.
func docsMentionedCommands(t *testing.T, root *cobra.Command, repoRoot string) map[string]bool {
	t.Helper()
	files, err := handWrittenDocFiles(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	mentioned := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		for _, span := range brontoCodeSpans(string(data)) {
			for c := spanCommand(root, span.text); c != nil && c != root; c = c.Parent() {
				mentioned[docCommandPath(c)] = true
			}
		}
	}
	return mentioned
}

// TestDocsGuideCoverage is the "no command ships untaught" gate for the
// docs site: every top-level command, and every subcommand that is not a
// uniform resource verb (list/get/create/update/delete), must appear as a
// `bronto <path>` code span (inline, fenced, or "$ "-prompted) in at least
// one hand-written page under docs/src/content/docs/ (the generated
// reference does not count — it documents everything by construction).
//
// Deliberate exceptions live in docs/coverage-allowlist.txt with a reason;
// TestDocsCoverageAllowlist keeps that file from going stale.
func TestDocsGuideCoverage(t *testing.T) {
	root := docsRootCmd()
	repoRoot := docsRepoRoot(t)
	mentioned := docsMentionedCommands(t, root, repoRoot)
	allowed := map[string]bool{}
	for _, e := range readCoverageAllowlist(t, repoRoot) {
		allowed[e.path] = true
	}

	var missing []string
	for _, c := range availableCommands(root) {
		p := docCommandPath(c)
		if !guideRequired(c) || mentioned[p] || allowed[p] {
			continue
		}
		missing = append(missing, fmt.Sprintf("  bronto %-32s -> e.g. %s", p, guideHint(p)))
	}
	if len(missing) > 0 {
		t.Errorf("%d command(s) are not taught in any hand-written docs page (docs/src/content/docs/**, excluding the generated reference/commands/):\n%s\n\n"+
			"Fix: show each one as a `bronto <command> ...` code span in the guide where a user would look for it.\n"+
			"If a command genuinely needs no guide, add it to %s as '<command path>  # reason'.",
			len(missing), strings.Join(missing, "\n"), docsCoverageAllowlist)
	}
}

// TestDocsCoverageAllowlist fails on stale allowlist entries: paths that
// are not (or no longer) commands, commands the coverage rule never
// requires (uniform resource verbs), and commands a guide now covers —
// each of those entries should simply be deleted.
func TestDocsCoverageAllowlist(t *testing.T) {
	root := docsRootCmd()
	repoRoot := docsRepoRoot(t)
	mentioned := docsMentionedCommands(t, root, repoRoot)
	byPath := map[string]*cobra.Command{}
	for _, c := range availableCommands(root) {
		byPath[docCommandPath(c)] = c
	}
	seen := map[string]bool{}
	for _, e := range readCoverageAllowlist(t, repoRoot) {
		where := fmt.Sprintf("%s:%d: %q", docsCoverageAllowlist, e.line, e.path)
		c, ok := byPath[e.path]
		switch {
		case seen[e.path]:
			t.Errorf("%s is listed twice", where)
		case !ok:
			t.Errorf("%s is not a (visible) bronto command — remove the stale entry", where)
		case !guideRequired(c):
			t.Errorf("%s is a uniform resource verb, which never needs guide coverage — remove the entry", where)
		case mentioned[e.path]:
			t.Errorf("%s is now covered by a docs page — remove the stale entry", where)
		}
		seen[e.path] = true
	}
}

// TestDocsReferenceComplete asserts the committed generated reference
// (make docs-reference) has a page for every top-level command and a
// section heading for every subcommand, plus the overview and global-flags
// pages. `make docs-reference-check` catches any drift too; this is the
// cheap in-test version that runs with plain `go test`.
func TestDocsReferenceComplete(t *testing.T) {
	root := docsRootCmd()
	refDir := filepath.Join(docsRepoRoot(t), filepath.FromSlash(docsReferenceDir))
	for _, f := range []string{"index.md", "global-flags.md"} {
		if _, err := os.Stat(filepath.Join(refDir, f)); err != nil {
			t.Errorf("generated reference is missing %s — run 'make docs-reference'", f)
		}
	}
	pages := map[string]string{}
	var problems []string
	for _, c := range availableCommands(root) {
		top := c
		for top.Parent() != root {
			top = top.Parent()
		}
		page, ok := pages[top.Name()]
		if !ok {
			data, err := os.ReadFile(filepath.Join(refDir, top.Name()+".md"))
			if err != nil {
				problems = append(problems, fmt.Sprintf("  no page %s.md for %q", top.Name(), top.CommandPath()))
				pages[top.Name()] = ""
				continue
			}
			page = string(data)
			pages[top.Name()] = page
		}
		if page == "" {
			continue
		}
		if c == top {
			if !strings.Contains(page, fmt.Sprintf("title: %q", c.CommandPath())) {
				problems = append(problems, fmt.Sprintf("  %s.md does not have title %q", top.Name(), c.CommandPath()))
			}
			continue
		}
		if !strings.Contains(page, "\n## "+c.CommandPath()+"\n") {
			problems = append(problems, fmt.Sprintf("  %s.md has no section for %q", top.Name(), c.CommandPath()))
		}
	}
	// Pages for commands that no longer exist.
	entries, _ := os.ReadDir(refDir)
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		if name == "index" || name == "global-flags" || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if _, ok := pages[name]; !ok {
			problems = append(problems, fmt.Sprintf("  stale page %s for a command that is no longer documented", e.Name()))
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Errorf("generated command reference (%s) is incomplete:\n%s\nRun 'make docs-reference' and commit the result.", docsReferenceDir, strings.Join(problems, "\n"))
	}
}
