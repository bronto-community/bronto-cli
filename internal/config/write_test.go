package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetUserValueRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := SetUserValue(dir, "default", "region", "us"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{
		Getenv:        func(string) string { return "" },
		WorkDir:       t.TempDir(),
		UserConfigDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := cfg.Get("region")
	if !ok || v.Val != "us" || v.Source != SourceUser {
		t.Fatalf("got %+v", v)
	}
	// file landed at the expected path with restrictive permissions
	fi, err := os.Stat(filepath.Join(dir, "bronto", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestSetUserValueRejectsSecret(t *testing.T) {
	if err := SetUserValue(t.TempDir(), "default", "api_key", "sekret"); err == nil {
		t.Fatal("want rejection of api_key")
	}
}

func TestSetUserValueDefaultsProfileWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := SetUserValue(dir, "", "region", "us"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{Getenv: func(string) string { return "" }, WorkDir: t.TempDir(), UserConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile() != "default" {
		t.Fatalf("Profile() = %q, want the implicit \"default\" profile", cfg.Profile())
	}
	v, _ := cfg.Get("region")
	if v.Val != "us" {
		t.Fatalf("region = %+v, want it stored under the default profile", v)
	}
}

func TestSetUserValueCorruptExistingFileIsParseError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bronto", "config.toml"), "not [valid toml =\n")
	if err := SetUserValue(dir, "prod", "region", "us"); err == nil {
		t.Fatal("want config_parse_error for a corrupt existing file")
	}
}

func TestSetUserValueMkdirAllErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	// Put a plain file where SetUserValue needs to create the "bronto"
	// directory: os.MkdirAll must fail because a non-directory occupies the
	// path.
	if err := os.WriteFile(filepath.Join(dir, "bronto"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetUserValue(dir, "prod", "region", "us"); err == nil {
		t.Fatal("want an error when the config directory can't be created")
	}
}

func TestSetUserValuePreservesProfilesAfterFileWithNoProfilesSection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bronto", "config.toml"), "default_profile = \"prod\"\n")
	if err := SetUserValue(dir, "prod", "region", "us"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{Getenv: func(string) string { return "" }, WorkDir: t.TempDir(), UserConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := cfg.Get("region"); v.Val != "us" {
		t.Fatalf("region = %+v", v)
	}
}

func TestSetDefaultProfileCorruptExistingFileIsParseError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bronto", "config.toml"), "not [valid toml =\n")
	if err := SetDefaultProfile(dir, "prod"); err == nil {
		t.Fatal("want config_parse_error for a corrupt existing file")
	}
}

func TestSetDefaultProfileMkdirAllErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bronto"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultProfile(dir, "prod"); err == nil {
		t.Fatal("want an error when the config directory can't be created")
	}
}
