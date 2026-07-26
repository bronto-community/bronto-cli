package config

import (
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// mutateUserFile loads the user config file, applies fn, and writes it back.
// dir is the parent config dir (same semantics as LoadOptions.UserConfigDir).
// A missing file starts from an empty userFile; Profiles is always non-nil
// when fn runs.
func mutateUserFile(dir string, fn func(*userFile)) error {
	path := filepath.Join(dir, "bronto", "config.toml")
	uf := userFile{Profiles: map[string]map[string]string{}}
	if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- rewriting the user's own config file in place
		if err := toml.Unmarshal(b, &uf); err != nil {
			return clierr.New("config_parse_error", "cannot parse "+path+": "+err.Error())
		}
		if uf.Profiles == nil {
			uf.Profiles = map[string]map[string]string{}
		}
	}
	fn(&uf)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := toml.Marshal(uf)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// SetUserValue writes key=value into the profile section of the user config.
// dir is the parent config dir (same semantics as LoadOptions.UserConfigDir).
func SetUserValue(dir, profile, key, value string) error {
	if key == "api_key" {
		return clierr.New("config_secret_rejected", "api_key cannot be stored in the config file").
			WithHint("Use the BRONTO_API_KEY environment variable or 'bronto auth login' (keychain).")
	}
	if key == "ask_api_key" {
		return clierr.New("config_secret_rejected", "ask_api_key cannot be stored in the config file").
			WithHint("Use the BRONTO_ASK_API_KEY environment variable.")
	}
	if profile == "" {
		profile = "default"
	}
	return mutateUserFile(dir, func(uf *userFile) {
		if uf.DefaultProfile == "" {
			uf.DefaultProfile = profile
		}
		if uf.Profiles[profile] == nil {
			uf.Profiles[profile] = map[string]string{}
		}
		uf.Profiles[profile][key] = value
	})
}

// SetDefaultProfile persists default_profile in the user config file.
func SetDefaultProfile(dir, name string) error {
	return mutateUserFile(dir, func(uf *userFile) { uf.DefaultProfile = name })
}
