package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// TestAPICmdMissingPositionalArgIsUsageError pins Fix 1: cobra's
// Args-validation errors (e.g. "accepts 2 arg(s), received 1" from
// `bronto api GET`) must be usage errors that exit 2, not the generic
// exit code 1 a plain error produces.
func TestAPICmdMissingPositionalArgIsUsageError(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"api", "GET"})
	err := root.Execute()
	if err == nil {
		t.Fatal("want error for missing positional arg")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want *clierr.Error, got %T: %v", err, err)
	}
	if got := clierr.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode = %d, want 2", got)
	}
}

// TestRootUnknownCommandIsUsageError extends the same coverage to root's
// NoArgs validator ("unknown command ..." errors).
func TestRootUnknownCommandIsUsageError(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"no-such-command"})
	err := root.Execute()
	if err == nil {
		t.Fatal("want error for unknown command")
	}
	if got := clierr.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode = %d, want 2", got)
	}
}

// TestBareInvocationPrintsUsage pins Fix 5: `bronto` with no args shows
// help and exits 0 (no error).
func TestBareInvocationPrintsUsage(t *testing.T) {
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{})
	if err := root.Execute(); err != nil {
		t.Fatalf("bare invocation returned error: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Usage:")) {
		t.Fatalf("bare invocation output missing %q: %q", "Usage:", out.String())
	}
}
