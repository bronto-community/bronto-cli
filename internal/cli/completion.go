package cli

import (
	"context"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/config"
	"github.com/bronto-community/bronto-cli/internal/output"
)

// Shell completion. bronto registers ValidArgsFunction on positionals and
// RegisterFlagCompletionFunc on flag values so tab-completion offers real
// datasets/resources/fields instead of cobra's default file completion.
// Every API-backed completer is bounded by completionTimeout and degrades to
// "no candidates, no file fallback" on any error, so a slow or offline API
// never hangs the shell. File completion is deliberately kept only where a
// path is actually expected (--input, --local, send from a file).

type compFunc = func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)

// completionTimeout bounds any API-backed completion.
const completionTimeout = 2 * time.Second

// completionApp builds an App for a completion callback. Flags are parsed by
// the time __complete runs, so NewApp behaves as in a normal invocation; its
// stderr is redirected to discard so warnings (keychain fallback, etc.) never
// leak into the shell. ok is false when no App can be built (e.g. no
// credentials), in which case the caller completes nothing.
func completionApp(cmd *cobra.Command) (app *App, ctx context.Context, cancel context.CancelFunc, ok bool) {
	cmd.SetErr(io.Discard)
	a, err := NewApp(cmd)
	if err != nil {
		return nil, nil, nil, false
	}
	base := cmd.Context()
	if base == nil {
		base = context.Background()
	}
	c, cxl := context.WithTimeout(base, completionTimeout)
	return a, c, cxl, true
}

// --- static completions ---

func completeOutputFormat(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return []string{
		string(output.FormatTable) + "\tcolumnar, TTY default",
		string(output.FormatJSON) + "\tsingle JSON document",
		string(output.FormatJSONL) + "\tone JSON object per line",
		string(output.FormatRaw) + "\t@raw / whole row verbatim",
		string(output.FormatCSV) + "\tcomma-separated values",
	}, cobra.ShellCompDirectiveNoFileComp
}

func completeRegion(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return []string{"eu", "us"}, cobra.ShellCompDirectiveNoFileComp
}

func completeProfiles(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	names, err := config.ListProfiles("")
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// noFileComplete suppresses the file fallback for a positional that is not a
// path (a query, an expression) without offering candidates of its own.
func noFileComplete(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// --- dynamic completions ---

// completeDatasets lists the account's datasets, qualifying names that are
// duplicated across collections (mirroring resolveDatasetRef).
func completeDatasets(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	app, ctx, cancel, ok := completionApp(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer cancel()
	ds, err := listDatasets(ctx, app)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	counts := map[string]int{}
	for _, d := range ds {
		counts[d.name]++
	}
	var out []string
	for _, d := range ds {
		if d.name == "" {
			continue
		}
		name := d.name
		if counts[d.name] > 1 {
			name = d.qualified()
		}
		out = append(out, name+"\t"+d.collection)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// listResourceNames fetches a resource's rows and returns "name\tid"
// completions, reusing the registry descriptor's list keys and name/id keys.
func listResourceNames(cmd *cobra.Command, desc resourceDesc) []string {
	app, ctx, cancel, ok := completionApp(cmd)
	if !ok {
		return nil
	}
	defer cancel()
	payload, err := doJSONRequest(ctx, app, http.MethodGet, desc.Base, nil)
	if err != nil {
		return nil
	}
	rows := rowsFromPayload(payload, desc.ListRowKeys...)
	var out []string
	for _, row := range rows {
		id := desc.rowID(row)
		for _, key := range desc.nameKeys() {
			if v, _ := row[key].(string); v != "" {
				if id != "" {
					out = append(out, v+"\t"+id)
				} else {
					out = append(out, v)
				}
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// completeResourceRef is a ValidArgsFunction completing the first positional
// (a resource ref) with the resource's names; later positionals get no
// candidates (still suppressing files).
func completeResourceRef(desc resourceDesc) compFunc {
	return func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return listResourceNames(cmd, desc), cobra.ShellCompDirectiveNoFileComp
	}
}

// completeKindRef is completeResourceRef for hand-written extras that know
// their registry entry only by kind name.
func completeKindRef(kind string) compFunc {
	if desc, ok := descByName(kind); ok {
		return completeResourceRef(desc)
	}
	return noFileComplete
}

// completeKindFlag lists a kind's names for a flag value (no positional-arg
// guard, unlike completeResourceRef).
func completeKindFlag(kind string) compFunc {
	return func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		desc, ok := descByName(kind)
		if !ok {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return listResourceNames(cmd, desc), cobra.ShellCompDirectiveNoFileComp
	}
}

// descByName finds a registry descriptor by command name.
func descByName(name string) (resourceDesc, bool) {
	for _, d := range resourceRegistry {
		if d.Name == name {
			return d, true
		}
	}
	return resourceDesc{}, false
}

// datasetFlagValue reads the -d/--dataset value off the command line during
// completion, tolerating both the StringArray (search/tail) and String
// (fields) flag shapes.
func datasetFlagValue(cmd *cobra.Command) string {
	if cmd.Flags().Lookup("dataset") == nil {
		return ""
	}
	if sv, err := cmd.Flags().GetStringArray("dataset"); err == nil {
		if len(sv) > 0 {
			return sv[0]
		}
		return ""
	}
	return cmd.Flags().Lookup("dataset").Value.String()
}

// fieldNamesForCmd fetches the field names of the dataset named by the -d on
// the command line. Completion of fields REQUIRES a dataset (an unscoped
// /top-keys over every dataset would be slow and noisy), so it returns
// nothing when -d is absent.
func fieldNamesForCmd(cmd *cobra.Command) []string {
	dsRef := datasetFlagValue(cmd)
	if dsRef == "" {
		return nil
	}
	app, ctx, cancel, ok := completionApp(cmd)
	if !ok {
		return nil
	}
	defer cancel()
	logID, err := resolveDatasetRef(ctx, app, dsRef)
	if err != nil {
		return nil
	}
	names, err := fetchFieldNames(ctx, app, logID, "Last 1 hour")
	if err != nil {
		return nil
	}
	sort.Strings(names)
	return names
}

// completeFields completes a field name (--select, -g, --fields).
func completeFields(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return fieldNamesForCmd(cmd), cobra.ShellCompDirectiveNoFileComp
}

// completeFilterField completes a structured filter flag value with
// "field=" (--eq/--gt/…). NoSpace keeps the cursor on the value after the =.
func completeFilterField(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	names := fieldNamesForCmd(cmd)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, n+"=")
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}
