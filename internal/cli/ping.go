package cli

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/api"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/config"
)

func newPingCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ping",
		Short:   "Check connectivity and credentials against the Bronto API",
		Example: "  bronto ping\n  BRONTO_API_KEY=... bronto ping -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			if app.Config.APIKey() == "" {
				return clierr.New("auth_missing_key", "no API key configured").
					WithHint("Set BRONTO_API_KEY or pass --api-key.")
			}
			// shown is the base URL for output: a password in its userinfo
			// is masked, as url.Error does in network errors.
			shown := config.RedactURLString(app.Config.BaseURL())
			start := time.Now()
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet,
				app.Config.BaseURL()+"/logs", nil)
			if err != nil {
				return err
			}
			if _, err := api.Do(app.HTTPClient, req); err != nil {
				// Name the base URL on transport failures: "cannot reach"
				// is the question ping exists to answer.
				var ce *clierr.Error
				if errors.As(err, &ce) && (ce.Code == "network_error" || ce.Code == "timeout") {
					ce.Message = fmt.Sprintf("cannot reach %s: %s", shown, ce.Message)
				}
				return err
			}
			latency := time.Since(start).Milliseconds()
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			if app.StdoutIsTTY && app.OutputFlag == "" {
				_, _ = fmt.Fprintf(app.Stdout, "OK — %s (%dms)\n", shown, latency)
				return nil
			}
			return p.PrintJSON(map[string]any{
				"status": "ok", "base_url": shown, "latency_ms": latency,
			})
		},
	}
}
