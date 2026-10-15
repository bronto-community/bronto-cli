package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/bronto-community/bronto-cli/internal/secrets"
)

// runQuietCase runs args with and without --quiet and checks the notice
// appears only without it.
func runQuietCase(t *testing.T, args []string, want string, stdin string) {
	t.Helper()
	for _, quiet := range []bool{false, true} {
		full := append([]string{}, args...)
		if quiet {
			full = append(full, "--quiet")
		}
		root := NewRootCmd()
		var errBuf bytes.Buffer
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&errBuf)
		root.SetIn(strings.NewReader(stdin))
		root.SetArgs(full)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v", full, err)
		}
		got := errBuf.String()
		if quiet && got != "" {
			t.Errorf("%v: --quiet printed %q", full, got)
		}
		if !quiet && !strings.Contains(got, want) {
			t.Errorf("%v: stderr = %q, want %q", full, got, want)
		}
	}
}

// TestConfirmationNoticesHonorQuiet: the auth and config confirmations
// are non-data messages, so --quiet suppresses them like every other.
func TestConfirmationNoticesHonorQuiet(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	runQuietCase(t, []string{"config", "set", "default_dataset", "ds-42"}, "Set default_dataset", "")
	runQuietCase(t, []string{"auth", "login", "--key-stdin", "--profile", "stage",
		"--region", "eu", "--base-url", srv.URL}, "Logged in", "the-key\n")
	runQuietCase(t, []string{"auth", "switch", "stage"}, `Switched default profile to "stage"`, "")
	// logout needs a stored key for each run.
	for _, quiet := range []bool{false, true} {
		if _, err := secrets.Store("gone", "k"); err != nil {
			t.Fatal(err)
		}
		args := []string{"auth", "logout", "--profile", "gone"}
		if quiet {
			args = append(args, "--quiet")
		}
		root := NewRootCmd()
		var errBuf bytes.Buffer
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&errBuf)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if got := errBuf.String(); quiet != (got == "") || (!quiet && !strings.Contains(got, "Logged out")) {
			t.Errorf("%v: stderr = %q", args, got)
		}
	}
}

// TestUserActionNotices: the users verb-actions confirm in the past tense
// and stay silent under --quiet.
func TestUserActionNotices(t *testing.T) {
	const id = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1"
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	for action, want := range map[string]string{
		"deactivate":    "Deactivated user " + id + ".",
		"reactivate":    "Reactivated user " + id + ".",
		"resend-invite": "Resent the invitation to user " + id + ".",
	} {
		_, stderr, err := runResource(t, ok, "", "users", action, id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(stderr) != want {
			t.Errorf("%s stderr = %q, want %q", action, stderr, want)
		}
		_, stderr, err = runResource(t, ok, "", "users", action, id, "--quiet")
		if err != nil {
			t.Fatal(err)
		}
		if stderr != "" {
			t.Errorf("%s --quiet stderr = %q", action, stderr)
		}
	}
}
