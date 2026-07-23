package config

import (
	"path/filepath"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// A project .bronto.toml is discovered by walking UP from the working
// directory (loadProjectFile), so cd-ing into an untrusted clone makes
// that file's values active. Such a file must not be able to redirect
// where the authenticated API key is sent. These tests pin every host-
// controlling key against a hostile project file (2026-07-23 audit,
// HIGH). base_url/ingest_url from the TRUSTED user config file must keep
// working — the fix is source-scoped, not a blanket ban.

func TestProjectFileCannotSetBaseURL(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".bronto.toml"), "base_url = \"http://attacker.example\"\n")

	cfg, err := Load(LoadOptions{Getenv: env(nil), WorkDir: dir, UserConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BaseURL(); got != "https://api.eu.bronto.io" {
		t.Fatalf("BaseURL() = %q — a project file redirected the API host", got)
	}
	if v, ok := cfg.Get("base_url"); ok {
		t.Fatalf("base_url was set from a project file: %+v", v)
	}
}

func TestProjectFileCannotSetIngestURL(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".bronto.toml"), "ingest_url = \"http://attacker.example\"\n")

	cfg, err := Load(LoadOptions{Getenv: env(nil), WorkDir: dir, UserConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := cfg.Get("ingest_url"); ok {
		t.Fatalf("ingest_url was set from a project file: %+v", v)
	}
}

// TestUserConfigCanStillSetBaseURL guards against over-correcting: the
// user's own config file (under their config dir, not discovered by
// walking up an untrusted tree) is a trusted source and must keep setting
// base_url/ingest_url.
func TestUserConfigCanStillSetBaseURL(t *testing.T) {
	ucd := t.TempDir()
	writeFile(t, filepath.Join(ucd, "bronto", "config.toml"),
		"default_profile = \"p\"\n[profiles.p]\nbase_url = \"https://api.staging.example\"\ningest_url = \"https://ingest.staging.example\"\n")

	cfg, err := Load(LoadOptions{Getenv: env(nil), WorkDir: t.TempDir(), UserConfigDir: ucd})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BaseURL(); got != "https://api.staging.example" {
		t.Fatalf("BaseURL() = %q, want the user-config value", got)
	}
	if v, _ := cfg.Get("ingest_url"); v.Val != "https://ingest.staging.example" || v.Source != SourceUser {
		t.Fatalf("ingest_url = %+v, want the user-config value", v)
	}
}

// TestRegionCannotSmuggleAHost pins the subtler vector: region is
// interpolated into "https://api.%s.bronto.io", so a value containing a
// slash or "@" moves the host off bronto.io entirely (region="evil.com/"
// parses to host api.evil.com). A malformed region must be rejected
// wherever it comes from, so a project file can't use it as a base_url
// bypass.
func TestRegionCannotSmuggleAHost(t *testing.T) {
	for _, bad := range []string{"evil.com/", "x@evil.com", "us/../..", "UP.CASE", "a b"} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".bronto.toml"), "region = \""+bad+"\"\n")
		_, err := Load(LoadOptions{Getenv: env(nil), WorkDir: dir, UserConfigDir: t.TempDir()})
		if err == nil {
			t.Errorf("region=%q accepted; must be rejected as it can smuggle a host", bad)
			continue
		}
		if clierr.ExitCode(err) != 2 {
			t.Errorf("region=%q: exit code = %d, want 2 (usage/config error)", bad, clierr.ExitCode(err))
		}
	}
}

// TestValidRegionsStillAccepted keeps the region validation from being
// over-strict: the real regions must load cleanly.
func TestValidRegionsStillAccepted(t *testing.T) {
	for _, ok := range []string{"eu", "us", "us-2", "ap1"} {
		cfg, err := Load(LoadOptions{
			Getenv: env(map[string]string{"BRONTO_REGION": ok}), WorkDir: t.TempDir(), UserConfigDir: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("region=%q rejected: %v", ok, err)
		}
		if v, _ := cfg.Get("region"); v.Val != ok {
			t.Fatalf("region=%q not resolved: %+v", ok, v)
		}
	}
}
