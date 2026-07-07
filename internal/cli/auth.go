package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/svrnm/bronto-cli/internal/api"
	"github.com/svrnm/bronto-cli/internal/clierr"
	"github.com/svrnm/bronto-cli/internal/config"
	"github.com/svrnm/bronto-cli/internal/secrets"
	"github.com/svrnm/bronto-cli/internal/version"
)

// readPassword reads a password from the given file descriptor without
// echoing it. Package-level so tests can stub the TTY prompt.
var readPassword = term.ReadPassword

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage Bronto credentials and profiles",
	}
	cmd.AddCommand(newAuthLoginCmd(), newAuthStatusCmd(), newAuthSwitchCmd(), newAuthLogoutCmd())
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
	case stdoutIsTTY():
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), "Bronto management API key: ")
		b, err := readPassword(int(os.Stdin.Fd()))
		_, _ = fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", err
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
// /logs and returns the first that accepts it. All candidates failing is an
// auth_invalid_key error (exit 3).
func detectRegion(ctx context.Context, cfg *config.Config, key, regionFlag, baseURLFlag string) (region, baseURL string, err error) {
	var lastErr error
	for _, c := range regionCandidates(cfg, regionFlag, baseURLFlag) {
		client := api.NewHTTPClient(key, version.Version)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/logs", nil)
		if err != nil {
			return "", "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if apiErr := api.ErrorFromStatus(resp.StatusCode, body); apiErr != nil {
			lastErr = apiErr
			continue
		}
		return c.region, c.baseURL, nil
	}
	msg := "the key was not accepted in any region"
	if lastErr != nil {
		msg = fmt.Sprintf("%s (%v)", msg, lastErr)
	}
	return "", "", clierr.New("auth_invalid_key", msg).
		WithHint("You are likely using an ingestion key. This CLI needs a management key (Settings → API Keys in the Bronto UI).").
		WithDocs("https://docs.bronto.io/api-reference/api-keys/overview")
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
			maskedKey := ""
			if key != "" {
				n := min(8, len(key))
				maskedKey = key[:n] + "…"
			}

			status := "no key"
			if key != "" {
				status = checkLiveStatus(cmd.Context(), app)
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
			if !profileExists(profile) {
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
// config file.
func profileExists(profile string) bool {
	if _, _, err := secrets.Get(profile); err == nil {
		return true
	}
	return hasUserConfigSection(profile)
}

// userConfigProfiles mirrors the shape of the user config file's profile
// sections (internal/config keeps its own equivalent type unexported).
type userConfigProfiles struct {
	DefaultProfile string                       `toml:"default_profile"`
	Profiles       map[string]map[string]string `toml:"profiles"`
}

func hasUserConfigSection(profile string) bool {
	dir, err := configDir()
	if err != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(dir, "bronto", "config.toml"))
	if err != nil {
		return false
	}
	var uf userConfigProfiles
	if err := toml.Unmarshal(b, &uf); err != nil {
		return false
	}
	_, ok := uf.Profiles[profile]
	return ok
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

// configDir resolves the parent config directory: BRONTO_CONFIG_DIR when
// set, else the OS user config directory (same as configcmd.go's 'set').
func configDir() (string, error) {
	if dir := os.Getenv("BRONTO_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	return os.UserConfigDir()
}
