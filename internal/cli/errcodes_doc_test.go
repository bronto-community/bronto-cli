package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// errCodeRe matches an error code literal handed to clierr.New, or
// assigned to a code variable that later is (WrapExecuteError).
var errCodeRe = regexp.MustCompile(`(?:clierr\.New\(\s*|\bcode = )"([a-z0-9_]+)"`)

// TestExitCodesDocListsEveryErrorCode greps the non-test Go sources for
// error codes and fails when one is missing from the exit-codes reference
// page, so a new code can't ship undocumented. The generated
// usage_ambiguous_<kind> codes (ambiguousCode) are covered by the
// documented `usage_ambiguous_<kind>` family row.
func TestExitCodesDocListsEveryErrorCode(t *testing.T) {
	root := docsRepoRoot(t)
	doc, err := os.ReadFile(filepath.Join(root, "docs", "cli", "reference", "exit-codes.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, err := os.ReadFile(path) // #nosec G304 -- repo source files
			if err != nil {
				return err
			}
			for _, m := range errCodeRe.FindAllSubmatch(b, -1) {
				codes[string(m[1])] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(codes) < 20 {
		t.Fatalf("found only %d error codes; is the regexp still matching?", len(codes))
	}
	if !strings.Contains(string(doc), "`usage_ambiguous_<kind>`") {
		t.Error("exit-codes.mdx must document the `usage_ambiguous_<kind>` family")
	}
	var missing []string
	for c := range codes {
		if !strings.Contains(string(doc), "`"+c+"`") {
			missing = append(missing, c)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("error codes missing from docs/cli/reference/exit-codes.mdx: %s", strings.Join(missing, ", "))
	}
}
