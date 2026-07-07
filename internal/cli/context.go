package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/api"
	"github.com/svrnm/bronto-cli/internal/clierr"
	"github.com/svrnm/bronto-cli/internal/config"
	"github.com/svrnm/bronto-cli/internal/output"
	"github.com/svrnm/bronto-cli/internal/secrets"
	"github.com/svrnm/bronto-cli/internal/version"
)

// secretLookup resolves a stored API key for a profile. Package-level so
// tests can stub the keychain/fallback-file lookup.
var secretLookup = secrets.Get

// stdoutIsTTY reports whether the process stdout is a terminal.
// Package-level so tests can stub the TTY-dependent output path.
var stdoutIsTTY = func() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}

// App bundles everything a command needs. Built once per invocation.
type App struct {
	Config      *config.Config
	Stdout      io.Writer
	Stderr      io.Writer
	HTTPClient  *http.Client
	StdoutIsTTY bool
	OutputFlag  string
	Quiet       bool
	Color       bool
}

func NewApp(cmd *cobra.Command) (*App, error) {
	flags := map[string]string{}
	for _, name := range []string{"api-key", "profile", "region", "base-url", "output"} {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			key := map[string]string{
				"api-key": "api_key", "base-url": "base_url",
				"profile": "profile", "region": "region", "output": "output",
			}[name]
			flags[key] = f.Value.String()
		}
	}
	cfg, err := config.Load(config.LoadOptions{Flags: flags})
	if err != nil {
		return nil, err
	}
	quiet, _ := cmd.Flags().GetBool("quiet")
	if cfg.APIKey() == "" {
		if key, fb, err := secretLookup(profileOrDefault(cfg.Profile())); err == nil {
			cfg.Inject("api_key", key, config.SourceKeychain)
			if fb && !quiet {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
					"Warning: OS keychain unavailable — using the credentials file fallback.")
			}
		}
	}
	noColor, _ := cmd.Flags().GetBool("no-color")
	outFlag := ""
	if v, ok := cfg.Get("output"); ok {
		outFlag = v.Val
	}
	ttyNow := stdoutIsTTY()
	// CRITICAL: httpClient captures cfg.APIKey() at construction time, so the
	// keychain injection above MUST happen before this line.
	httpClient := api.NewHTTPClient(cfg.APIKey(), version.Version)
	if v, ok := cfg.Get("timeout"); ok {
		secs, err := strconv.Atoi(v.Val)
		if err != nil || secs <= 0 {
			return nil, clierr.New("config_invalid_timeout",
				fmt.Sprintf("timeout must be a positive integer (seconds), got %q", v.Val))
		}
		httpClient.Timeout = time.Duration(secs) * time.Second
	}
	return &App{
		Config:      cfg,
		Stdout:      cmd.OutOrStdout(),
		Stderr:      cmd.ErrOrStderr(),
		HTTPClient:  httpClient,
		StdoutIsTTY: ttyNow,
		OutputFlag:  outFlag,
		Quiet:       quiet,
		Color:       output.ColorEnabled(noColor, ttyNow, os.Getenv),
	}, nil
}

func (a *App) Printer(streaming bool) (*output.Printer, error) {
	f, err := output.DetectFormat(a.OutputFlag, a.StdoutIsTTY, streaming)
	if err != nil {
		return nil, err
	}
	return output.NewPrinter(a.Stdout, f), nil
}
