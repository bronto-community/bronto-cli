package config

import (
	"fmt"
	"slices"
	"strconv"

	toml "github.com/pelletier/go-toml/v2"
)

// parseUserFile decodes the user config file. Profile values may be TOML
// strings or plain scalars (`timeout = 60`), see settingValues.
func parseUserFile(b []byte) (*userFile, error) {
	var raw struct {
		DefaultProfile string                    `toml:"default_profile"`
		Profiles       map[string]map[string]any `toml:"profiles"`
	}
	if err := toml.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	uf := &userFile{DefaultProfile: raw.DefaultProfile, Profiles: map[string]map[string]string{}}
	for name, p := range raw.Profiles {
		vals, err := settingValues(p)
		if err != nil {
			return nil, fmt.Errorf("[profiles.%s]: %w", name, err)
		}
		uf.Profiles[name] = vals
	}
	return uf, nil
}

// parseProjectFile decodes a .bronto.toml (flat, scalar values).
func parseProjectFile(b []byte) (map[string]string, error) {
	var raw map[string]any
	if err := toml.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	return settingValues(raw)
}

// settingValues turns decoded TOML values into the string form every
// setting is resolved in. Numbers and booleans are accepted as written
// (`timeout = 60` and `timeout = "60"` mean the same); the validators
// decide whether the value makes sense. A table, array or date is an
// error for a setting the CLI reads, and is kept as "" (so its presence
// is still visible, e.g. api_key) for keys it doesn't.
func settingValues(m map[string]any) (map[string]string, error) {
	out := make(map[string]string, len(m))
	for k, v := range m {
		switch v := v.(type) {
		case string:
			out[k] = v
		case int64:
			out[k] = strconv.FormatInt(v, 10)
		case float64:
			out[k] = strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			out[k] = strconv.FormatBool(v)
		default:
			if slices.Contains(userFileKeys, k) {
				return nil, fmt.Errorf("%s must be a string or number, got %T", k, v)
			}
			out[k] = ""
		}
	}
	return out, nil
}
