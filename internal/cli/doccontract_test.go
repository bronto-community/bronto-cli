package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// verbExceptionsSentence builds the canonical resource-verb-exceptions
// sentence from resourceRegistry, the single source of truth. The docs'
// "uniform verbs" paragraph must contain this sentence verbatim (see
// TestDocsStateResourceVerbExceptions), so the claim can never again
// overpromise verbs a resource doesn't have: adding or removing a
// NoGet/NoUpdate resource changes the sentence here, and the test then
// prints the exact replacement to paste into the docs.
//
// Fully list-only catalogs (NoCreate+NoUpdate+NoDelete+NoGet) are excluded
// — the docs already carve those out as "list-only".
func verbExceptionsSentence() string {
	var noGet, noUpdate []string
	for _, d := range resourceRegistry {
		if d.NoCreate && d.NoUpdate && d.NoDelete && d.NoGet {
			continue
		}
		if d.NoGet {
			noGet = append(noGet, "`"+d.display()+"`")
		}
		if d.NoUpdate {
			noUpdate = append(noUpdate, "`"+d.display()+"`")
		}
	}
	s := "Exceptions: no `get` for " + strings.Join(noGet, ", ")
	if len(noUpdate) > 0 {
		s += "; no `update` for " + strings.Join(noUpdate, ", ")
	}
	return s + "."
}

// TestDocsStateResourceVerbExceptions welds the resource-pattern paragraph
// in the user-facing docs to the registry: skill.md and README.md must
// contain the generated exceptions sentence verbatim. The 2026-07-23 audit
// found the docs promising `get`/`update` on seven resources that don't
// have them — an error class the command-level doc-rot guard structurally
// cannot catch, because the claim hides behind a `<resource>` placeholder.
func TestDocsStateResourceVerbExceptions(t *testing.T) {
	want := verbExceptionsSentence()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	for _, docFile := range []string{"skill.md", "README.md"} {
		data, err := os.ReadFile(filepath.Join(repoRoot, docFile))
		if err != nil {
			t.Fatalf("reading %s: %v", docFile, err)
		}
		if !strings.Contains(string(data), want) {
			t.Errorf("%s does not state the resource verb exceptions — the uniform-verbs claim overpromises.\nPaste this sentence (generated from resourceRegistry) into the resource-pattern paragraph:\n%s", docFile, want)
		}
	}
}

// TestNonStreamingPipedDefaultIsJSONArray pins the actual machine-output
// contract for non-streaming commands: with no -o flag and a non-TTY
// stdout, resource lists emit ONE pretty-printed JSON array — not JSONL.
// Only streaming commands (search, tail, traces) default to JSONL when
// piped. The docs must describe it this way; this test keeps the behavior
// from drifting underneath the corrected wording.
func TestNonStreamingPipedDefaultIsJSONArray(t *testing.T) {
	out, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1","name":"cpu"},{"id":"m2","name":"mem"}]`))
	}, "", "monitors", "list") // deliberately no -o: buffers are non-TTY, so this exercises DetectFormat
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("piped default is not a single JSON array: %v (%q)", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	// A JSONL emitter would put one complete object per line; the array
	// form is multi-line (indented) with the opening bracket on its own.
	if !strings.HasPrefix(strings.TrimSpace(out), "[") || len(strings.Split(strings.TrimSpace(out), "\n")) < 3 {
		t.Fatalf("expected a pretty-printed JSON array, got %q", out)
	}
}
