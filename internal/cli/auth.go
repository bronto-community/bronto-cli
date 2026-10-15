package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/bronto-community/bronto-cli/internal/api"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/config"
	"github.com/bronto-community/bronto-cli/internal/secrets"
	"github.com/bronto-community/bronto-cli/internal/version"
)

// readPassword reads a password from the given file descriptor without
// echoing it. Package-level so tests can stub the TTY prompt.
var readPassword = term.ReadPassword

// stdinIsTTY reports whether the process stdin is a terminal. Package-level
// so tests can stub the TTY-dependent interactive-prompt path. The prompt
// must never fire when stdin is not a TTY, regardless of stdout.
var stdinIsTTY = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage Bronto credentials and profiles",
	}
	cmd.AddCommand(newAuthLoginCmd(), newAuthStatusCmd(), newAuthSwitchCmd(), newAuthLogoutCmd(), newAuthTokenCmd())
	return cmd
}

// newLoginAliasCmd registers 'bronto login' as a top-level convenience
// alias for 'bronto auth login' (spec §3).
func newLoginAliasCmd() *cobra.Command {
	return newAuthLoginRunner("login", "Alias for 'bronto auth login'")
}

func newAuthLoginCmd() *cobra.Command {
	return newAuthLoginRunner("login", "Authenticate and store a Bronto API key")
}

func newAuthLoginRunner(use, short string) *cobra.Command {
	var keyStdin bool
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthLogin(cmd, keyStdin)
		},
	}
	cmd.Flags().BoolVar(&keyStdin, "key-stdin", false, "read the API key from stdin instead of prompting")
	return cmd
}

func runAuthLogin(cmd *cobra.Command, keyStdin bool) error {
	app, err := newApp(cmd, appOptions{allowNewProfile: true})
	if err != nil {
		return err
	}
	key, err := acquireKey(cmd, keyStdin)
	if err != nil {
		return err
	}
	regionFlag, _ := cmd.Flags().GetString("region")
	baseURLFlag, _ := cmd.Flags().GetString("base-url")
	region, _, err := detectRegion(cmd.Context(), app.Config, key, regionFlag, baseURLFlag)
	if err != nil {
		return err
	}

	profile := profileOrDefault(app.Config.Profile())
	fallback, err := secrets.Store(profile, key)
	if err != nil {
		return err
	}
	if fallback && !app.Quiet {
		_, _ = fmt.Fprintln(app.Stderr, "Warning: OS keychain unavailable — using the credentials file fallback.")
	}

	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := config.SetUserValue(dir, profile, "region", region); err != nil {
		return err
	}
	if err := config.SetDefaultProfile(dir, profile); err != nil {
		return err
	}

	where := "OS keychain"
	if fallback {
		where = "credentials file"
	}
	app.Notef("Logged in — profile %q, region %s. Key stored in the %s.\n", profile, region, where)
	return nil
}

// acquireKey obtains the API key from stdin (--key-stdin), an interactive
// hidden prompt (TTY), or fails with a usage error (headless, no flag).
func acquireKey(cmd *cobra.Command, keyStdin bool) (string, error) {
	var key string
	switch {
	case keyStdin:
		b, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", err
		}
		key = strings.TrimSpace(string(b))
	case stdoutIsTTY() && stdinIsTTY():
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), "Bronto API key: ")
		b, err := readPassword(int(os.Stdin.Fd()))
		_, _ = fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", errUsageKeyRequired()
		}
		key = strings.TrimSpace(string(b))
	default:
		return "", errUsageKeyRequired()
	}
	if key == "" {
		return "", errUsageKeyRequired()
	}
	return key, nil
}

func errUsageKeyRequired() error {
	return clierr.New("usage_key_required", "no API key provided").
		WithHint("Pipe the key with --key-stdin, or run 'bronto auth login' interactively to be prompted.")
}

type regionCandidate struct {
	region  string
	baseURL string
}

// regionCandidates builds the ordered list of (region, base URL) pairs to
// probe. With --base-url set there is exactly one candidate; otherwise it
// tries every requested region against api.<region>.bronto.io.
func regionCandidates(cfg *config.Config, regionFlag, baseURLFlag string) []regionCandidate {
	if baseURLFlag != "" {
		region := regionFlag
		if region == "" {
			if v, ok := cfg.Get("region"); ok {
				region = v.Val
			}
		}
		return []regionCandidate{{region: region, baseURL: baseURLFlag}}
	}
	regions := []string{"eu", "us"}
	if regionFlag != "" {
		regions = []string{regionFlag}
	}
	cands := make([]regionCandidate, 0, len(regions))
	for _, r := range regions {
		cands = append(cands, regionCandidate{region: r, baseURL: fmt.Sprintf("https://api.%s.bronto.io", r)})
	}
	return cands
}

