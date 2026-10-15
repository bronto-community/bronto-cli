package cli

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/bronto-community/bronto-cli/internal/config"
)

// The selected profile being unknown must not block switching away from it.
func TestAuthSwitchAwayFromMissingProfile(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BRONTO_API_KEY", "")
	path := writeUserConfig(t, "default_profile = \"ghost\"\n[profiles.prod]\nregion = \"us\"\n")
	if _, _, err := runCLI(t, "auth", "switch", "prod"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.LoadOptions{Getenv: func(string) string { return "" },
		WorkDir: t.TempDir(), UserConfigDir: filepath.Dir(filepath.Dir(path))})
	if err != nil || cfg.Profile() != "prod" {
		t.Fatalf("profile = %q, %v", cfg.Profile(), err)
	}
	// The target is still validated.
	_, _, err = runCLI(t, "auth", "switch", "nope")
	_ = wantCLIErr(t, err, "profile_not_found", 4)
}

// A server that accepts TCP but never completes the TLS handshake must
// hit the app timeout, not the transport's 10s default.
func TestDownloadExportStalledTLSHandshakeTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Swallow the ClientHello and never answer, until the client
			// gives up and closes.
			go func(c net.Conn) {
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}(c)
		}
	}()
	dir := t.TempDir()
	start := time.Now()
	err = downloadExport(context.Background(), downloadApp(t, "https://api.eu.bronto.io", 300*time.Millisecond),
		"https://"+ln.Addr().String()+"/export", filepath.Join(dir, "out.bin"))
	_ = wantCLIErr(t, err, "timeout", 5)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %s; the handshake isn't bounded by the app timeout", d)
	}
	assertOnlyFile(t, dir)
}
