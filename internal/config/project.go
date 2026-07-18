package config

import (
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// loadProjectFile walks up from dir looking for .bronto.toml (like .git).
// Returns (nil, "", nil) when none exists.
func loadProjectFile(dir string) (map[string]string, string, error) {
	if dir == "" {
		d, err := os.Getwd()
		if err != nil {
			return nil, "", nil //nolint:nilerr // unreadable cwd simply means no project config layer
		}
		dir = d
	}
	for {
		path := filepath.Join(dir, ".bronto.toml")
		if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- .bronto.toml discovered in the user's own working tree
			var m map[string]string
			if err := toml.Unmarshal(b, &m); err != nil {
				return nil, path, clierr.New("config_parse_error", "cannot parse "+path+": "+err.Error())
			}
			return m, path, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, "", nil
		}
		dir = parent
	}
}
