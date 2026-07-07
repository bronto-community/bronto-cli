package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zalando/go-keyring"

	"github.com/svrnm/bronto-cli/internal/clierr"
	"github.com/svrnm/bronto-cli/internal/config"
	"github.com/svrnm/bronto-cli/internal/secrets"
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

func TestAuthLoginNonTTYWithoutKeyStdinIsUsageError(t *testing.T) {
	keyring.MockInit()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "login"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v", err)
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
	if got := maskSecret("short"); got != "short…" {
		t.Fatalf("maskSecret(short) = %q", got)
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
