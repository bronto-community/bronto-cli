package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func execPing(t *testing.T, srvStatus int) (stdout string, err error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/logs" {
			t.Errorf("ping hit %s, want /logs", r.URL.Path)
		}
		w.WriteHeader(srvStatus)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"ping", "--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	err = root.Execute()
	return out.String(), err
}

func TestPingOK(t *testing.T) {
	out, err := execPing(t, 200)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Status  string `json:"status"`
		BaseURL string `json:"base_url"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output not JSON: %v (%q)", err, out)
	}
	if got.Status != "ok" || got.BaseURL == "" {
		t.Fatalf("got %+v", got)
	}
}

func TestPingForbiddenIsTypedAuthError(t *testing.T) {
	_, err := execPing(t, 403)
	if err == nil {
		t.Fatal("want error")
	}
	if clierr.ExitCode(err) != 3 {
		t.Fatalf("exit code = %d, want 3", clierr.ExitCode(err))
	}
}

// TestPingUnreachableIsRetryableNetworkError pins the machine contract: an
// unreachable base URL is a retryable network_error (exit 1), aligned with
// how bronto.Client and the resources/apicmd request paths classify the same
// failure — not the bespoke, non-retryable api_unreachable code ping used
// to return.
func TestPingUnreachableIsRetryableNetworkError(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"ping", "--base-url", "http://127.0.0.1:1", "--api-key", "k"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 1 {
		t.Fatalf("want exit 1 (network_error), got %v", err)
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "network_error" || !ce.Retryable {
		t.Fatalf("want retryable network_error, got %v", err)
	}
}

func TestPingHumanOutputOnTTY(t *testing.T) {
	old := stdoutIsTTY
	stdoutIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY = old })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"ping", "--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "OK — ") || !strings.Contains(out.String(), srv.URL) {
		t.Fatalf("human output = %q", out.String())
	}
}

// TestNoColorFlagDisablesColor exercises NewApp directly rather than routing
// through cobra parent/child flag inheritance (root.Find doesn't parse flags,
// and PersistentFlags().Parse on root doesn't propagate to a subcommand's
// Flags() lookup cleanly). Building a minimal *cobra.Command with the same
// flags NewApp reads and parsing it directly is a faithful, honest exercise
// of the wiring: it fails if the Color field is removed (compile error) or
// if ColorEnabled stops being consulted (the TTY-without-no-color case would
// stay false instead of true).
func TestNoColorFlagDisablesColor(t *testing.T) {
	old := stdoutIsTTY
	stdoutIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY = old })

	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("TERM", "")

	newCmd := func(args ...string) *cobra.Command {
		cmd := &cobra.Command{Use: "test"}
		fs := cmd.Flags()
		fs.String("api-key", "", "")
		fs.String("profile", "", "")
		fs.String("region", "", "")
		fs.String("base-url", "", "")
		fs.StringP("output", "o", "", "")
		fs.Bool("no-color", false, "")
		fs.Bool("quiet", false, "")
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return cmd
	}

	appNoColor, err := NewApp(newCmd("--no-color", "--api-key", "k"))
	if err != nil {
		t.Fatal(err)
	}
	if appNoColor.Color {
		t.Fatal("Color must be false when --no-color set")
	}

	appColor, err := NewApp(newCmd("--api-key", "k"))
	if err != nil {
		t.Fatal(err)
	}
	if !appColor.Color {
		t.Fatal("Color must be true on TTY without --no-color")
	}
}

func TestTimeoutConfigAppliesToHTTPClient(t *testing.T) {
	t.Setenv("BRONTO_TIMEOUT", "5")
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	cmd := NewRootCmd()
	pingCmd, _, err := cmd.Find([]string{"ping"})
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(pingCmd)
	if err != nil {
		t.Fatal(err)
	}
	if app.HTTPClient.Timeout != 5*time.Second {
		t.Fatalf("timeout = %v, want 5s", app.HTTPClient.Timeout)
	}
}
