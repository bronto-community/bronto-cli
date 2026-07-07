package cli

import (
	"os"
	"testing"

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
