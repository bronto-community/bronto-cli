package cli

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/clierr"
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
		Long:  "Changes the customer record. The API takes customer_name and billing_emails, and both are required: send the current value of the one you keep (see 'bronto org show').",
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
		if err := checkOrgBody(b); err != nil {
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

// checkOrgBody refuses an org update body without customer_name or
// billing_emails before any request: the API answers 400 ("provided
// object should contain property billing_emails", live 2026-11-27).
func checkOrgBody(body []byte) error {
	var obj map[string]any
	_ = json.Unmarshal(body, &obj)
	if obj == nil {
		return nil // not a JSON object: let the API report it
	}
	var missing []string
	for _, k := range []string{"customer_name", "billing_emails"} {
		if _, ok := obj[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return clierr.New("usage_missing_fields", "org update needs both customer_name and billing_emails: the body lacks "+strings.Join(missing, ", ")).
		WithHint("Send both: bronto org update -f customer_name=<name> -f 'billing_emails=[\"<email>\"]'. " +
			"'bronto org show' prints the current values.")
}
