package cli

import (
	"net/http"
	"net/url"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/timerange"
	"github.com/spf13/cobra"
)

// newPoliciesCmd builds "policies list <resource-type> <resource-id>"
// (GET /policies/resources/{type}/{id}), the one policies endpoint the
// API documents.
func newPoliciesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "policies", Short: "List the access policies on a resource"}
	cmd.AddCommand(&cobra.Command{
		Use:     "list <resource-type> <resource-id>",
		Short:   "List the policies that apply to a resource",
		Long:    "Lists the policies associated with one resource, given by id. The resource type is the API's name for it: logset for a dataset (the API answers 400 to the spec's other example, dataset).",
		Example: "  bronto policies list logset <dataset-id>",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodGet,
				"/policies/resources/"+url.PathEscape(args[0])+"/"+url.PathEscape(args[1]), nil)
			if err != nil {
				return err
			}
			p, rows, err := humanRows(app, rowsFromPayload(payload, "policies"), resourceListPolish)
			if err != nil {
				return err
			}
			return p.PrintRows(bronto.EventColumns(rows, 8), rows)
		},
	})
	return cmd
}

// newDatasetResolveCmd builds "datasets resolve": which datasets hold
// events matching a filter or a from expression (POST /datasets/resolve,
// which changes nothing).
func newDatasetResolveCmd() *cobra.Command {
	var where, fromExpr, since, matchType, expandType string
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Find the datasets a from expression selects",
		Long: `Asks Bronto which datasets a query would read: those a --from-expr
selects, narrowed by --where to the ones with matching events in the time
window. Prints the dataset ids and the keys used to resolve them.`,
		Example: "  bronto datasets resolve --from-expr \"environment = 'production'\"\n" +
			"  bronto datasets resolve --from-expr \"environment = 'production'\" --where \"status >= 500\" --since 1h",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The spec takes --where alone, but live (2026-11-26) that
			// answers 500; with a from_expr it answers 200.
			if fromExpr == "" {
				return clierr.New("usage_invalid_flags", "pass --from-expr (the datasets to choose from)").
					WithHint("e.g. --from-expr \"environment = 'production'\"; --where narrows it further.")
			}
			spec, err := timerange.Resolve(since, "", "", nil)
			if err != nil {
				return err
			}
			body := map[string]any{}
			for k, v := range map[string]string{"where": where, "from_expr": fromExpr, "match_type": matchType, "expand_type": expandType} {
				if v != "" {
					body[k] = v
				}
			}
			if spec.TimeRange != "" {
				body["time_range"] = spec.TimeRange
			} else {
				body["from_ts"], body["to_ts"] = spec.FromTs, spec.ToTs
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			app.DryRun = false // a read, though it is a POST
			return sendWrite(cmd.Context(), app, http.MethodPost, "/datasets/resolve", body, "")
		},
	}
	cmd.Flags().StringVar(&where, "where", "", "only datasets with events matching this filter")
	cmd.Flags().StringVar(&fromExpr, "from-expr", "", "expression selecting datasets by their tags (required)")
	cmd.Flags().StringVar(&since, "since", "1h", "lookback window, e.g. 15m, 1h, or 1h30m")
	cmd.Flags().StringVar(&matchType, "match-type", "", "how keys match: EXACT, IEXACT, SUBSTRING or ISUBSTRING")
	cmd.Flags().StringVar(&expandType, "expand-type", "", "key expansion: DISABLED or SYNONYM")
	return cmd
}

// newWidgetPatchCmd builds "widgets patch <widget>" (PATCH /widgets/{id}):
// the partial update next to the full-replacement "widgets update". One
// call takes one of name, aux, layout, or display and/or sort.
func newWidgetPatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "patch <widget>",
		Short: "Change one part of a widget without resending the rest",
		Long: `Changes one part of a widget and leaves the rest as it is, unlike
'widgets update', which replaces the widget. One call carries one of: name,
aux, layout (a group widget's widget_layouts), or display and/or sort
(within those, a value sets a field and null clears it). It confirms on
stderr; with -o json or --jq it prints the updated widget.`,
		Example: "  bronto widgets patch <widget> -f name=<name>\n" +
			"  bronto widgets patch <widget> -f 'display={\"stacked_bars\":true}'\n" +
			"  bronto widgets patch <group-widget> --input layout.json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKindRef("widgets"),
	}
	body := bodyFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		b, err := body()
		if err != nil {
			return err
		}
		app, err := NewApp(cmd)
		if err != nil {
			return err
		}
		id, err := resolveKindRef(cmd.Context(), app, "widgets", args[0])
		if err != nil {
			return err
		}
		path := "/widgets/" + url.PathEscape(id)
		return sendWrite(cmd.Context(), app, http.MethodPatch, path, b, "Updated widget "+args[0]+".", path)
	}
	return cmd
}
