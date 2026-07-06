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
