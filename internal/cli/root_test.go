package cli

import (
	"bytes"
	"errors"
	"strings"
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

func TestUnknownCommandSuggests(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"serach"})
	err := root.Execute()
	var ce *clierr.Error
	if err == nil || !errors.As(err, &ce) || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage error, got %v", err)
	}
	if !strings.Contains(ce.Hint, `"search"`) {
		t.Fatalf("hint should suggest search: %q", ce.Hint)
	}
}

// TestGroupCommandsRejectUnknownSubcommands pins that every group, not just
// root, fails a stray or typo'd subcommand with usage_invalid_args (exit 2)
// instead of printing help and exiting 0.
func TestGroupCommandsRejectUnknownSubcommands(t *testing.T) {
	for _, argv := range [][]string{
		{"parsers", "get", "x"},
		{"webhooks", "get", "x"},
		{"datasets", "bogus"},
		{"monitors", "downtimes", "get", "x"},
		{"datasets", "parser", "bogus"},
		{"auth", "bogus"},
		{"config", "bogus"},
	} {
		root := NewRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(argv)
		err := root.Execute()
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != "usage_invalid_args" || clierr.ExitCode(err) != 2 {
			t.Errorf("%v: want usage_invalid_args exit 2, got %v", argv, err)
			continue
		}
		if out.Len() != 0 {
			t.Errorf("%v: printed help on stdout: %q", argv, out.String())
		}
	}
}

// TestBareGroupPrintsHelp pins the other half: a bare group still shows its
// help and exits 0, without a "[flags]" usage line for the group itself.
func TestBareGroupPrintsHelp(t *testing.T) {
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"monitors"})
	if err := root.Execute(); err != nil {
		t.Fatalf("bare group returned error: %v", err)
	}
	if !strings.Contains(out.String(), "bronto monitors [command]") {
		t.Fatalf("bare group output missing usage: %q", out.String())
	}
	if strings.Contains(out.String(), "bronto monitors [flags]") {
		t.Fatalf("help-only group shows a runnable usage line: %q", out.String())
	}
}

// TestFlagGroupErrorsAreUsageErrors pins that cobra's flag-group errors
// (mutually exclusive, required together, one required) exit 2 like
// required-flag errors do, instead of unknown_error exit 1.
func TestFlagGroupErrorsAreUsageErrors(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	argv := []string{"traces", "shape", "--any-span", "--entry"}
	root.SetArgs(argv)
	err := Execute(t.Context(), root, argv)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_conflicting_flags" || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage_conflicting_flags exit 2, got %v", err)
	}

	for msg, want := range map[string]string{
		`required flag(s) "x" not set`:                                                          "usage_missing_flag",
		"at least one of the flags in the group [a b] is required":                              "usage_missing_flag",
		"if any flags in the group [a b] are set they must all be set; missing [b]":             "usage_missing_flag",
		"if any flags in the group [a b] are set none of the others can be; [a b] were all set": "usage_conflicting_flags",
	} {
		err := WrapExecuteError(errors.New(msg))
		if !errors.As(err, &ce) || ce.Code != want {
			t.Errorf("%q: want %s, got %v", msg, want, err)
		}
	}
	if err := WrapExecuteError(errors.New("boom")); errors.As(err, &ce) {
		t.Errorf("unrelated error wrapped: %v", err)
	}
}
