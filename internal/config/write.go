package config

import (
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

// SetUserValue writes key=value into the profile section of the user config.
// dir is the parent config dir (same semantics as LoadOptions.UserConfigDir).
func SetUserValue(dir, profile, key, value string) error {
	if key == "api_key" {
		return clierr.New("config_secret_rejected", "api_key cannot be stored in the config file").
			WithHint("Use the BRONTO_API_KEY environment variable or 'bronto auth login' (keychain).")
	}
	if profile == "" {
		profile = "default"
	}
	path := filepath.Join(dir, "bronto", "config.toml")
	uf := userFile{Profiles: map[string]map[string]string{}}
	if b, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(b, &uf); err != nil {
			return clierr.New("config_parse_error", "cannot parse "+path+": "+err.Error())
		}
		if uf.Profiles == nil {
			uf.Profiles = map[string]map[string]string{}
		}
	}
	if uf.DefaultProfile == "" {
		uf.DefaultProfile = profile
	}
	if uf.Profiles[profile] == nil {
		uf.Profiles[profile] = map[string]string{}
	}
	uf.Profiles[profile][key] = value

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := toml.Marshal(uf)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
