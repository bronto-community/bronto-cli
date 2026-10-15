package config

import (
	"os"
	"path/filepath"
	"testing"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bronto"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bronto", "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readConfigFile(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "bronto", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// `config set` edits only the line it changes: comments, key order and
// keys the CLI doesn't know survive.
func TestSetUserValuePreservesCommentsAndUnknownKeys(t *testing.T) {
	dir := writeConfigFile(t, `# my bronto settings
default_profile = "prod"
editor = "vim" # not a bronto key

[profiles.prod]
# production, careful
region = "eu"
custom_thing = "42"

[profiles."my dev"]
region = "us"

[plugins]
enabled = ["a", "b"]
`)
	for _, e := range [][3]string{
		{"prod", "region", "us"},
		{"prod", "timeout", "60"},
		{"my dev", "output", "json"},
		{"staging", "region", "eu"},
	} {
		if err := SetUserValue(dir, e[0], e[1], e[2]); err != nil {
			t.Fatal(err)
		}
	}
	want := `# my bronto settings
default_profile = "prod"
editor = "vim" # not a bronto key

[profiles.prod]
# production, careful
region = "us"
custom_thing = "42"
timeout = "60"

[profiles."my dev"]
region = "us"
output = "json"

[plugins]
enabled = ["a", "b"]

[profiles.staging]
region = "eu"
`
	if got := readConfigFile(t, dir); got != want {
		t.Fatalf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestSetDefaultProfileKeepsComments(t *testing.T) {
	dir := writeConfigFile(t, "# header\n[profiles.a]\nregion = \"eu\" # trailing\n")
	if err := SetDefaultProfile(dir, "a"); err != nil {
		t.Fatal(err)
	}
	want := "default_profile = \"a\"\n# header\n[profiles.a]\nregion = \"eu\" # trailing\n"
	if got := readConfigFile(t, dir); got != want {
		t.Fatalf("file =\n%s\nwant\n%s", got, want)
	}
}

// Files the line editor can't handle safely (here an inline table) are
// re-encoded: comments go, but every key, known or not, stays.
func TestSetUserValueFallbackKeepsUnknownKeys(t *testing.T) {
	dir := writeConfigFile(t, "# comment\neditor = \"vim\"\nprofiles = { prod = { region = \"eu\", custom = \"x\" } }\n")
	if err := SetUserValue(dir, "prod", "region", "us"); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := toml.Unmarshal([]byte(readConfigFile(t, dir)), &doc); err != nil {
		t.Fatal(err)
	}
	prod := doc["profiles"].(map[string]any)["prod"].(map[string]any)
	if doc["editor"] != "vim" || prod["custom"] != "x" || prod["region"] != "us" || doc["default_profile"] != "prod" {
		t.Fatalf("doc = %v", doc)
	}
}

func TestSetUserValueValidates(t *testing.T) {
	for _, kv := range [][2]string{
		{"timout", "30"}, {"timeout", "abc"}, {"timeout", "-5"}, {"max_retries", "-1"},
		{"max_retries", "two"}, {"region", "EU"}, {"region", "evil.com/"}, {"output", "yaml"},
		{"base_url", "api.eu.bronto.io"}, {"ingest_url", "ftp://x"},
	} {
		dir := t.TempDir()
		err := SetUserValue(dir, "default", kv[0], kv[1])
		if clierr.ExitCode(err) != 2 {
			t.Fatalf("%s=%q: want a usage/config error (exit 2), got %v", kv[0], kv[1], err)
		}
		if _, err := os.Stat(filepath.Join(dir, "bronto", "config.toml")); !os.IsNotExist(err) {
			t.Fatalf("%s=%q: file written despite rejection", kv[0], kv[1])
		}
	}
	for _, kv := range [][2]string{
		{"timeout", "30"}, {"max_retries", "0"}, {"region", "us"}, {"output", "jsonl"},
		{"base_url", "http://localhost:8080"}, {"default_dataset", "prod/api"}, {"org_id", ""},
	} {
		if err := SetUserValue(t.TempDir(), "default", kv[0], kv[1]); err != nil {
			t.Fatalf("%s=%q rejected: %v", kv[0], kv[1], err)
		}
	}
}