// detectRegion probes each candidate base URL with the given key via GET
// /logs and returns the first that accepts it. Each probe is bounded to 5s
// so an unreachable host can't hang the command.
//
// Only a 401/403 says anything about the key. If every candidate rejected
// it that way, the key is invalid: auth_invalid_key (exit 3). If any
// candidate failed for another reason (DNS, timeout, 5xx, 429, ...) the
// key may well belong to that region, so that typed error is returned
// instead (network_error, timeout, api_server_error, ...), keeping its
// exit code and retryability.
func detectRegion(ctx context.Context, cfg *config.Config, key, regionFlag, baseURLFlag string) (region, baseURL string, err error) {
	if regionFlag != "" && regionFlag != "eu" && regionFlag != "us" {
		return "", "", clierr.New("usage_invalid_region",
			fmt.Sprintf("--region must be \"eu\" or \"us\", got %q", regionFlag))
	}
	return pickRegion(ctx, regionCandidates(cfg, regionFlag, baseURLFlag), key)
}

// pickRegion is detectRegion's probe loop over explicit candidates.
func pickRegion(ctx context.Context, cands []regionCandidate, key string) (region, baseURL string, err error) {
	var lastAuthErr, forbiddenErr, otherErr error
	var otherRegion string
	for _, c := range cands {
		err := probeRegion(ctx, c, key)
		var ce *clierr.Error
		switch {
		case err == nil:
			return c.region, c.baseURL, nil
		case isAuthRejection(err):
			lastAuthErr = err
			if errors.As(err, &ce) && ce.Code == "auth_insufficient_role" {
				forbiddenErr = err
			}
		default:
			otherErr, otherRegion = err, c.region
		}
	}
	if otherErr != nil {
		var ce *clierr.Error
		if !errors.As(otherErr, &ce) {
			return "", "", otherErr // context cancellation: pass through
		}
		where := otherRegion
		if where == "" {
			where = "the --base-url"
		}
		out := *ce
		out.Message = fmt.Sprintf("could not verify the key against %s: %s", where, ce.Message)
		if ce.Code == "network_error" {
			out.Hint = "Check your network connection and the --base-url / --region."
		}
		return "", "", &out
	}
	// A 403 means a region knows the key but its role doesn't reach the
	// management API (an ingestion-only key). Only 401s everywhere mean
	// the key itself was rejected.
	if forbiddenErr != nil {
		return "", "", clierr.New("auth_insufficient_role",
			fmt.Sprintf("the key lacks permission for the management API (%v)", forbiddenErr)).
			WithHint(api.IngestionKeyHint).
			WithDocs("https://docs.bronto.io/api-reference/api-keys/overview")
	}
	msg := "the key was not accepted in any region"
	if lastAuthErr != nil {
		msg = fmt.Sprintf("%s (%v)", msg, lastAuthErr)
	}
	return "", "", clierr.New("auth_invalid_key", msg).
		WithHint("Check that the key is complete, not revoked, and belongs to this account. " +
			"Keys only work in their own region: pass --region eu or --region us to try one.").
		WithDocs("https://docs.bronto.io/api-reference/api-keys/overview")
}

// isAuthRejection reports whether err is the API rejecting the key itself
// (401/403), as opposed to a network, server or rate-limit failure.
func isAuthRejection(err error) bool {
	var ce *clierr.Error
	return errors.As(err, &ce) && (ce.Code == "auth_invalid_key" || ce.Code == "auth_insufficient_role")
}

// probeTimeout bounds each detectRegion probe. A variable so tests can
// shorten it.
var probeTimeout = 5 * time.Second

