package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func noEnv(string) string { return "" }

// Numeric settings may be written as TOML integers; they used to fail
// every command with "cannot decode TOML integer into string".
func TestLoadAcceptsIntegerSettings(t *testing.T) {
	dir := writeConfigFile(t, "default_profile = \"p\"\n[profiles.p]\ntimeout = 60\nmax_retries = 3\n")
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, ".bronto.toml"), []byte("max_retries = 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{Getenv: noEnv, WorkDir: work, UserConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := cfg.Get("timeout"); v.Val != "60" {
		t.Fatalf("timeout = %+v", v)
	}
	if v, _ := cfg.Get("max_retries"); v.Val != "5" || v.Source != SourceProject {
		t.Fatalf("max_retries = %+v", v)
	}
	if !cfg.ProfileConfigured() {
		t.Fatal("profile p has a section")
	}
	// config set on such a file keeps working and rewrites only that line.
	if err := SetUserValue(dir, "p", "timeout", "30"); err != nil {
		t.Fatal(err)
	}
	if got := readConfigFile(t, dir); got != "default_profile = \"p\"\n[profiles.p]\ntimeout = \"30\"\nmax_retries = 3\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestLoadRejectsNonScalarSetting(t *testing.T) {
	dir := writeConfigFile(t, "default_profile = \"p\"\n[profiles.p]\ntimeout = [1]\n")
	_, err := Load(LoadOptions{Getenv: noEnv, WorkDir: t.TempDir(), UserConfigDir: dir})
	if clierr.ExitCode(err) != 2 || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("want config_parse_error naming timeout, got %v", err)
	}
}

func TestLoadRejectsMalformedBaseURL(t *testing.T) {
	for _, bad := range []string{"notaurl", "ftp://x", "https://:443", "https://h/pfx?x=1", "https://h/pfx#f", "https://h?"} {
		_, err := Load(LoadOptions{Flags: map[string]string{"base_url": bad}, Getenv: noEnv,
			WorkDir: t.TempDir(), UserConfigDir: t.TempDir()})
		if clierr.ExitCode(err) != 2 || !strings.Contains(err.Error(), "base_url") {
			t.Fatalf("%q: want config_invalid_url (exit 2), got %v", bad, err)
		}
	}
	_, err := Load(LoadOptions{Flags: map[string]string{"base_url": "https://u:secretpw@h?x=1"}, Getenv: noEnv,
		WorkDir: t.TempDir(), UserConfigDir: t.TempDir()})
	if err == nil || strings.Contains(err.Error(), "secretpw") {
		t.Fatalf("error must not echo the password: %v", err)
	}
	for _, ok := range []string{"https://api.eu.bronto.io", "http://localhost:8080/", "https://proxy.example.com/bronto"} {
		if _, err := Load(LoadOptions{Flags: map[string]string{"base_url": ok}, Getenv: noEnv,
			WorkDir: t.TempDir(), UserConfigDir: t.TempDir()}); err != nil {
			t.Fatalf("%q rejected: %v", ok, err)
		}
	}
	// Lenient (bronto config) still loads, so the value can be repaired.
	if _, err := Load(LoadOptions{Flags: map[string]string{"base_url": "notaurl"}, Getenv: noEnv,
		WorkDir: t.TempDir(), UserConfigDir: t.TempDir(), Lenient: true}); err != nil {
		t.Fatal(err)
	}
}

func TestRedactURLString(t *testing.T) {
	if got := RedactURLString("https://user:pw@h/x"); got != "https://user:xxxxx@h/x" {
		t.Fatalf("got %q", got)
	}
	if got := RedactURLString("prod/checkout-service"); got != "prod/checkout-service" {
		t.Fatalf("got %q", got)
	}
}
