package cli

import (
	"net/http"

	"github.com/spf13/cobra"
)

// newOrgCmd builds "org show|update": the organization's customer record
// (GET/PATCH /customer).
func newOrgCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "org", Short: "Show or update the organization record"}
	cmd.AddCommand(&cobra.Command{
		Use:     "show",
		Short:   "Show the organization record",
		Long:    "Prints the organization's customer record: its ids, name, region, billing emails and the organizations of the same customer.",
		Example: "  bronto org show\n  bronto org show -o json --jq .customer_name",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			return printFetched(cmd.Context(), app, "/customer")
		},
	})
	update := &cobra.Command{
		Use:   "update",
		Short: "Update the organization's name and billing emails",
		Long:  "Changes the customer record. The API takes customer_name and billing_emails, and documents both as required: send the current value of the one you keep (see 'bronto org show').",
		Example: "  bronto org update -f customer_name=<name> -f 'billing_emails=[\"<email>\"]'\n" +
			"  bronto org update --input org.json",
		Args: cobra.NoArgs,
	}
	body := bodyFlags(update)
	update.RunE = func(cmd *cobra.Command, _ []string) error {
		b, err := body()
		if err != nil {
			return err
		}
		app, err := NewApp(cmd)
		if err != nil {
			return err
		}
		return sendWrite(cmd.Context(), app, http.MethodPatch, "/customer", b, "Updated the organization record.", "/customer")
	}
	cmd.AddCommand(update)
	return cmd
}
