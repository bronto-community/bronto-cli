package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/query"
)

func newQueryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Work with the search query language",
	}
	cmd.AddCommand(newQueryCheckCmd())
	return cmd
}

// newQueryCheckCmd validates a query expression client-side: syntax with
// caret-positioned errors, and (with a dataset in scope) best-effort
// field existence against /top-keys with did-you-mean suggestions.
func newQueryCheckCmd() *cobra.Command {
	var dataset string
	var strict bool
	cmd := &cobra.Command{
		Use:   "check <query>",
		Short: "Validate a query expression before using it",
		Example: "  bronto query check \"status >= 500 AND level = 'error'\"\n" +
			"  bronto query check \"stauts >= 500\" -d payments-api   # catches the typo",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := args[0]
			node, err := query.Parse(input)
			if err != nil {
				var pe *query.ParseError
				if errors.As(err, &pe) {
					return clierr.New("usage_invalid_query", pe.Msg).
						WithHint("  " + strings.ReplaceAll(pe.Caret(input), "\n", "\n  "))
				}
				return err
			}
			fields := query.Fields(node)

			// Field existence is best-effort: /top-keys only sees recent
			// data, so unknown fields WARN by default; --strict makes
			// them fatal (CI usage).
			var unknownWarnings []string
			if dataset != "" {
				app, err := NewApp(cmd)
				if err != nil {
					return err
				}
				known, err := datasetFieldNames(cmd.Context(), app, dataset)
				if err != nil {
					return err
				}
				knownSet := map[string]bool{}
				for _, k := range known {
					knownSet[k] = true
				}
				for _, f := range fields {
					if strings.HasPrefix(f, "@") || strings.HasPrefix(f, "$") || knownSet[f] {
						continue // internal columns always exist
					}
					w := fmt.Sprintf("field %q not seen in dataset %s recently", f, dataset)
					if s := query.Suggest(f, known); s != "" {
						w += fmt.Sprintf(" — did you mean %q?", s)
					}
					unknownWarnings = append(unknownWarnings, w)
				}
			}

			out := cmd.OutOrStdout()
			if strict && len(unknownWarnings) > 0 {
				for _, w := range unknownWarnings {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "✗ "+w)
				}
				return clierr.New("query_unknown_field",
					fmt.Sprintf("%d unknown field(s)", len(unknownWarnings)))
			}
			for _, w := range unknownWarnings {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+w)
			}
			_, _ = fmt.Fprintf(out, "✓ valid — fields: %s\n", strings.Join(fields, ", "))
			return nil
		},
	}
	cmd.Flags().StringVarP(&dataset, "dataset", "d", "", "dataset (name or UUID) to check field names against")
	cmd.Flags().BoolVar(&strict, "strict", false, "treat unknown fields as errors (for CI)")
	cmd.ValidArgsFunction = defaultArgComplete // positional is free text; hint flags instead
	_ = cmd.RegisterFlagCompletionFunc("dataset", completeDatasets)
	return cmd
}

// datasetFieldNames lists recently-seen field names via /top-keys (the
// same source the fields command uses), over the last day.
func datasetFieldNames(ctx context.Context, app *App, dataset string) ([]string, error) {
	logID, err := resolveDatasetRef(ctx, app, dataset)
	if err != nil {
		return nil, err
	}
	return topKeyNames(ctx, app, logID, "Last 1 day")
}
