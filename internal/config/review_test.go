package config

import (
	"strings"
	"testing"
)

// url.Parse rejects these, so the userinfo password must be masked by hand.
func TestRedactURLStringMalformed(t *testing.T) {
	for _, in := range []string{
		"https://user:secretpw@host:bad",
		"https://user:secretpw@host/%zz",
		"https://user:secretpw@host:bad/p?q=1#f",
	} {
		got := RedactURLString(in)
		if strings.Contains(got, "secretpw") || !strings.Contains(got, "user:xxxxx@host") {
			t.Fatalf("%q -> %q", in, got)
		}
	}
	for _, keep := range []string{"prod/checkout-service", "100%zz", "https://host:bad"} {
		if got := RedactURLString(keep); got != keep {
			t.Fatalf("%q changed to %q", keep, got)
		}
	}
}

func TestInvalidURLErrorDoesNotLeakPassword(t *testing.T) {
	for _, bad := range []string{"https://user:secretpw@host:bad", "https://user:secretpw@host/%zz"} {
		err := ValidateUserValue("base_url", bad)
		if err == nil || strings.Contains(err.Error(), "secretpw") {
			t.Fatalf("%q: %v", bad, err)
		}
		_, err = Load(LoadOptions{Flags: map[string]string{"base_url": bad}, Getenv: noEnv,
			WorkDir: t.TempDir(), UserConfigDir: t.TempDir()})
		if err == nil || strings.Contains(err.Error(), "secretpw") {
			t.Fatalf("%q: Load: %v", bad, err)
		}
	}
}

func TestSetUserValueKeepsInlineComment(t *testing.T) {
	dir := writeConfigFile(t, "default_profile = \"p\"\n[profiles.p]\n"+
		"timeout = \"30\" # CI limit\n"+
		"default_dataset = \"a#b\"\t# hash inside the value\n")
	if err := SetUserValue(dir, "p", "timeout", "60"); err != nil {
		t.Fatal(err)
	}
	if err := SetUserValue(dir, "p", "default_dataset", "c#d"); err != nil {
		t.Fatal(err)
	}
	want := "default_profile = \"p\"\n[profiles.p]\n" +
		"timeout = \"60\" # CI limit\n" +
		"default_dataset = \"c#d\"\t# hash inside the value\n"
	if got := readConfigFile(t, dir); got != want {
		t.Fatalf("file =\n%s\nwant\n%s", got, want)
	}
}

// An empty value clears an override; there is no unset subcommand.
func TestSetUserValueEmptyClears(t *testing.T) {
	dir := writeConfigFile(t, "default_profile = \"p\"\n[profiles.p]\nbase_url = \"https://staging.example.com\"\n")
	for _, k := range []string{"base_url", "timeout", "region", "output"} {
		if err := SetUserValue(dir, "p", k, ""); err != nil {
			t.Fatalf("clearing %s: %v", k, err)
		}
	}
	cfg, err := Load(LoadOptions{Getenv: noEnv, WorkDir: t.TempDir(), UserConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BaseURL(); got != "https://api.eu.bronto.io" {
		t.Fatalf("BaseURL = %q, want the region default", got)
	}
	if err := SetUserValue(dir, "p", "timout", ""); err == nil {
		t.Fatal("unknown key accepted with an empty value")
	}
	if err := SetUserValue(dir, "p", "api_key", ""); err == nil {
		t.Fatal("api_key accepted with an empty value")
	}
}
