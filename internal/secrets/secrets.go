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
			return "", false, ErrNotFound
		}
		return key, true, nil
	}
	key, ferr := fileGet(account(profile))
	if ferr != nil {
		return "", true, ErrNotFound
	}
	return key, true, nil
}

func Delete(profile string) error {
	kerr := keyring.Delete(service, account(profile))
	ferr := fileDelete(account(profile))
	if kerr == nil || ferr == nil {
		return nil
	}
	if errors.Is(kerr, keyring.ErrNotFound) && errors.Is(ferr, ErrNotFound) {
		return nil // nothing stored anywhere: deleting is idempotent
	}
	return kerr
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

func readFileMap() (map[string]string, string, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, "", err
	}
	m := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		_ = toml.Unmarshal(b, &m)
	}
	return m, path, nil
}

func fileStore(account, key string) error {
	m, path, err := readFileMap()
	if err != nil {
		return err
	}
	m[account] = key
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := toml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
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
	return os.WriteFile(path, b, 0o600)
}
