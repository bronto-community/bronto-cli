package cli

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/secrets"
)

// deviceServer stubs the RFC 8628 endpoints plus the /logs probe that
// region detection performs after a successful token grant.
func deviceServer(t *testing.T, tokenResponses []string) *httptest.Server {
	t.Helper()
	var tokenCalls atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/device/authorization":
			_, _ = w.Write([]byte(`{"device_code":"dc-1","user_code":"B7KD-Q2XN","verification_uri":"https://app.example/device","expires_in":60,"interval":1}`))
		case "/oauth/token":
			if r.Header.Get("X-BRONTO-API-KEY") != "" {
				t.Error("Bronto API key header must not reach the OAuth endpoint")
			}
			n := int(tokenCalls.Add(1)) - 1
			if n >= len(tokenResponses) {
				n = len(tokenResponses) - 1
			}
			resp := tokenResponses[n]
			if strings.Contains(resp, `"error"`) {
				w.WriteHeader(http.StatusBadRequest)
			}
			_, _ = w.Write([]byte(resp))
		case "/logs":
			_, _ = w.Write([]byte(`{"logs":[]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
}

func runDeviceLogin(t *testing.T, srv *httptest.Server) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var errb bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errb)
	root.SetArgs([]string{"auth", "login", "--device", "--base-url", srv.URL})
	err := root.Execute()
	return errb.String(), err
}

func TestDeviceLoginHappyPath(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := deviceServer(t, []string{
		`{"error":"authorization_pending"}`,
		`{"access_token":"device-key-123"}`,
	})
	defer srv.Close()
	stderr, err := runDeviceLogin(t, srv)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"B7KD-Q2XN", "https://app.example/device", "Waiting for approval", "Logged in"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr)
		}
	}
	key, _, err := secrets.Get("default")
	if err != nil || key != "device-key-123" {
		t.Fatalf("stored key = %q err=%v", key, err)
	}
}

func TestDeviceLoginUnsupportedEndpoint(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := runDeviceLogin(t, srv)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "auth_device_unsupported" || clierr.ExitCode(err) != 3 {
		t.Fatalf("want auth_device_unsupported exit 3, got %v (exit %d)", err, clierr.ExitCode(err))
	}
	if !strings.Contains(ce.Hint, "paste an API key") {
		t.Fatalf("hint must teach the fallback: %q", ce.Hint)
	}
}

func TestDeviceLoginDeniedAndExpired(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	for wantCode, resp := range map[string]string{
		"auth_device_denied":  `{"error":"access_denied"}`,
		"auth_device_expired": `{"error":"expired_token"}`,
	} {
		srv := deviceServer(t, []string{resp})
		_, err := runDeviceLogin(t, srv)
		srv.Close()
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != wantCode {
			t.Fatalf("want %s, got %v", wantCode, err)
		}
	}
}

func TestDeviceLoginMalformedAuthorization(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"nope":true}`))
	}))
	defer srv.Close()
	_, err := runDeviceLogin(t, srv)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "auth_device_error" {
		t.Fatalf("want auth_device_error, got %v", err)
	}
}

func TestDeviceExclusiveWithKeyStdin(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "login", "--device", "--key-stdin"})
	err := root.Execute()
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_invalid_flags" {
		t.Fatalf("want usage_invalid_flags, got %v", err)
	}
}
