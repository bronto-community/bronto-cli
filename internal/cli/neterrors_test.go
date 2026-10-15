package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/config"
)

// runCLI runs the root command with args and returns stdout, stderr and
// the error. The user config dir is a fresh temp dir unless the test set
// BRONTO_CONFIG_DIR itself.
func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	if os.Getenv("BRONTO_CONFIG_DIR") == "" {
		t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	}
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errBuf.String(), err
}

func wantCLIErr(t *testing.T, err error, code string, exit int) *clierr.Error {
	t.Helper()
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != code || clierr.ExitCode(err) != exit {
		t.Fatalf("want %s (exit %d), got %#v (exit %d)", code, exit, err, clierr.ExitCode(err))
	}
	return ce
}

// A server that never answers must fail the same way whichever command
// asked: before api.Do, `api` and `ping` reported network_error (exit 1)
// where `datasets list` reported timeout (exit 5).
func TestHangingServerIsTimeoutForEveryCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	for _, args := range [][]string{
		{"api", "GET", "/logs"},
		{"ping"},
		{"datasets", "list"},
	} {
		full := append(slices.Clone(args), "--base-url", srv.URL, "--api-key", "k", "--timeout", "1", "-o", "json")
		_, _, err := runCLI(t, full...)
		_ = wantCLIErr(t, err, "timeout", 5)
	}
}

func TestBodyReadFailureIsNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte(`{"logs":[`))
		w.(http.Flusher).Flush()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	}))
	defer srv.Close()
	for _, args := range [][]string{{"api", "GET", "/logs"}, {"datasets", "list"}} {
		full := append(slices.Clone(args), "--base-url", srv.URL, "--api-key", "k", "-o", "json")
		_, _, err := runCLI(t, full...)
		_ = wantCLIErr(t, err, "network_error", 1)
	}
}

func TestPingNetworkErrorNamesBaseURL(t *testing.T) {
	_, _, err := runCLI(t, "ping", "--base-url", "http://127.0.0.1:1", "--api-key", "k")
	ce := wantCLIErr(t, err, "network_error", 1)
	if !strings.Contains(ce.Message, "cannot reach http://127.0.0.1:1") {
		t.Fatalf("message = %q", ce.Message)
	}
}

// auth status maps only 401/403 to auth_status_not_ok (exit 3, "run auth
// login"); outages keep their own typed error.
func TestAuthStatusOnlyAuthFailuresAreAuthErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		exit   int
	}{
		{500, "api_server_error", 1},
		{429, "rate_limited", 5},
		{401, "auth_status_not_ok", 3},
		{403, "auth_status_not_ok", 3},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}))
		out, _, err := runCLI(t, "auth", "status", "--base-url", srv.URL, "--api-key", "kkkkkkkkkkkkkkkk",
			"--max-retries", "0", "-o", "json")
		srv.Close()
		_ = wantCLIErr(t, err, tc.code, tc.exit)
		if !strings.Contains(out, `"status"`) {
			t.Fatalf("%d: status row not printed: %q", tc.status, out)
		}
	}
	_, _, err := runCLI(t, "auth", "status", "--base-url", "http://127.0.0.1:1", "--api-key", "kkkkkkkkkkkkkkkk")
	_ = wantCLIErr(t, err, "network_error", 1)
}

func TestPickRegionDistinguishesOutageFromRejection(t *testing.T) {
	serve := func(status int) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		t.Cleanup(s.Close)
		return s
	}
	down, rejects, accepts := serve(503), serve(401), serve(200)
	ctx := context.Background()

	// eu is down, us rejects: the key may be an eu key, so this is the
	// outage, not auth_invalid_key.
	_, _, err := pickRegion(ctx, []regionCandidate{{"eu", down.URL}, {"us", rejects.URL}}, "k")
	ce := wantCLIErr(t, err, "api_server_error", 1)
	if !strings.Contains(ce.Message, "eu") || !ce.Retryable {
		t.Fatalf("got %+v", ce)
	}

	_, _, err = pickRegion(ctx, []regionCandidate{{"eu", rejects.URL}, {"us", rejects.URL}}, "k")
	_ = wantCLIErr(t, err, "auth_invalid_key", 3)

	region, base, err := pickRegion(ctx, []regionCandidate{{"eu", rejects.URL}, {"us", accepts.URL}}, "k")
	if err != nil || region != "us" || base != accepts.URL {
		t.Fatalf("got %q %q %v", region, base, err)
	}
}

func writeUserConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BRONTO_CONFIG_DIR", dir)
	if err := os.MkdirAll(filepath.Join(dir, "bronto"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bronto", "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// An invalid saved value must not lock the user out of the command that
// repairs it.
func TestConfigCommandsWorkOnInvalidSavedValues(t *testing.T) {
	path := writeUserConfig(t, "default_profile = \"default\"\n[profiles.default]\n"+
		"timeout = \"abc\"\nmax_retries = \"-1\"\nregion = \"EU\"\noutput = \"yaml\"\n")

	_, _, err := runCLI(t, "ping", "--api-key", "k", "--base-url", "http://127.0.0.1:1")
	ce := wantCLIErr(t, err, "config_invalid_region", 2)
	if !strings.Contains(ce.Hint, "config set region") {
		t.Fatalf("hint = %q", ce.Hint)
	}
	out, _, err := runCLI(t, "config", "get", "timeout")
	if err != nil || strings.TrimSpace(out) != "abc" {
		t.Fatalf("config get: %q %v", out, err)
	}
	if _, _, err := runCLI(t, "config", "list", "-o", "json"); err != nil {
		t.Fatalf("config list: %v", err)
	}
	for _, kv := range [][2]string{{"region", "eu"}, {"timeout", "30"}, {"max_retries", "2"}, {"output", "json"}} {
		if _, _, err := runCLI(t, "config", "set", kv[0], kv[1]); err != nil {
			t.Fatalf("config set %s: %v", kv[0], err)
		}
	}
	cfg, err := config.Load(config.LoadOptions{Getenv: func(string) string { return "" },
		WorkDir: t.TempDir(), UserConfigDir: filepath.Dir(filepath.Dir(path))})
	if err != nil {
		t.Fatalf("config still invalid after repair: %v", err)
	}
	if v, _ := cfg.Get("timeout"); v.Val != "30" {
		t.Fatalf("timeout = %+v", v)
	}
}

func TestConfigInvalidTimeoutHasHint(t *testing.T) {
	writeUserConfig(t, "default_profile = \"default\"\n[profiles.default]\ntimeout = \"abc\"\n")
	_, _, err := runCLI(t, "ping", "--api-key", "k", "--base-url", "http://127.0.0.1:1")
	ce := wantCLIErr(t, err, "config_invalid_timeout", 2)
	if !strings.Contains(ce.Hint, "bronto config set timeout") || !strings.Contains(ce.Message, "user config file") {
		t.Fatalf("got %+v", ce)
	}
	t.Setenv("BRONTO_MAX_RETRIES", "x")
	_, _, err = runCLI(t, "ping", "--api-key", "k", "--base-url", "http://127.0.0.1:1", "--timeout", "5")
	ce = wantCLIErr(t, err, "config_invalid_max_retries", 2)
	if ce.Hint == "" {
		t.Fatal("max_retries error has no hint")
	}
}

func TestConfigSetRejectsUnknownKeyAndBadValues(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	for _, kv := range [][2]string{
		{"timout", "30"}, {"timeout", "abc"}, {"timeout", "0"}, {"region", "EU"},
		{"max_retries", "-1"}, {"output", "yaml"}, {"base_url", "api.eu.bronto.io"},
	} {
		_, _, err := runCLI(t, "config", "set", kv[0], kv[1])
		if clierr.ExitCode(err) != 2 {
			t.Fatalf("config set %s %s: want exit 2, got %v", kv[0], kv[1], err)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("BRONTO_CONFIG_DIR"), "bronto", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("rejected values must not create the config file (stat err %v)", err)
	}
}

// A profile picked by .bronto.toml switches the key and base_url, so it is
// announced on stderr (unless --quiet).
func TestProjectFileProfileIsAnnounced(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".bronto.toml"), []byte("profile = \"prod\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	_, stderr, err := runCLI(t, "auth", "token", "--api-key", "k")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, `Using profile "prod" from `) || !strings.Contains(stderr, ".bronto.toml") {
		t.Fatalf("stderr = %q", stderr)
	}
	_, stderr, err = runCLI(t, "auth", "token", "--api-key", "k", "--quiet")
	if err != nil || stderr != "" {
		t.Fatalf("--quiet: stderr = %q, err = %v", stderr, err)
	}
	// An explicit --profile wins over the project file: no notice.
	_, stderr, _ = runCLI(t, "auth", "token", "--api-key", "k", "--profile", "dev")
	if strings.Contains(stderr, "Using profile") {
		t.Fatalf("notice printed for a flag-selected profile: %q", stderr)
	}
}

func downloadApp(t *testing.T, baseURL string, timeout time.Duration) *App {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{
		Flags:  map[string]string{"base_url": baseURL},
		Getenv: func(string) string { return "" }, WorkDir: t.TempDir(), UserConfigDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &App{Config: cfg, HTTPClient: &http.Client{Timeout: timeout}, Stderr: io.Discard, Quiet: true}
}

// assertOnlyFile checks that dir holds exactly the given files: a failed
// download must leave neither a truncated destination nor a temp file.
func assertOnlyFile(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(names, ",") {
		t.Fatalf("dir holds %v, want %v", got, names)
	}
}

func TestDownloadExportFailuresLeaveNoPartialFile(t *testing.T) {
	midBody := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	}))
	defer midBody.Close()
	stall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer stall.Close()
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer hang.Close()

	for _, tc := range []struct {
		srv  *httptest.Server
		code string
	}{
		{midBody, "network_error"},
		{stall, "timeout"},
		{hang, "timeout"},
	} {
		dir := t.TempDir()
		dest := filepath.Join(dir, "out.bin")
		if err := os.WriteFile(dest, []byte("previous"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := downloadExport(context.Background(), downloadApp(t, tc.srv.URL, 300*time.Millisecond), tc.srv.URL, dest)
		_ = wantCLIErr(t, err, tc.code, clierr.New(tc.code, "").ExitCode())
		assertOnlyFile(t, dir, "out.bin")
		if b, _ := os.ReadFile(dest); string(b) != "previous" {
			t.Fatalf("%s: existing file clobbered: %q", tc.code, b)
		}
	}
}

func TestDownloadExportLargeSlowBodyIsNotATimeout(t *testing.T) {
	// The timeout bounds gaps between reads, not the whole transfer.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i < 5; i++ {
			_, _ = w.Write([]byte("chunk"))
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	if err := downloadExport(context.Background(), downloadApp(t, srv.URL, 300*time.Millisecond), srv.URL, dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != strings.Repeat("chunk", 5) {
		t.Fatalf("content = %q", b)
	}
	assertOnlyFile(t, dir, "out.bin")
}

func TestDownloadExportRefusesPlainHTTPForHTTPSBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("plain-http location must not be contacted")
	}))
	defer srv.Close()
	dir := t.TempDir()
	err := downloadExport(context.Background(), downloadApp(t, "https://api.eu.bronto.io", time.Second),
		srv.URL, filepath.Join(dir, "out.bin"))
	_ = wantCLIErr(t, err, "export_download_insecure", 1)
	assertOnlyFile(t, dir)

	if err := checkDownloadURL("https://api.eu.bronto.io", "https://bucket.s3.amazonaws.com/x?sig=1"); err != nil {
		t.Fatalf("https location refused: %v", err)
	}
	if err := checkDownloadURL("http://localhost:8080", "http://localhost:9000/x"); err != nil {
		t.Fatalf("http location refused for an http (dev) base: %v", err)
	}
}

