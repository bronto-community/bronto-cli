package config

import (
	"os"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPrecedenceFlagBeatsEnvBeatsFiles(t *testing.T) {
	dir := t.TempDir()
	ucd := t.TempDir()
	writeFile(t, filepath.Join(ucd, "bronto", "config.toml"),
		"default_profile = \"p1\"\n[profiles.p1]\nregion = \"us\"\n")
	writeFile(t, filepath.Join(dir, ".bronto.toml"), "region = \"us\"\n")

	cfg, err := Load(LoadOptions{
		Flags:         map[string]string{"region": "eu"},
		Getenv:        env(map[string]string{"BRONTO_REGION": "us"}),
		WorkDir:       dir,
		UserConfigDir: ucd,
	})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := cfg.Get("region")
	if v.Val != "eu" || v.Source != SourceFlag {
		t.Fatalf("got %+v, want eu from flag", v)
	}
}

func TestEnvAPIKeyAndSourceTracking(t *testing.T) {
	cfg, err := Load(LoadOptions{
		Getenv:  env(map[string]string{"BRONTO_API_KEY": "sekret"}),
		WorkDir: t.TempDir(), UserConfigDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey() != "sekret" {
		t.Fatal("api key from env not resolved")
	}
	v, _ := cfg.Get("api_key")
	if v.Source != SourceEnv {
		t.Fatalf("source = %s, want env", v.Source)
	}
}

func TestBaseURLDerivedFromRegionDefaultEU(t *testing.T) {
	cfg, err := Load(LoadOptions{Getenv: env(nil), WorkDir: t.TempDir(), UserConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BaseURL(); got != "https://api.eu.bronto.io" {
		t.Fatalf("BaseURL() = %q", got)
	}
	cfg2, _ := Load(LoadOptions{
		Getenv:  env(map[string]string{"BRONTO_REGION": "us"}),
		WorkDir: t.TempDir(), UserConfigDir: t.TempDir(),
	})
	if got := cfg2.BaseURL(); got != "https://api.us.bronto.io" {
		t.Fatalf("BaseURL() = %q", got)
	}
}

func TestProjectFileWalksUpAndRefusesSecrets(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".bronto.toml"), "default_dataset = \"ds-1\"\n")

	cfg, err := Load(LoadOptions{Getenv: env(nil), WorkDir: sub, UserConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := cfg.Get("default_dataset")
	if v.Val != "ds-1" || v.Source != SourceProject {
		t.Fatalf("got %+v, want ds-1 from project", v)
	}

	writeFile(t, filepath.Join(root, ".bronto.toml"), "api_key = \"leaked\"\n")
	_, err = Load(LoadOptions{Getenv: env(nil), WorkDir: sub, UserConfigDir: t.TempDir()})
	if err == nil {
		t.Fatal("want error for secret in project file")
	}
}

func TestProfileSelectionFromUserConfig(t *testing.T) {
	ucd := t.TempDir()
	writeFile(t, filepath.Join(ucd, "bronto", "config.toml"),
		"default_profile = \"stage\"\n[profiles.stage]\nregion = \"us\"\noutput = \"json\"\n[profiles.prod]\nregion = \"eu\"\n")

	cfg, err := Load(LoadOptions{Getenv: env(nil), WorkDir: t.TempDir(), UserConfigDir: ucd})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile() != "stage" {
		t.Fatalf("Profile() = %q", cfg.Profile())
	}
	if cfg.BaseURL() != "https://api.us.bronto.io" {
		t.Fatalf("BaseURL() = %q", cfg.BaseURL())
	}
	// BRONTO_PROFILE env overrides default_profile
	cfg2, _ := Load(LoadOptions{
		Getenv:  env(map[string]string{"BRONTO_PROFILE": "prod"}),
		WorkDir: t.TempDir(), UserConfigDir: ucd,
	})
	if cfg2.BaseURL() != "https://api.eu.bronto.io" {
		t.Fatalf("profile override failed: %q", cfg2.BaseURL())
	}
}

func TestBrontoConfigDirOverridesUserConfigDir(t *testing.T) {
	override := t.TempDir()
	ignored := t.TempDir()
	writeFile(t, filepath.Join(override, "bronto", "config.toml"),
		"default_profile = \"p1\"\n[profiles.p1]\nregion = \"us\"\n")

	cfg, err := Load(LoadOptions{
		Getenv:  env(map[string]string{"BRONTO_CONFIG_DIR": override}),
		WorkDir: t.TempDir(), UserConfigDir: ignored,
	})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := cfg.Get("region")
	if v.Val != "us" || v.Source != SourceUser {
		t.Fatalf("got %+v, want us from user config via BRONTO_CONFIG_DIR", v)
	}
}

func TestInjectOnlyWhenAbsent(t *testing.T) {
	cfg, err := Load(LoadOptions{Getenv: env(map[string]string{"BRONTO_API_KEY": "envkey"}),
		WorkDir: t.TempDir(), UserConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Inject("api_key", "keychain-key", SourceKeychain)
	if cfg.APIKey() != "envkey" { // env wins; Inject must not override
		t.Fatalf("APIKey = %q", cfg.APIKey())
	}
	cfg2, _ := Load(LoadOptions{Getenv: env(nil), WorkDir: t.TempDir(), UserConfigDir: t.TempDir()})
	cfg2.Inject("api_key", "keychain-key", SourceKeychain)
	v, _ := cfg2.Get("api_key")
	if v.Val != "keychain-key" || v.Source != SourceKeychain {
		t.Fatalf("injected: %+v", v)
	}
}

func TestSetDefaultProfile(t *testing.T) {
	dir := t.TempDir()
	if err := SetUserValue(dir, "prod", "region", "us"); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultProfile(dir, "prod"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{Getenv: func(string) string { return "" },
		WorkDir: t.TempDir(), UserConfigDir: dir})
	if err != nil || cfg.Profile() != "prod" {
		t.Fatalf("profile = %q, %v", cfg.Profile(), err)
	}
}
