package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/spf13/cobra"
)

// exportFormats are the infrastructure-as-code formats the API's export
// endpoints write.
var exportFormats = []string{"terraform", "opentofu"}

// checkExportFlags refuses, before any request, a format the API doesn't
// have and --jq, which can't filter HCL.
func checkExportFlags(app *App, format string) error {
	if !slices.Contains(exportFormats, format) {
		return clierr.New("usage_invalid_flags", fmt.Sprintf("--format %q is not an export format", format)).
			WithHint("Pass --format terraform or --format opentofu.")
	}
	if app.JQ != nil {
		return clierr.New("usage_invalid_flags", "--jq does not apply: export writes HCL, not JSON")
	}
	return nil
}

// writeExport GETs path/export and writes the HCL it answers to stdout
// unchanged: it is text, so -o does not apply.
func writeExport(ctx context.Context, app *App, path, format string, deps bool) error {
	q := url.Values{"format": {format}}
	if deps {
		q.Set("include_dependencies", "true")
	}
	body, err := doRequest(ctx, app, http.MethodGet, path+"/export?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	_, err = app.Stdout.Write(body)
	return err
}

// newExportCmd builds an export command for the resource path resolves
// from the arguments: the generated export verb, and the SSO exports,
// which have no registry resource. what is the thing exported, e.g. "a
// monitor".
func newExportCmd(use, what, example string, deps bool, path func(ctx context.Context, app *App, args []string) (string, error)) *cobra.Command {
	var format string
	var withDeps bool
	cmd := &cobra.Command{
		Use:   use,
		Short: fmt.Sprintf("Write %s as Terraform or OpenTofu", what),
		Long: "Writes " + what + " as infrastructure-as-code (HCL) on stdout, to check into version control\n" +
			"and manage with the Bronto Terraform provider. The output is text, so -o does not apply.",
		Example: example,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			if err := checkExportFlags(app, format); err != nil {
				return err
			}
			p, err := path(cmd.Context(), app, args)
			if err != nil {
				return err
			}
			return writeExport(cmd.Context(), app, p, format, withDeps)
		},
	}
	cmd.Flags().StringVar(&format, "format", "terraform", "export format: terraform or opentofu")
	if deps {
		cmd.Flags().BoolVar(&withDeps, "include-dependencies", false, "also export what it references, and reference that instead of literal ids")
	}
	return cmd
}

func newResourceExportCmd(desc resourceDesc) *cobra.Command {
	cmd := newExportCmd("export <id>", desc.aSingular(),
		fmt.Sprintf("  bronto %s export <id> > %s.tf", desc.display(), strings.ReplaceAll(strings.ToLower(desc.singular()), " ", "_")),
		desc.ExportDeps,
		func(ctx context.Context, app *App, args []string) (string, error) {
			id, err := resolveResourceRef(ctx, app, desc, args[0])
			return desc.idBase() + "/" + url.PathEscape(id), err
		})
	cmd.Args = cobra.ExactArgs(1)
	cmd.ValidArgsFunction = completeResourceRef(desc)
	return cmd
}

// newSSOCmd holds the two SSO exports. The API has no other SSO
// operation, so there is no registry resource to hang them on.
func newSSOCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "sso", Short: "Export the single sign-on configuration"}
	idps := &cobra.Command{Use: "idps", Short: "Export SSO identity providers"}
	idp := newExportCmd("export <idp>", "an SSO identity provider", "  bronto sso idps export Google > idp.tf", false,
		func(_ context.Context, _ *App, args []string) (string, error) {
			return "/sso/idps/" + url.PathEscape(args[0]), nil
		})
	idp.Args = cobra.ExactArgs(1)
	idp.Long += "\nThe client secret is never exported: a comment marks where to set it."
	idps.AddCommand(idp)
	mappings := &cobra.Command{Use: "mappings", Short: "Export SSO mappings"}
	all := newExportCmd("export", "the organization's SSO mappings", "  bronto sso mappings export > sso_mappings.tf", false,
		func(context.Context, *App, []string) (string, error) { return "/sso/mappings", nil })
	all.Args = cobra.NoArgs
	all.Long += "\nThe export is always the complete mapping list, not one mapping."
	mappings.AddCommand(all)
	cmd.AddCommand(idps, mappings)
	return cmd
}