// A mistyped --profile used to fall back to the defaults silently.
func TestUnknownProfileIsNotFound(t *testing.T) {
	t.Setenv("BRONTO_API_KEY", "")
	writeUserConfig(t, "default_profile = \"prod\"\n[profiles.prod]\nregion = \"us\"\n")
	_, _, err := runCLI(t, "ping", "--profile", "p2x")
	ce := wantCLIErr(t, err, "profile_not_found", 4)
	if !strings.Contains(ce.Message, "p2x") {
		t.Fatalf("message = %q", ce.Message)
	}
	t.Setenv("BRONTO_PROFILE", "p2x")
	_, _, err = runCLI(t, "auth", "token")
	_ = wantCLIErr(t, err, "profile_not_found", 4)

	// A key from the environment or a flag needs no profile config.
	out, _, err := runCLI(t, "auth", "token", "--api-key", "k")
	if err != nil || strings.TrimSpace(out) != "k" {
		t.Fatalf("flag key: %q %v", out, err)
	}
	// Creating or inspecting a profile is still allowed.
	if _, _, err := runCLI(t, "config", "set", "region", "eu"); err != nil {
		t.Fatalf("config set on a new profile: %v", err)
	}
	if _, _, err := runCLI(t, "config", "list", "--profile", "brand-new"); err != nil {
		t.Fatalf("config list on a new profile: %v", err)
	}
}

func TestBaseURLPasswordIsMaskedInOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	base := strings.Replace(srv.URL, "http://", "http://user:hunter2@", 1)
	t.Setenv("BRONTO_BASE_URL", base)
	for _, args := range [][]string{
		{"ping", "--api-key", "k", "-o", "json"},
		{"config", "list", "-o", "json"},
		{"config", "get", "base_url"},
		{"auth", "status", "--api-key", "kkkkkkkkkkkkkkkk", "-o", "json"},
	} {
		out, stderr, err := runCLI(t, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.Contains(out+stderr, "hunter2") || !strings.Contains(out, "user:xxxxx@") {
			t.Fatalf("%v: out = %q", args, out)
		}
	}
	t.Setenv("BRONTO_BASE_URL", "http://user:hunter2@127.0.0.1:1")
	_, _, err := runCLI(t, "ping", "--api-key", "k")
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("network error leaks the password: %v", err)
	}
}

func TestMalformedBaseURLIsConfigError(t *testing.T) {
	for _, base := range []string{"notaurl", "ftp://x", "https://h/pfx?x=1"} {
		_, _, err := runCLI(t, "ping", "--api-key", "k", "--base-url", base)
		_ = wantCLIErr(t, err, "config_invalid_url", 2)
	}
}
