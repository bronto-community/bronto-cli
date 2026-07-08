package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
	"github.com/svrnm/bronto-cli/internal/secrets"
)

func TestMain(m *testing.M) {
	// Keep the package's tests hermetic: never touch the real OS keychain.
	// Individual tests that need a specific lookup stub it themselves
	// (with save/restore), composing cleanly with this default.
	secretLookup = func(string) (string, bool, error) { return "", false, secrets.ErrNotFound }
	os.Exit(m.Run())
}

func TestNewAppFallsBackToKeychain(t *testing.T) {
	t.Setenv("BRONTO_API_KEY", "")
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	old := secretLookup
	secretLookup = func(profile string) (string, bool, error) { return "kc-key", false, nil }
	t.Cleanup(func() { secretLookup = old })

	cmd := NewRootCmd()
	pingCmd, _, _ := cmd.Find([]string{"ping"})
	app, err := NewApp(pingCmd)
	if err != nil {
		t.Fatal(err)
	}
	if app.Config.APIKey() != "kc-key" {
		t.Fatalf("APIKey = %q", app.Config.APIKey())
	}
	v, _ := app.Config.Get("api_key")
	if string(v.Source) != "keychain" {
		t.Fatalf("source = %s", v.Source)
	}
}

// TestNewAppWarnsOnceOnCorruptCredentialsButStillWorks pins: a corrupt
// credentials file surfaces from secretLookup as a typed config_parse_error
// (not secrets.ErrNotFound). NewApp must not fail the whole command over
// this — it treats it as "no stored key" (so BRONTO_API_KEY / --api-key
// still work) but warns once on stderr so the problem isn't silent, and
// exposes the error via App.SecretLookupErr for callers like 'auth status'.
func TestNewAppWarnsOnceOnCorruptCredentialsButStillWorks(t *testing.T) {
	t.Setenv("BRONTO_API_KEY", "")
	old := secretLookup
	parseErr := clierr.New("config_parse_error", "cannot parse /x/credentials: bad toml")
	secretLookup = func(string) (string, bool, error) { return "", false, parseErr }
	t.Cleanup(func() { secretLookup = old })

	root := NewRootCmd()
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	root.SetArgs([]string{"ping"})
	err := root.Execute()
	// No stored key and none via env/flag -> ping fails on auth_missing_key,
	// not on the corrupt file itself: NewApp must have succeeded.
	if clierr.ExitCode(err) != 3 {
		t.Fatalf("want exit 3 (auth_missing_key; NewApp itself must not fail), got %v", err)
	}
	if !strings.Contains(errBuf.String(), "credentials") {
		t.Fatalf("stderr missing corrupt-credentials warning: %q", errBuf.String())
	}
}

func TestNewAppQuietSuppressesCorruptCredentialsWarning(t *testing.T) {
	t.Setenv("BRONTO_API_KEY", "")
	old := secretLookup
	parseErr := clierr.New("config_parse_error", "cannot parse /x/credentials: bad toml")
	secretLookup = func(string) (string, bool, error) { return "", false, parseErr }
	t.Cleanup(func() { secretLookup = old })

	root := NewRootCmd()
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	root.SetArgs([]string{"ping", "--quiet"})
	_ = root.Execute()
	if strings.Contains(errBuf.String(), "credentials") {
		t.Fatalf("--quiet must suppress the warning, got %q", errBuf.String())
	}
}

// TestNewAppEnvKeyUnaffectedByCorruptCredentials pins: when an API key is
// already resolved (env/flag), NewApp never even consults secretLookup, so
// a corrupt credentials file has zero effect on the command.
func TestNewAppEnvKeyUnaffectedByCorruptCredentials(t *testing.T) {
	t.Setenv("BRONTO_API_KEY", "env-key")
	old := secretLookup
	secretLookup = func(string) (string, bool, error) {
		return "", false, clierr.New("config_parse_error", "corrupt")
	}
	t.Cleanup(func() { secretLookup = old })

	cmd := NewRootCmd()
	pingCmd, _, _ := cmd.Find([]string{"ping"})
	app, err := NewApp(pingCmd)
	if err != nil {
		t.Fatal(err)
	}
	if app.Config.APIKey() != "env-key" {
		t.Fatalf("APIKey = %q, want env-key", app.Config.APIKey())
	}
	if app.SecretLookupErr != nil {
		t.Fatalf("SecretLookupErr should be nil when the env key short-circuits lookup, got %v", app.SecretLookupErr)
	}
}

func TestNewAppRejectsFieldsWithRawFormat(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"ping", "--fields", "foo", "-o", "raw", "--api-key", "k"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v", err)
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_invalid_flags" {
		t.Fatalf("want usage_invalid_flags, got %v", err)
	}
}
