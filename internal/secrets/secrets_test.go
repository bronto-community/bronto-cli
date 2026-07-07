package secrets

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

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
