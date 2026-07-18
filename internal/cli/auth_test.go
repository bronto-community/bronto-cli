package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/config"
	"github.com/bronto-community/bronto-cli/internal/secrets"
)

func TestAuthLoginKeyStdinStoresAndDetectsRegion(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-BRONTO-API-KEY") != "the-key" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader("the-key\n"))
	root.SetArgs([]string{"auth", "login", "--key-stdin", "--profile", "prod",
		"--region", "eu", "--base-url", srv.URL})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	key, _, err := secrets.Get("prod")
	if err != nil || key != "the-key" {
		t.Fatalf("stored key = %q, %v", key, err)
	}
	if !strings.Contains(errBuf.String(), `profile "prod"`) {
		t.Fatalf("confirmation = %q", errBuf.String())
	}
}

func TestAuthLoginRejectsBadKey(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader("bad\n"))
	root.SetArgs([]string{"auth", "login", "--key-stdin", "--region", "eu", "--base-url", srv.URL})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 3 {
		t.Fatalf("want exit 3, got %v", err)
	}
}

func TestAuthLoginUnreachableBaseURLIsNetworkError(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader("some-key\n"))
	root.SetArgs([]string{"auth", "login", "--key-stdin", "--region", "eu", "--base-url", "http://127.0.0.1:1"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 1 {
		t.Fatalf("want exit 1 (network_error), got %v", err)
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "network_error" || !ce.Retryable {
		t.Fatalf("want retryable network_error, got %v", err)
	}
}

func TestAuthLoginInvalidRegionIsUsageError(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader("some-key\n"))
	root.SetArgs([]string{"auth", "login", "--key-stdin", "--region", "apac"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2 (usage_invalid_region), got %v", err)
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_invalid_region" {
		t.Fatalf("want usage_invalid_region, got %v", err)
	}
}

func TestAuthLoginNonTTYWithoutKeyStdinIsUsageError(t *testing.T) {
	keyring.MockInit()
	oldOut, oldIn := stdoutIsTTY, stdinIsTTY
	stdoutIsTTY = func() bool { return false }
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdoutIsTTY, stdinIsTTY = oldOut, oldIn })

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "login"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v", err)
	}
}

func TestAuthLoginPromptRequiresStdinTTYToo(t *testing.T) {
	keyring.MockInit()
	oldOut, oldIn := stdoutIsTTY, stdinIsTTY
	stdoutIsTTY = func() bool { return true }
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdoutIsTTY, stdinIsTTY = oldOut, oldIn })

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "login"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2 (usage_key_required) when stdin is not a TTY, got %v", err)
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_key_required" {
		t.Fatalf("want usage_key_required, got %v", err)
	}
}

