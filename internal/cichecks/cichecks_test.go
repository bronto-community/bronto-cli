package cichecks

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return b
}

// requiredChecks reads .github/required-status-checks.txt: one context per
// line, blank lines and #-comments ignored.
func requiredChecks(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(string(readFile(t, filepath.Join(root, ".github", "required-status-checks.txt"))), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// TestRequiredChecksAreBackedByJobs is the tripwire proper: every context
// in .github/required-status-checks.txt (the checked-in mirror of the
// branch-protection ruleset's required checks) must be produced by a job
// in ci.yml. Rename or remove a required job without updating both, and
// this fails here instead of silently wedging every PR.
func TestRequiredChecksAreBackedByJobs(t *testing.T) {
	root := repoRoot(t)
	jobs, err := WorkflowJobs(readFile(t, filepath.Join(root, ".github", "workflows", "ci.yml")))
	if err != nil {
		t.Fatalf("parsing ci.yml: %v", err)
	}
	required := requiredChecks(t, root)
	if len(required) == 0 {
		t.Fatal("required-status-checks.txt is empty — the tripwire has nothing to protect")
	}
	if bad := UnbackedChecks(required, jobs); len(bad) > 0 {
		t.Fatalf("required status checks with no backing ci.yml job: %v\n"+
			"Either the job was renamed/removed (fix ci.yml or the ruleset) or "+
			"required-status-checks.txt drifted from the ruleset. Backing jobs present: %v",
			bad, sortedKeys(jobs))
	}
}

// TestUnbackedChecksDetectsPhantom is the checker's own self-test: a
// tripwire that cannot fail guards nothing. It must flag a required check
// whose job doesn't exist (the exact generate-clean/repo-gates wedge) and
// pass a matrix-suffixed check whose base job is present.
func TestUnbackedChecksDetectsPhantom(t *testing.T) {
	jobs := map[string]bool{"repo-gates": true, "test": true, "lint": true}
	bad := UnbackedChecks([]string{"test (ubuntu-latest)", "lint", "generate-clean"}, jobs)
	if len(bad) != 1 || bad[0] != "generate-clean" {
		t.Fatalf("want [generate-clean] flagged, got %v", bad)
	}
}

func TestBaseJobStripsMatrixSuffix(t *testing.T) {
	cases := map[string]string{
		"test (ubuntu-latest)": "test",
		"lint":                 "lint",
		"repo-gates":           "repo-gates",
		"build (go, 1.26)":     "build",
	}
	for in, want := range cases {
		if got := BaseJob(in); got != want {
			t.Errorf("BaseJob(%q) = %q, want %q", in, got, want)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
