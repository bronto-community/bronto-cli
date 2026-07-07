package cli

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/api"
	"github.com/svrnm/bronto-cli/internal/clierr"
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
			start := time.Now()
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet,
				app.Config.BaseURL()+"/logs", nil)
			if err != nil {
				return err
			}
			resp, err := app.HTTPClient.Do(req)
			if err != nil {
				return clierr.New("api_unreachable", fmt.Sprintf("cannot reach %s: %v", app.Config.BaseURL(), err)).
					WithHint("Check your network and the region (--region eu|us).")
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			if apiErr := api.ErrorFromStatus(resp.StatusCode, body); apiErr != nil {
				return apiErr
			}
			latency := time.Since(start).Milliseconds()
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			if app.StdoutIsTTY && app.OutputFlag == "" {
				_, _ = fmt.Fprintf(app.Stdout, "OK — %s (%dms)\n", app.Config.BaseURL(), latency)
				return nil
			}
			return p.PrintJSON(map[string]any{
				"status": "ok", "base_url": app.Config.BaseURL(), "latency_ms": latency,
			})
		},
	}
}
