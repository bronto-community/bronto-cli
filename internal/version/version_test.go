package version

import (
	"strings"
	"testing"
)

func TestStringContainsVersionAndCommit(t *testing.T) {
	got := String()
	if !strings.Contains(got, Version) || !strings.Contains(got, Commit) {
		t.Fatalf("String() = %q, want it to contain %q and %q", got, Version, Commit)
	}
	if !strings.HasPrefix(got, "bronto ") {
		t.Fatalf("String() = %q, want prefix \"bronto \"", got)
	}
}
