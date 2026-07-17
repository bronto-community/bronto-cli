package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
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

func TestValuesReturnsIndependentCopy(t *testing.T) {
	cfg, err := Load(LoadOptions{
		Getenv:  env(map[string]string{"BRONTO_API_KEY": "sekret", "BRONTO_REGION": "us"}),
		WorkDir: t.TempDir(), UserConfigDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	vals := cfg.Values()
	if vals["api_key"].Val != "sekret" || vals["region"].Val != "us" {
		t.Fatalf("Values() = %+v", vals)
	}
	// Mutating the returned map must not affect the Config's own state.
	vals["region"] = Value{Val: "mutated", Source: SourceFlag}
	if v, _ := cfg.Get("region"); v.Val != "us" {
		t.Fatalf("Values() leaked a mutable reference: cfg.region = %+v", v)
	}
}

func TestLoadProjectFileEmptyWorkDirUsesGetwd(t *testing.T) {
	// WorkDir=="" makes loadProjectFile fall back to os.Getwd(), which during
	// `go test` is the package's own source directory. No .bronto.toml lives
	// anywhere above it in this repo, so the walk-up must terminate cleanly
	// with no project file found (not an error).
	cfg, err := Load(LoadOptions{Getenv: env(nil), WorkDir: "", UserConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Get("default_dataset"); ok {
		t.Fatalf("unexpected default_dataset from an unrelated ancestor .bronto.toml: %+v", cfg.Values())
	}
}

func TestProjectFileMalformedTOMLIsParseError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".bronto.toml"), "not [valid toml =\n")

	_, err := Load(LoadOptions{Getenv: env(nil), WorkDir: dir, UserConfigDir: t.TempDir()})
	if err == nil {
		t.Fatal("want config_parse_error for malformed project file")
	}
	if clierr.ExitCode(err) != 2 {
		t.Fatalf("exit code = %d, want 2 (config_parse_error)", clierr.ExitCode(err))
	}
}

func TestHasProfile(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", "") // neutralize any ambient override
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bronto", "config.toml"),
		"default_profile = \"prod\"\n[profiles.prod]\nregion = \"eu\"\n")

	ok, err := HasProfile(dir, "prod")
	if err != nil || !ok {
		t.Fatalf("HasProfile(prod) = %v, %v; want true", ok, err)
	}
	ok, err = HasProfile(dir, "ghost")
	if err != nil || ok {
		t.Fatalf("HasProfile(ghost) = %v, %v; want false", ok, err)
	}
	// absent config file: false without error
	ok, err = HasProfile(t.TempDir(), "prod")
	if err != nil || ok {
		t.Fatalf("HasProfile(absent) = %v, %v; want false, nil", ok, err)
	}
}

func TestHasProfileHonorsBrontoConfigDir(t *testing.T) {
	override := t.TempDir()
	writeFile(t, filepath.Join(override, "bronto", "config.toml"),
		"[profiles.stage]\nregion = \"us\"\n")
	t.Setenv("BRONTO_CONFIG_DIR", override)

	ok, err := HasProfile("", "stage")
	if err != nil || !ok {
		t.Fatalf("HasProfile via BRONTO_CONFIG_DIR = %v, %v; want true", ok, err)
	}
}

func TestHasProfileParseErrorPropagates(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", "")
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bronto", "config.toml"), "not [valid toml =\n")

	ok, err := HasProfile(dir, "prod")
	if err == nil || ok {
		t.Fatalf("HasProfile(corrupt) = %v, %v; want error", ok, err)
	}
	if clierr.ExitCode(err) != 2 {
		t.Fatalf("exit code = %d, want 2 (config_parse_error)", clierr.ExitCode(err))
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
