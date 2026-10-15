package config

import (
	"os"
	"path/filepath"

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
			m, err := parseProjectFile(b)
			if err != nil {
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
