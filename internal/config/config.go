// Package config resolves CLI configuration with the precedence
// flags > env > project .bronto.toml > user config > defaults (spec §6),
// tracking the source of every value.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

type Source string

const (
	SourceFlag     Source = "flag"
	SourceEnv      Source = "env"
	SourceProject  Source = "project"
	SourceUser     Source = "user"
	SourceDefault  Source = "default"
	SourceKeychain Source = "keychain"
)

type Value struct {
	Val    string
	Source Source
}

type LoadOptions struct {
	Flags         map[string]string
	Getenv        func(string) string
	WorkDir       string
	UserConfigDir string // parent dir; config lives at <dir>/bronto/config.toml
}

type Config struct {
	values  map[string]Value
	profile string
}

var envKeys = map[string]string{
	"api_key":     "BRONTO_API_KEY",
	"base_url":    "BRONTO_BASE_URL",
	"profile":     "BRONTO_PROFILE",
	"region":      "BRONTO_REGION",
	"timeout":     "BRONTO_TIMEOUT",
	"max_retries": "BRONTO_MAX_RETRIES",
	"ingest_url":  "BRONTO_INGEST_URL",
}

// userFileKeys are settable from the user's own config file (under the
// user config dir). api_key is deliberately absent: secrets never come
// from files.
var userFileKeys = []string{"profile", "region", "base_url", "output", "default_dataset", "timeout", "max_retries", "ingest_url"}

// projectFileKeys are settable from a discovered .bronto.toml. It walks UP
// from the working directory (loadProjectFile), so a project file can
// belong to an untrusted repo you cd into — it must NOT be able to
// redirect where the authenticated API key is sent. base_url and
// ingest_url are therefore excluded here (they name the host directly);
// region is included but validated (validateRegion) so it cannot smuggle
// a host through the "https://api.%s.bronto.io" template. 2026-07-23
// audit, HIGH.
var projectFileKeys = []string{"profile", "region", "output", "default_dataset", "timeout", "max_retries"}

type userFile struct {
	DefaultProfile string                       `toml:"default_profile"`
	Profiles       map[string]map[string]string `toml:"profiles"`
}

func Load(opts LoadOptions) (*Config, error) {
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	c := &Config{values: map[string]Value{}}
	set := func(key, val string, src Source) {
		if val == "" {
			return
		}
		if _, exists := c.values[key]; !exists {
			c.values[key] = Value{Val: val, Source: src}
		}
	}

	// 1. flags
	for k, v := range opts.Flags {
		set(k, v, SourceFlag)
	}
	// 2. env
	for key, env := range envKeys {
		set(key, opts.Getenv(env), SourceEnv)
	}
	// 3. project file
	proj, projPath, err := loadProjectFile(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	if proj != nil {
		if _, has := proj["api_key"]; has {
			return nil, clierr.New("config_secret_in_project_file",
				fmt.Sprintf("refusing to read api_key from %s", projPath)).
				WithHint("Move the key to the BRONTO_API_KEY environment variable or run 'bronto auth login'.")
		}
		for _, k := range projectFileKeys {
			set(k, proj[k], SourceProject)
		}
	}
	// 4. user config (profile section)
	uf, err := loadUserFile(opts.UserConfigDir, opts.Getenv)
	if err != nil {
		return nil, err
	}
	if uf != nil {
		set("profile", uf.DefaultProfile, SourceUser)
		c.profile = c.values["profile"].Val
		if p, ok := uf.Profiles[c.profile]; ok {
			if _, has := p["api_key"]; has {
				return nil, clierr.New("config_secret_in_config_file",
					"refusing to read api_key from the user config file").
					WithHint("Use the BRONTO_API_KEY environment variable or 'bronto auth login' (keychain).")
			}
			for _, k := range userFileKeys {
				set(k, p[k], SourceUser)
			}
		}
	} else {
		c.profile = c.values["profile"].Val
	}
	// 5. defaults
	set("region", "eu", SourceDefault)

	// region is interpolated into "https://api.%s.bronto.io" (BaseURL) and
	// "https://ingestion.%s.bronto.io" (ingest.URL). A value containing a
	// slash, "@", or dot moves the host off bronto.io entirely (e.g.
	// region="evil.com/" -> host api.evil.com), so a project file could use
	// it as a base_url bypass. Constrain it to a plain slug wherever it
	// came from. base_url/ingest_url are exempt: they are only settable
	// from trusted sources (flags/env/user config) and are full URLs by
	// design. 2026-07-23 audit, HIGH.
	if v, ok := c.values["region"]; ok {
		if err := validateRegion(v.Val); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// regionPattern is a conservative slug: lowercase alphanumerics and
// dashes, starting alphanumeric. It admits every real Bronto region
// (eu, us, us-2, …) while excluding anything that could smuggle a host
// (/, @, ., :, whitespace, uppercase).
var regionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func validateRegion(r string) error {
	if !regionPattern.MatchString(r) {
		return clierr.New("config_invalid_region",
			fmt.Sprintf("invalid region %q", r)).
			WithHint("Region must be a slug like 'eu' or 'us' (lowercase letters, digits, dashes). Use base_url for a custom endpoint.")
	}
	return nil
}

func loadUserFile(dir string, getenv func(string) string) (*userFile, error) {
	if override := getenv("BRONTO_CONFIG_DIR"); override != "" {
		dir = override
	}
	if dir == "" {
		d, err := os.UserConfigDir()
		if err != nil {
			return nil, nil //nolint:nilerr // no resolvable config dir simply means no user config layer
		}
		dir = d
	}
	path := filepath.Join(dir, "bronto", "config.toml")
	b, err := os.ReadFile(path) // #nosec G304 -- fixed filename under the user's own config dir
	if err != nil {
		return nil, nil //nolint:nilerr // absent user config file is fine
	}
	var uf userFile
	if err := toml.Unmarshal(b, &uf); err != nil {
		return nil, clierr.New("config_parse_error", fmt.Sprintf("cannot parse %s: %v", path, err))
	}
	return &uf, nil
}

// HasProfile reports whether the user config file has a [profiles.<name>]
// section. dir semantics match LoadOptions.UserConfigDir ("" = default,
// BRONTO_CONFIG_DIR honored).
func HasProfile(dir, name string) (bool, error) {
	uf, err := loadUserFile(dir, os.Getenv)
	if err != nil {
		return false, err // config_parse_error propagates
	}
	if uf == nil {
		return false, nil
	}
	_, ok := uf.Profiles[name]
	return ok, nil
}

func (c *Config) Get(key string) (Value, bool) {
	v, ok := c.values[key]
	return v, ok
}

func (c *Config) Values() map[string]Value {
	out := make(map[string]Value, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return out
}

func (c *Config) APIKey() string  { return c.values["api_key"].Val }
func (c *Config) Profile() string { return c.values["profile"].Val }

func (c *Config) BaseURL() string {
	if v, ok := c.values["base_url"]; ok {
		// Tolerate a trailing slash (common when pasting staging URLs):
		// every caller appends "/path", so strip it here once.
		return strings.TrimRight(v.Val, "/")
	}
	return fmt.Sprintf("https://api.%s.bronto.io", c.values["region"].Val)
}

// Inject adds a resolved value from an out-of-band source (keychain)
// without disturbing precedence: no-op when the key is already set.
func (c *Config) Inject(key, val string, src Source) {
	if val == "" {
		return
	}
	if _, exists := c.values[key]; exists {
		return
	}
	c.values[key] = Value{Val: val, Source: src}
}
