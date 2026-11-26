package cli

import (
	"github.com/spf13/cobra"
)

// bodyFlags adds the create/update body flags (-f, --input) to cmd and
// returns the function that reads the body from them.
func bodyFlags(cmd *cobra.Command) func() ([]byte, error) {
	var fields []string
	var input string
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "request body field as key=value; values parse as JSON when possible (repeatable)")
	cmd.Flags().StringVar(&input, "input", "", "read the request body from a file, or - for stdin")
	return func() ([]byte, error) { return resourceRequestBody(cmd, input, fields) }
}
