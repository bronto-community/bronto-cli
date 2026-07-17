package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// writeCredentialsFile writes raw bytes at BRONTO_CONFIG_DIR/bronto/credentials
// with the given permission, creating parent directories as needed.
func writeCredentialsFile(t *testing.T, dir string, b []byte, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, "bronto", "credentials")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, perm); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStoreGetDeleteRoundTrip(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir()) // isolate the fallback file path too
	fb, err := Store("prod", "sekret-key")
	if err != nil || fb {
		t.Fatalf("store: fb=%v err=%v", fb, err)
	}
	key, fb, err := Get("prod")
	if err != nil || fb || key != "sekret-key" {
		t.Fatalf("get: %q fb=%v err=%v", key, fb, err)
	}
	if err := Delete("prod"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Get("prod"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestEmptyProfileMapsToDefault(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	if _, err := Store("", "k1"); err != nil {
		t.Fatal(err)
	}
	key, _, err := Get("default")
	if err != nil || key != "k1" {
		t.Fatalf("got %q, %v", key, err)
	}
}

// TestDeleteSemantics locks in the Delete truth table: a genuine file I/O
// error (e.g. an unparseable credentials file) always surfaces, regardless
// of what the keyring side did; any keyring-side error (not-found,
// unsupported platform, or a fully unavailable backend) is never fatal on
// its own, since the file store is the source of truth for "is anything
// left" once the keyring can't be reasoned about.
func TestDeleteSemantics(t *testing.T) {
	t.Run("nothing stored anywhere is idempotent", func(t *testing.T) {
		keyring.MockInit()
		t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
		if err := Delete("ghost"); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})

	t.Run("keyring deletes, file was never used", func(t *testing.T) {
		keyring.MockInit()
		t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
		if fb, err := Store("prod", "k"); err != nil || fb {
			t.Fatalf("store: fb=%v err=%v", fb, err)
		}
		if err := Delete("prod"); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})

	t.Run("keyring backend fully unavailable, file deletes (first headless logout)", func(t *testing.T) {
		keyring.MockInitWithError(errors.New("no dbus"))
		t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
		if fb, err := Store("prod", "k"); err != nil || !fb {
			t.Fatalf("store: fb=%v err=%v", fb, err)
		}
		if err := Delete("prod"); err != nil {
			t.Fatalf("want nil (file deletion succeeded, keyring error is non-fatal), got %v", err)
		}
	})

	t.Run("keyring backend fully unavailable, file already gone (second headless logout)", func(t *testing.T) {
		keyring.MockInitWithError(errors.New("no dbus"))
		t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
		if err := Delete("prod"); err != nil {
			t.Fatalf("want nil (idempotent second logout), got %v", err)
		}
	})

	t.Run("keyring unsupported platform, file not-found", func(t *testing.T) {
		keyring.MockInitWithError(keyring.ErrUnsupportedPlatform)
		t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
		if err := Delete("prod"); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})

	t.Run("genuine file I/O error surfaces even though keyring succeeded", func(t *testing.T) {
		keyring.MockInit()
		dir := t.TempDir()
		t.Setenv("BRONTO_CONFIG_DIR", dir)
		writeCredentialsFile(t, dir, []byte("not [valid toml =\n"), 0o600)
		err := Delete("prod")
		if err == nil {
			t.Fatal("want a genuine file parse error to surface, got nil")
		}
		if errors.Is(err, ErrNotFound) {
			t.Fatalf("want a genuine parse error, not ErrNotFound: %v", err)
		}
	})
}

// TestGetSurfacesCorruptFileAsTypedError pins that Get, like Store/Delete,
// treats a corrupt fallback file as a genuine typed error — not ErrNotFound.
// Swallowing it as ErrNotFound would make a corrupt file indistinguishable
// from "no key configured" to callers.
func TestGetSurfacesCorruptFileAsTypedError(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keyring"))
	dir := t.TempDir()
	t.Setenv("BRONTO_CONFIG_DIR", dir)
	writeCredentialsFile(t, dir, []byte("not [valid toml =\n"), 0o600)

	_, _, err := Get("prod")
	if err == nil {
		t.Fatal("want error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("want a typed parse error, not ErrNotFound: %v", err)
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "config_parse_error" {
		t.Fatalf("want config_parse_error, got %v (%T)", err, err)
	}
}

// TestGetSurfacesCorruptFileWhenKeyringHasNoEntry covers the other branch of
// Get: the keychain works but has no entry for this profile, so Get still
// consults the fallback file — which is corrupt.
func TestGetSurfacesCorruptFileWhenKeyringHasNoEntry(t *testing.T) {
	keyring.MockInit() // keychain works, but has no entry -> keyring.ErrNotFound
	dir := t.TempDir()
	t.Setenv("BRONTO_CONFIG_DIR", dir)
	writeCredentialsFile(t, dir, []byte("not [valid toml =\n"), 0o600)

	_, _, err := Get("prod")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "config_parse_error" {
		t.Fatalf("want config_parse_error, got %v (%T)", err, err)
	}
}

func TestFileStoreRefusesToRewriteCorruptFile(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keyring"))
	dir := t.TempDir()
	t.Setenv("BRONTO_CONFIG_DIR", dir)
	writeCredentialsFile(t, dir, []byte("not [valid toml =\n"), 0o600)
	if _, err := Store("prod", "new-key"); err == nil {
		t.Fatal("want Store to refuse to rewrite a corrupt existing credentials file")
	}
}

func TestFileStoreRepairsLoosePermissions(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keyring"))
	dir := t.TempDir()
	t.Setenv("BRONTO_CONFIG_DIR", dir)
	path := writeCredentialsFile(t, dir, []byte("existing = \"k0\"\n"), 0o644)
	if _, err := Store("prod", "k1"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credentials file perm = %v, want 0600", perm)
	}
}

func TestFileFallbackWhenKeyringUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New("no dbus"))
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	fb, err := Store("prod", "file-key")
	if err != nil || !fb {
		t.Fatalf("store fallback: fb=%v err=%v", fb, err)
	}
	key, fb, err := Get("prod")
	if err != nil || !fb || key != "file-key" {
		t.Fatalf("get fallback: %q fb=%v err=%v", key, fb, err)
	}
	if err := Delete("prod"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Get("prod"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}