func TestAuthSwitchAndLogout(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	t.Setenv("BRONTO_CONFIG_DIR", dir)
	if _, err := secrets.Store("stage", "k1"); err != nil {
		t.Fatal(err)
	}
	root := NewRootCmd()
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	root.SetArgs([]string{"auth", "switch", "stage"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(config.LoadOptions{Getenv: os.Getenv, WorkDir: t.TempDir(), UserConfigDir: dir})
	if cfg.Profile() != "stage" {
		t.Fatalf("profile = %q", cfg.Profile())
	}
	// unknown profile -> exit 4
	root2 := NewRootCmd()
	root2.SetOut(&bytes.Buffer{})
	root2.SetErr(&bytes.Buffer{})
	root2.SetArgs([]string{"auth", "switch", "ghost"})
	if err := root2.Execute(); clierr.ExitCode(err) != 4 {
		t.Fatalf("want 4, got %v", err)
	}
	// logout removes the key
	root3 := NewRootCmd()
	root3.SetOut(&bytes.Buffer{})
	root3.SetErr(&bytes.Buffer{})
	root3.SetArgs([]string{"auth", "logout", "--profile", "stage"})
	if err := root3.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := secrets.Get("stage"); err == nil {
		t.Fatal("key must be gone after logout")
	}
}

func TestAuthSwitchCorruptConfigIsParseError(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	t.Setenv("BRONTO_CONFIG_DIR", dir)
	if err := os.MkdirAll(filepath.Join(dir, "bronto"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bronto", "config.toml"),
		[]byte("not [valid toml =\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "switch", "ghost"})
	err := root.Execute()
	if clierr.ExitCode(err) != 2 {
		t.Fatalf("want exit 2 (config_parse_error), got %d: %v", clierr.ExitCode(err), err)
	}
}

// TestAuthStatusShowsCorruptCredentialsParseError pins: when the credential
// lookup fails with a genuine parse error (not "no key"), 'auth status'
// surfaces the problem in its status cell instead of reporting "no key" as
// if nothing were stored.
func TestAuthStatusShowsCorruptCredentialsParseError(t *testing.T) {
	t.Setenv("BRONTO_API_KEY", "")
	old := secretLookup
	parseErr := clierr.New("config_parse_error", "cannot parse /x/credentials: bad toml")
	secretLookup = func(string) (string, bool, error) { return "", false, parseErr }
	t.Cleanup(func() { secretLookup = old })

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "status", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("out = %q, err = %v", out.String(), err)
	}
	status, _ := rows[0]["status"].(string)
	if !strings.Contains(status, "cannot parse") {
		t.Fatalf("status cell = %q, want it to surface the parse error", status)
	}
}

func TestRegionCandidatesExplicitBaseURLAndRegion(t *testing.T) {
	cands := regionCandidates(&config.Config{}, "us", "https://custom.example.com")
	if len(cands) != 1 || cands[0].region != "us" || cands[0].baseURL != "https://custom.example.com" {
		t.Fatalf("cands = %+v", cands)
	}
}

func TestRegionCandidatesBaseURLFallsBackToConfigRegion(t *testing.T) {
	// The only env lookup that matters here is BRONTO_REGION; stub Getenv to
	// answer just that one and "" for everything else so precedence stays clean.
	cfg, err := config.Load(config.LoadOptions{
		Getenv: func(k string) string {
			if k == "BRONTO_REGION" {
				return "us"
			}
			return ""
		},
		WorkDir: t.TempDir(), UserConfigDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cands := regionCandidates(cfg, "", "https://custom.example.com")
	if len(cands) != 1 || cands[0].region != "us" || cands[0].baseURL != "https://custom.example.com" {
		t.Fatalf("cands = %+v", cands)
	}
}

func TestRegionCandidatesBaseURLWithNoRegionAnywhere(t *testing.T) {
	// A zero-value Config has no values map at all: Get("region") reports
	// not-ok, exercising the "no region resolvable" arm.
	cands := regionCandidates(&config.Config{}, "", "https://custom.example.com")
	if len(cands) != 1 || cands[0].region != "" || cands[0].baseURL != "https://custom.example.com" {
		t.Fatalf("cands = %+v", cands)
	}
}

func TestRegionCandidatesNoBaseURLDefaultsToBothRegions(t *testing.T) {
	cands := regionCandidates(&config.Config{}, "", "")
	if len(cands) != 2 {
		t.Fatalf("cands = %+v, want 2 default regions", cands)
	}
	if cands[0].region != "eu" || cands[0].baseURL != "https://api.eu.bronto.io" {
		t.Fatalf("cands[0] = %+v", cands[0])
	}
	if cands[1].region != "us" || cands[1].baseURL != "https://api.us.bronto.io" {
		t.Fatalf("cands[1] = %+v", cands[1])
	}
}

func TestRegionCandidatesNoBaseURLWithExplicitRegion(t *testing.T) {
	cands := regionCandidates(&config.Config{}, "us", "")
	if len(cands) != 1 || cands[0].region != "us" || cands[0].baseURL != "https://api.us.bronto.io" {
		t.Fatalf("cands = %+v", cands)
	}
}

func TestConfigDirHonorsBrontoConfigDirOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BRONTO_CONFIG_DIR", dir)
	got, err := configDir()
	if err != nil || got != dir {
		t.Fatalf("configDir() = %q, %v; want %q, nil", got, err, dir)
	}
}

func TestConfigDirFallsBackToUserConfigDir(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", "")
	got, err := configDir()
	want, wantErr := os.UserConfigDir()
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if got != want {
		t.Fatalf("configDir() = %q, want %q", got, want)
	}
}

func TestAcquireKeyStdinTrimsWhitespace(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("  the-key  \n"))
	key, err := acquireKey(cmd, true)
	if err != nil {
		t.Fatal(err)
	}
	if key != "the-key" {
		t.Fatalf("key = %q, want trimmed", key)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

func TestAcquireKeyStdinReadErrorPropagates(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(errReader{})
	_, err := acquireKey(cmd, true)
	if err == nil || err.Error() != "read boom" {
		t.Fatalf("want raw read error, got %v", err)
	}
}

func TestAcquireKeyStdinEmptyIsUsageError(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("   \n"))
	_, err := acquireKey(cmd, true)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_key_required" {
		t.Fatalf("want usage_key_required, got %v", err)
	}
}

func TestAcquireKeyTTYPromptSuccess(t *testing.T) {
	oldOut, oldIn, oldRead := stdoutIsTTY, stdinIsTTY, readPassword
	stdoutIsTTY = func() bool { return true }
	stdinIsTTY = func() bool { return true }
	readPassword = func(int) ([]byte, error) { return []byte("  prompted-key \n"), nil }
	t.Cleanup(func() { stdoutIsTTY, stdinIsTTY, readPassword = oldOut, oldIn, oldRead })

	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	key, err := acquireKey(cmd, false)
	if err != nil {
		t.Fatal(err)
	}
	if key != "prompted-key" {
		t.Fatalf("key = %q, want trimmed prompted key", key)
	}
}

func TestAcquireKeyTTYPromptReadPasswordErrorIsUsageError(t *testing.T) {
	oldOut, oldIn, oldRead := stdoutIsTTY, stdinIsTTY, readPassword
	stdoutIsTTY = func() bool { return true }
	stdinIsTTY = func() bool { return true }
	readPassword = func(int) ([]byte, error) { return nil, errors.New("tty gone") }
	t.Cleanup(func() { stdoutIsTTY, stdinIsTTY, readPassword = oldOut, oldIn, oldRead })

	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	_, err := acquireKey(cmd, false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_key_required" {
		t.Fatalf("want usage_key_required, got %v", err)
	}
}

func TestMaskSecretRuneSafe(t *testing.T) {
	got := maskSecret("ключ-secret-key")
	if !utf8.ValidString(got) {
		t.Fatalf("masked key is not valid UTF-8: %q", got)
	}
	if r := []rune(got); len(r) != 9 || string(r[:8]) != "ключ-sec" || r[8] != '…' {
		t.Fatalf("masked key = %q, want first 8 runes + ellipsis", got)
	}
	if got := maskSecret(""); got != "" {
		t.Fatalf("maskSecret(\"\") = %q, want empty", got)
	}
	if got := maskSecret("short"); got != "…" {
		t.Fatalf("maskSecret(short) = %q, want bare ellipsis for <12-rune secrets", got)
	}
}

func TestAuthTokenPrintsResolvedKey(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "token", "--api-key", "the-full-key"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "the-full-key\n" {
		t.Fatalf("auth token output = %q, want %q", got, "the-full-key\n")
	}
}

func TestAuthTokenNoKeyExitsThree(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "token"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 3 {
		t.Fatalf("want exit 3 (auth_missing_key), got %v", err)
	}
}

func TestAuthStatusJSON(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "status", "--api-key", "abcdefgh12345", "--base-url", srv.URL, "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("out = %q", out.String())
	}
	if rows[0]["status"] != "ok" || rows[0]["key"] != "abcdefgh…" || rows[0]["key_source"] != "flag" {
		t.Fatalf("row = %v", rows[0])
	}
}
