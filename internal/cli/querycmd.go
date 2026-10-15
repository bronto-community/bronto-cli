package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/bronto-community/bronto-cli/internal/query"
)

func newQueryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Check expressions in the search query language",
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
		Long: "Checks a query expression client-side: syntax (with the error position marked), " +
			"likely mistakes such as an unquoted string value, and with -d whether its fields " +
			"exist in the dataset. Pass '-' as the query to read it from stdin.",
		Example: "  bronto query check \"status >= 500 AND level = 'error'\"\n" +
			"  bronto query check \"status IN (500, 502) AND message LIKE '%timeout%'\"\n" +
			"  bronto query check \"stauts >= 500\" -d prod/payments-api   # catches the typo\n" +
			"  bronto query check - -o json < query.txt",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			input := args[0]
			if input == "-" {
				b, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				input = strings.TrimSpace(string(b))
			}
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
			warnings := query.Warnings(node)

			// Field existence is best-effort: /top-keys only sees recent
			// data, so unknown fields WARN by default; --strict makes
			// them fatal (CI usage).
			var unknownWarnings []string
			if dataset != "" {
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

			format, err := app.DetectFormat(false)
			if err != nil {
				return err
			}
			machine := format == output.FormatJSON || format == output.FormatJSONL
			if strict && len(unknownWarnings) > 0 {
				if !machine {
					for _, w := range unknownWarnings {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "✗ "+w)
					}
				}
				return clierr.New("query_unknown_field",
					fmt.Sprintf("%d unknown field(s)", len(unknownWarnings))).
					WithHint(strings.Join(unknownWarnings, "\n"))
			}
			warnings = append(warnings, unknownWarnings...)
			if machine {
				p, err := app.PrinterFor(format)
				if err != nil {
					return err
				}
				// Failures use the standard error envelope; success is
				// this object, with warnings always an array.
				ws := make([]any, len(warnings))
				for i, w := range warnings {
					ws[i] = w
				}
				fs := make([]any, len(fields))
				for i, f := range fields {
					fs[i] = f
				}
				return p.PrintJSON(map[string]any{"valid": true, "fields": fs, "warnings": ws})
			}
			for _, w := range warnings {
				app.Notef("warning: %s\n", w)
			}
			msg := "✓ valid"
			if len(fields) > 0 {
				msg += " — fields: " + strings.Join(fields, ", ")
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), msg)
			return nil
		},
	}
	cmd.Flags().StringVarP(&dataset, "dataset", "d", "", "dataset (name or UUID) to check field names against")
	cmd.Flags().BoolVar(&strict, "strict", false, "fail on unknown fields instead of warning (for CI)")
	cmd.ValidArgsFunction = defaultArgComplete // positional is free text; hint flags instead
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
