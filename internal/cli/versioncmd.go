package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/bronto-community/bronto-cli/internal/version"
)

// newVersionCmd implements `bronto version`. Deliberately does NOT go
// through NewApp: printing the version must work even with broken config
// or missing credentials, so only the output format flag is consulted.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, commit, and build date",
		Example: "  bronto version\n" +
			"  bronto version -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			outFlag, _ := cmd.Flags().GetString("output")
			format, err := output.DetectFormat(outFlag, stdoutIsTTY(), false)
			if err != nil {
				return err
			}
			switch format {
			case output.FormatTable, output.FormatRaw, output.FormatCSV:
				_, err = fmt.Fprintln(cmd.OutOrStdout(), version.String())
				return err
			default: // json / jsonl
				enc := json.NewEncoder(cmd.OutOrStdout())
				return enc.Encode(map[string]string{
					"version": version.Version,
					"commit":  version.Commit,
					"date":    version.Date,
				})
			}
		},
	}
}
