package cli

import (
	"context"
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
	app, err := NewApp(cmd)
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
	_, _ = fmt.Fprintf(app.Stderr, "Logged in — profile %q, region %s. Key stored in the %s.\n", profile, region, where)
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
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), "Bronto management API key: ")
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
// /logs and returns the first that accepts it. Each probe gets its own
// bounded 5s context so an unreachable host can't hang the command.
//
// If no candidate ever produced an HTTP response (every attempt failed at
// the network layer — DNS, connection refused, timeout, ...), the failure
// says nothing about the key itself: it's a network_error (retryable, exit
// 1). Only once at least one candidate answered — even with a rejection
// like 401/403 — do we conclude the key was checked and rejected:
// auth_invalid_key (exit 3).
func detectRegion(ctx context.Context, cfg *config.Config, key, regionFlag, baseURLFlag string) (region, baseURL string, err error) {
	if regionFlag != "" && regionFlag != "eu" && regionFlag != "us" {
		return "", "", clierr.New("usage_invalid_region",
			fmt.Sprintf("--region must be \"eu\" or \"us\", got %q", regionFlag))
	}
	var lastErr error
	var sawResponse bool
	for _, c := range regionCandidates(cfg, regionFlag, baseURLFlag) {
		apiErr, netErr := probeRegion(ctx, c, key)
		if netErr != nil {
			lastErr = netErr
			continue
		}
		sawResponse = true
		if apiErr != nil {
			lastErr = apiErr
			continue
		}
		return c.region, c.baseURL, nil
	}
	msg := "the key was not accepted in any region"
	if lastErr != nil {
		msg = fmt.Sprintf("%s (%v)", msg, lastErr)
	}
	if !sawResponse {
		return "", "", clierr.New("network_error", msg).WithRetryable().
			WithHint("Check your network connection and the --base-url / --region.")
	}
	return "", "", clierr.New("auth_invalid_key", msg).
		WithHint("You are likely using an ingestion key. This CLI needs a management key (Settings → API Keys in the Bronto UI).").
		WithDocs("https://docs.bronto.io/api-reference/api-keys/overview")
}

// probeRegion issues one bounded GET /logs against a single candidate.
// netErr is a network-layer failure (no HTTP response at all was
// received); apiErr is a non-2xx HTTP response translated to a typed
// error. At most one of the two is non-nil.
func probeRegion(ctx context.Context, c regionCandidate, key string) (apiErr, netErr error) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client := api.NewHTTPClient(key, version.Version)
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, c.baseURL+"/logs", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if ae := api.ErrorFromStatus(resp.StatusCode, body); ae != nil {
		return ae, nil
	}
	return nil, nil
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
			switch {
			case key != "":
				status = checkLiveStatus(cmd.Context(), app)
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
				"base_url":   app.Config.BaseURL(),
				"status":     status,
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows([]string{"profile", "key_source", "key", "region", "base_url", "status"},
				[]map[string]any{row})
		},
	}
}

func checkLiveStatus(ctx context.Context, app *App) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, app.Config.BaseURL()+"/logs", nil)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	resp, err := app.HTTPClient.Do(req)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if apiErr := api.ErrorFromStatus(resp.StatusCode, body); apiErr != nil {
		return apiErr.Error()
	}
	return "ok"
}

func newAuthSwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch <profile>",
		Short: "Set the default profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
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
			_, _ = fmt.Fprintf(app.Stderr, "Switched default profile to %q.\n", profile)
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
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			profile := profileOrDefault(app.Config.Profile())
			if err := secrets.Delete(profile); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(app.Stderr, "Logged out — removed credentials for profile %q.\n", profile)
			return nil
		},
	}
}

func newAuthTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "token",
		Short:   "Print the resolved API key (for scripting)",
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