// probeRegion issues one bounded GET /logs against a single candidate and
// returns the typed error from api.Do (nil when the key is accepted). The
// bound is the client timeout rather than a context deadline so that a
// slow host classifies as "timeout" like any other request.
func probeRegion(ctx context.Context, c regionCandidate, key string) error {
	client := api.NewHTTPClient(key, version.Version)
	client.Timeout = probeTimeout
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/logs", nil)
	if err != nil {
		return err
	}
	_, err = api.Do(client, req)
	return err
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the resolved profile, credential source, and API reachability",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}

			key, source := "", ""
			if v, ok := app.Config.Get("api_key"); ok && v.Val != "" {
				key, source = v.Val, string(v.Source)
			}
			maskedKey := maskSecret(key)

			status := "no key"
			// liveErr is a failure of the live check that is NOT about the
			// key (network, timeout, 5xx, 429): it is returned as-is so the
			// exit code says what actually went wrong, instead of sending
			// the user to 'bronto auth login' for an outage.
			var liveErr error
			switch {
			case key != "":
				status, liveErr = checkLiveStatus(cmd.Context(), app)
			case app.SecretLookupErr != nil:
				// A genuine credential-lookup failure (e.g. a corrupt
				// credentials file) is not the same as "nothing configured"
				// — surface it instead of reporting a plain "no key".
				status = app.SecretLookupErr.Error()
			}

			region := ""
			if v, ok := app.Config.Get("region"); ok {
				region = v.Val
			}

			row := map[string]any{
				"profile":    profileOrDefault(app.Config.Profile()),
				"key_source": source,
				"key":        maskedKey,
				"region":     region,
				"base_url":   config.RedactURLString(app.Config.BaseURL()),
				"status":     status,
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			if err := p.PrintRows([]string{"profile", "key_source", "key", "region", "base_url", "status"},
				[]map[string]any{row}); err != nil {
				return err
			}
			// A status command that reports a broken credential must also
			// exit non-zero, or `bronto auth status && deploy` gates on
			// nothing. The row above already carries the detail; the error
			// only sets the exit code (quiet keeps stderr clean of the
			// duplicate message in machine pipelines — the envelope is
			// still emitted for parity with every other failure).
			if liveErr != nil {
				return liveErr
			}
			if status != "ok" {
				return clierr.New("auth_status_not_ok",
					fmt.Sprintf("credential check failed: %s", status)).
					WithHint("Run 'bronto auth login' to store a working key, or set BRONTO_API_KEY.")
			}
			return nil
		},
	}
}

// checkLiveStatus probes GET /logs with the resolved key. status is the
// text for the status cell ("ok" or the failure). err is non-nil only for
// failures unrelated to the key itself; a 401/403 is reported through
// status alone and becomes auth_status_not_ok in the caller.
func checkLiveStatus(ctx context.Context, app *App) (status string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, app.Config.BaseURL()+"/logs", nil)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	if _, err := api.Do(app.HTTPClient, req); err != nil {
		if isAuthRejection(err) {
			return err.Error(), nil
		}
		return "error: " + err.Error(), err
	}
	return "ok", nil
}

func newAuthSwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch <profile>",
		Short: "Set the default profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// allowNewProfile: switching away from a missing profile must
			// work. The target profile is checked below.
			app, err := newApp(cmd, appOptions{allowNewProfile: true})
			if err != nil {
				return err
			}
			profile := args[0]
			exists, err := profileExists(profile)
			if err != nil {
				return err
			}
			if !exists {
				return clierr.New("profile_not_found", fmt.Sprintf("no profile named %q", profile)).
					WithHint(fmt.Sprintf("Run 'bronto auth login --profile %s' to create it.", profile))
			}
			dir, err := configDir()
			if err != nil {
				return err
			}
			if err := config.SetDefaultProfile(dir, profile); err != nil {
				return err
			}
			app.Notef("Switched default profile to %q.\n", profile)
			return nil
		},
	}
}

// profileExists reports whether a profile has any known credentials or
// configuration: a keychain/fallback-file entry, or a section in the user
// config file. A malformed user config file is a real error (propagated as
// config_parse_error), not treated as "profile not found".
func profileExists(profile string) (bool, error) {
	if _, _, err := secrets.Get(profile); err == nil {
		return true, nil
	}
	return config.HasProfile("", profile)
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored API key for a profile",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(cmd, appOptions{allowNewProfile: true}) // logout is idempotent
			if err != nil {
				return err
			}
			profile := profileOrDefault(app.Config.Profile())
			if err := secrets.Delete(profile); err != nil {
				return err
			}
			app.Notef("Logged out — removed credentials for profile %q.\n", profile)
			return nil
		},
	}
}

func newAuthTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "token",
		Short:   "Print the API key in use, for scripts",
		Args:    cobra.NoArgs,
		Example: "  export BRONTO_API_KEY=$(bronto auth token)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			key := app.Config.APIKey()
			if key == "" {
				return clierr.New("auth_missing_key", "no API key configured").
					WithHint("Run 'bronto auth login', or set BRONTO_API_KEY / --api-key.")
			}
			_, _ = fmt.Fprintln(app.Stdout, key)
			return nil
		},
	}
}

// configDir resolves the parent config directory: BRONTO_CONFIG_DIR when
// set, else the OS user config directory (also used by configcmd.go's 'set').
func configDir() (string, error) {
	if dir := os.Getenv("BRONTO_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	return os.UserConfigDir()
}

// maskSecret returns the first 8 runes of v followed by an ellipsis, never
// splitting a multi-byte rune. Secrets shorter than 12 runes reveal nothing
// (a partial prefix of a short secret still meaningfully narrows it down),
// masking to a bare ellipsis instead. Empty input stays empty.
func maskSecret(v string) string {
	if v == "" {
		return ""
	}
	r := []rune(v)
	if len(r) < 12 {
		return "…"
	}
	return string(r[:8]) + "…"
}
