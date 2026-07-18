// Package secrets stores API keys in the OS keychain (macOS Keychain,
// Linux Secret Service, Windows Credential Manager) with a 0600
// credentials-file fallback for headless environments (spec §6).
package secrets

import (
	"errors"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/zalando/go-keyring"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

const service = "bronto-cli"

var ErrNotFound = errors.New("no stored credential")

func account(profile string) string {
	if profile == "" {
		return "default"
	}
	return profile
}

func Store(profile, key string) (bool, error) {
	if err := keyring.Set(service, account(profile), key); err != nil {
		return true, fileStore(account(profile), key)
	}
	return false, nil
}

// Get resolves the stored credential for profile: the OS keychain first,
// falling back to the credentials file when the keychain has no entry (or
// is unavailable). A corrupt fallback file is a genuine, typed error (a
// config_parse_error from fileGet) and is surfaced as such — never
// flattened to ErrNotFound, which would make it indistinguishable from "no
// key configured" to callers such as NewApp and 'auth status'.
func Get(profile string) (string, bool, error) {
	key, err := keyring.Get(service, account(profile))
	if err == nil {
		return key, false, nil
	}
	if errors.Is(err, keyring.ErrNotFound) {
		// keychain works but has no entry; still consult the fallback file
		// (a credential stored under fallback earlier must stay readable).
		key, ferr := fileGet(account(profile))
		if ferr != nil {
			if errors.Is(ferr, ErrNotFound) {
				return "", false, ErrNotFound
			}
			return "", false, ferr
		}
		return key, true, nil
	}
	key, ferr := fileGet(account(profile))
	if ferr != nil {
		if errors.Is(ferr, ErrNotFound) {
			return "", true, ErrNotFound
		}
		return "", true, ferr
	}
	return key, true, nil
}

// Delete removes the stored credential for profile from both the keyring
// and the fallback file, and is idempotent (deleting twice, or deleting a
// profile that was never stored, is not an error).
//
// The file store is treated as the source of truth: a genuine file I/O
// error (a corrupt or unreadable credentials file) always surfaces,
// regardless of what the keyring did. The keyring side, on the other hand,
// is never fatal on its own — "not found", "unsupported platform", and a
// wholesale-unavailable backend (e.g. no D-Bus / Secret Service on a
// headless box) are all indistinguishable from Delete's point of view, and
// none of them should block logout when the file confirms there is
// nothing left to remove.
func Delete(profile string) error {
	_ = keyring.Delete(service, account(profile))
	ferr := fileDelete(account(profile))
	if ferr != nil && !errors.Is(ferr, ErrNotFound) {
		return ferr // genuine file I/O error: never mask this
	}
	return nil
}

func credentialsPath() (string, error) {
	dir := os.Getenv("BRONTO_CONFIG_DIR")
	if dir == "" {
		d, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = d
	}
	return filepath.Join(dir, "bronto", "credentials"), nil
}

// readFileMap loads the fallback credentials file. A missing file is not an
// error (an empty map is returned); an existing file that cannot be read or
// parsed is a genuine, typed error — callers must not silently treat it as
// empty, since doing so and then rewriting it would drop every other
// profile's credentials.
func readFileMap() (map[string]string, string, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, "", err
	}
	m := map[string]string{}
	b, err := os.ReadFile(path) // #nosec G304 -- path is derived from the resolved config dir, not remote input
	if err != nil {
		if os.IsNotExist(err) {
			return m, path, nil
		}
		return nil, "", err
	}
	if err := toml.Unmarshal(b, &m); err != nil {
		return nil, "", clierr.New("config_parse_error", "cannot parse "+path+": "+err.Error())
	}
	return m, path, nil
}

func fileStore(account, key string) error {
	m, path, err := readFileMap()
	if err != nil {
		return err
	}
	m[account] = key
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := toml.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	// os.WriteFile only applies the given mode when creating a new file; an
	// existing file keeps its prior (possibly looser) permissions. Repair
	// that here so the credentials file is never left world/group readable.
	_ = os.Chmod(path, 0o600)
	return nil
}

func fileGet(account string) (string, error) {
	m, _, err := readFileMap()
	if err != nil {
		return "", err
	}
	key, ok := m[account]
	if !ok || key == "" {
		return "", ErrNotFound
	}
	return key, nil
}

func fileDelete(account string) error {
	m, path, err := readFileMap()
	if err != nil {
		return err
	}
	if _, ok := m[account]; !ok {
		return ErrNotFound
	}
	delete(m, account)
	b, err := toml.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	return nil
}
