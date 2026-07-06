// Package cli contains the Cobra command tree.
package cli

import (
	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/clierr"
	"github.com/svrnm/bronto-cli/internal/version"
)

func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "bronto",
		Short:         "CLI for the Bronto observability platform",
		Long:          "bronto is a command-line client for the Bronto observability platform.\nDocs: https://docs.bronto.io",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.String(),
		Run:           func(cmd *cobra.Command, args []string) {},
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return clierr.New("usage_invalid_flag", err.Error()).
			WithHint("Run 'bronto --help' for usage.")
	})

	pf := cmd.PersistentFlags()
	pf.String("api-key", "", "Bronto management API key (prefer BRONTO_API_KEY env)")
	pf.String("profile", "", "named profile to use")
	pf.String("region", "", "Bronto region: eu or us")
	pf.String("base-url", "", "override the API base URL")
	pf.StringP("output", "o", "", "output format: table|json|jsonl|raw|csv")
	pf.Bool("no-color", false, "disable color output")
	pf.Bool("quiet", false, "suppress non-data messages on stderr")
	cmd.AddCommand(newConfigCmd())
	cmd.AddCommand(newPingCmd())

	return cmd
}
