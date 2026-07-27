package cli

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/config"
	"github.com/bronto-community/bronto-cli/internal/output"
)

// maxCompletionValues caps how many sample values a filter-flag value
// completion offers, so a high-cardinality field doesn't flood the shell.
const maxCompletionValues = 20

// completeHiddenCommands surfaces the hidden easter-egg commands (graze/herd/
// rumble) in root completion — cobra omits hidden subcommands, so without this
// `bronto g<tab>` would never suggest `graze`. Set as root.ValidArgsFunction;
// cobra merges the result with the visible subcommand names.
func completeHiddenCommands(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var out []string
	for _, c := range cmd.Commands() {
		if c.Hidden && !cobraHiddenBuiltin(c.Name()) && strings.HasPrefix(c.Name(), toComplete) {
			out = append(out, c.Name()+"\t"+c.Short)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// cobraHiddenBuiltin reports whether a hidden command is one cobra manages
// itself (the __complete pair), which must not be surfaced as a suggestion.
func cobraHiddenBuiltin(name string) bool {
	return name == "__complete" || name == "__completeNoDesc"
}

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

// datasetCompletionThreshold is the dataset count above which -d completion
// drills down by collection first (collection/ → datasets) instead of
// dumping every dataset name at once.
const datasetCompletionThreshold = 40

// completeDatasets lists the account's datasets. Small accounts get a flat
// list of names (duplicates qualified as collection/name). Large accounts get
// a two-level flow: with no "/" typed it offers collections ("web/", NoSpace
// so the next tab drills in); once "collection/" is typed it offers that
// collection's datasets.
func completeDatasets(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	app, ctx, cancel, ok := completionApp(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer cancel()
	ds, err := listDatasets(ctx, app)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if len(ds) <= datasetCompletionThreshold {
		return flatDatasetNames(ds), cobra.ShellCompDirectiveNoFileComp
	}
	// Large account: drill down by collection. Once the user has typed
	// "collection/", offer that collection's datasets (qualified so the ref
	// resolves); datasets without a collection are matched too.
	if slash := strings.IndexByte(toComplete, '/'); slash >= 0 {
		coll := toComplete[:slash]
		var out []string
		for _, d := range ds {
			if d.name != "" && d.collection == coll {
				out = append(out, d.qualified()+"\t"+d.name)
			}
		}
		sort.Strings(out)
		return out, cobra.ShellCompDirectiveNoFileComp
	}
	// Otherwise offer the collections. NoSpace keeps the cursor after
	// "collection/" so the next tab lists that collection's datasets. Bare
	// (collection-less) datasets are offered by name alongside.
	seen := map[string]bool{}
	var out []string
	for _, d := range ds {
		switch {
		case d.collection != "" && !seen[d.collection]:
			seen[d.collection] = true
			out = append(out, d.collection+"/\tcollection")
		case d.collection == "" && d.name != "":
			out = append(out, d.name+"\tdataset")
		}
	}
	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// flatDatasetNames returns every dataset as a completion candidate, qualifying
// names duplicated across collections (mirroring resolveDatasetRef).
func flatDatasetNames(ds []datasetInfo) []string {
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
	return out
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
		name := ""
		for _, key := range desc.nameKeys() {
			if v, _ := row[key].(string); v != "" {
				name = v
				break
			}
		}
		switch {
		case name != "" && id != "":
			out = append(out, name+"\t"+id)
		case name != "":
			out = append(out, name)
		case id != "":
			// No name field (e.g. limits): the opaque id still completes,
			// which beats a silent file-completion fallback.
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// completeResourceRef is a ValidArgsFunction completing the first positional
// (a resource ref) with the resource's names; later positionals get no
// candidates (still suppressing files). datasets is special-cased to the
// dataset completer because its rows key name/id as log/log_id (mirroring
// resolveResourceRef's own datasets special case).
func completeResourceRef(desc resourceDesc) compFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		if desc.Name == "datasets" {
			return completeDatasets(cmd, args, toComplete)
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

// completeFieldsOrHints is the `fields` positional completer: the dataset's
// field names when a -d is on the line, otherwise the command's flag hints
// (so `bronto fields <tab>` surfaces --dataset/--since rather than nothing).
func completeFieldsOrHints(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if names := fieldNamesForCmd(cmd); len(names) > 0 {
		return names, cobra.ShellCompDirectiveNoFileComp
	}
	return flagHints(cmd)
}

// apiMethods are the HTTP methods `bronto api` accepts, in a sensible order.
var apiMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"}

// completeAPIArgs completes `bronto api <METHOD> <path>`: the method for the
// first positional, then known collection paths for the second.
func completeAPIArgs(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return apiMethods, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
	case 1:
		return apiPathHints(), cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// apiPathHints returns the management-API collection paths the CLI knows about
// (from the resource registry) as starting points for `bronto api <method> <tab>`.
func apiPathHints() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range resourceRegistry {
		for _, p := range []string{d.Base, d.createPath()} {
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// filterFieldStage completes the field half of a filter flag: "$model=", with
// NoSpace so the cursor stays put for the value.
func filterFieldStage(cmd *cobra.Command) ([]string, cobra.ShellCompDirective) {
	names := fieldNamesForCmd(cmd)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, n+"=")
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// completeFilterField completes a comparison/regex filter flag (--gt/--lt/
// --match/…): the field name, then nothing — the value is a threshold or
// pattern the user types, not one of a set. Offering the field's observed
// values here would be noise (a numeric field returns a flood of arbitrary
// numbers; you want to type "1000", not pick "13").
func completeFilterField(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if strings.ContainsRune(toComplete, '=') {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterFieldStage(cmd)
}

// completeFilterFieldWithValues completes an equality filter flag (--eq/--ne):
// the field name, then a sample of that field's observed values from the same
// /top-keys data `bronto fields` shows (capped, $-/case tolerant). Equality is
// the one operator where "which known value?" is the actual question.
func completeFilterFieldWithValues(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if eq := strings.IndexByte(toComplete, '='); eq >= 0 {
		field := toComplete[:eq]
		values := fieldValuesForCmd(cmd, field)
		out := make([]string, 0, len(values))
		for _, v := range values {
			out = append(out, field+"="+v)
		}
		// No NoSpace: once a value is chosen the clause is complete.
		return out, cobra.ShellCompDirectiveNoFileComp
	}
	return filterFieldStage(cmd)
}

// fieldValuesForCmd returns a capped, sorted sample of the values observed for
// one field in the dataset named by -d, via /top-keys (the same endpoint
// `bronto fields` uses). Field matching is tolerant of the $-prefix and case,
// so "model=" resolves to the "$model" key. Empty when no -d is set, the field
// isn't found, or it carries no value sample.
func fieldValuesForCmd(cmd *cobra.Command, field string) []string {
	dsRef := datasetFlagValue(cmd)
	if dsRef == "" || field == "" {
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
	params := url.Values{"time_range": {"Last 1 hour"}, "log_id": {logID}}
	var payload map[string]any
	client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
	if err := client.GetJSON(ctx, "/top-keys", params, &payload); err != nil {
		return nil
	}
	target := normalizeFieldName(field)
	for _, r := range normalizeTopKeys(payload) {
		k, _ := r["key"].(string)
		if normalizeFieldName(k) != target {
			continue
		}
		vals, _ := r["values"].([]string)
		out := make([]string, 0, len(vals))
		for _, v := range vals {
			if v != "" { // an empty-string sample isn't a useful completion
				out = append(out, v)
			}
		}
		if len(out) > maxCompletionValues {
			out = out[:maxCompletionValues]
		}
		return out
	}
	return nil
}

// completeSince suggests common relative lookback windows for --since/--window.
func completeSince(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return []string{
		"15m\t15 minutes", "1h\t1 hour", "6h\t6 hours", "12h\t12 hours",
		"1d\t1 day", "3d\t3 days", "1w\t1 week",
	}, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
}

// completeCollections lists the account's collections (for send --collection).
func completeCollections(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	app, ctx, cancel, ok := completionApp(cmd)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer cancel()
	ds, err := listDatasets(ctx, app)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range ds {
		if d.collection != "" && !seen[d.collection] {
			seen[d.collection] = true
			out = append(out, d.collection)
		}
	}
	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeConfigKeys completes a config key argument (config get/set) from the
// settable keys. The guard on args keeps `set <key> <value>` from re-offering
// keys for the value position.
func completeConfigKeys(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return config.SettableKeys(), cobra.ShellCompDirectiveNoFileComp
}

// primaryFlagOrder ranks a command's flags for the "<command> <tab>" hint so
// the most useful surface first (KeepOrder preserves this in the shell).
var primaryFlagOrder = []string{
	"dataset", "since", "window", "select", "group-by", "limit",
	"from", "to", "input", "field",
}

// flagHints returns a command's own (local, non-hidden) flags as ordered
// completion candidates for the "<command> <tab>" case — most useful first.
// When the command has a --dataset flag and no default dataset is configured,
// --dataset is starred and forced to the front (the single most common
// missing argument).
func flagHints(cmd *cobra.Command) ([]string, cobra.ShellCompDirective) {
	rankOf := func(name string) int {
		for i, n := range primaryFlagOrder {
			if n == name {
				return i
			}
		}
		return len(primaryFlagOrder)
	}
	type hint struct {
		name, usage string
		rank        int
	}
	var hints []hint
	hasDataset := false
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" { // --help is universal noise here
			return
		}
		if f.Name == "dataset" {
			hasDataset = true
		}
		hints = append(hints, hint{name: f.Name, usage: f.Usage, rank: rankOf(f.Name)})
	})
	if len(hints) == 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	noDefaultDataset := hasDataset && !hasDefaultDataset(cmd)
	sort.SliceStable(hints, func(i, j int) bool {
		if hints[i].rank != hints[j].rank {
			return hints[i].rank < hints[j].rank
		}
		return hints[i].name < hints[j].name
	})
	out := make([]string, 0, len(hints))
	for _, h := range hints {
		usage := h.usage
		if h.name == "dataset" && noDefaultDataset {
			usage = "★ " + usage + " (no default dataset set)"
		}
		out = append(out, "--"+h.name+"\t"+usage)
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
}

// hasDefaultDataset reports whether a default_dataset is configured (config
// only, no API call), so flagHints can nudge toward -d when it isn't.
func hasDefaultDataset(cmd *cobra.Command) bool {
	app, _, cancel, ok := completionApp(cmd)
	if !ok {
		return false
	}
	cancel()
	v, ok := app.Config.Get("default_dataset")
	return ok && v.Val != ""
}

// defaultArgComplete is the ValidArgsFunction for commands whose positional is
// free text (a query, a question) or absent: on an empty token it offers the
// command's flags (most-useful-first); otherwise it just suppresses the file
// fallback. It never offers files.
func defaultArgComplete(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if toComplete == "" {
		return flagHints(cmd)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// completeDirection completes context's --direction.
func completeDirection(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return []string{"both", "before", "after"}, cobra.ShellCompDirectiveNoFileComp
}

// treeFlagCompleters maps a flag name to a value completer applied to EVERY
// command that carries that flag, wherever it is defined — so e.g. --dataset
// completes datasets on search/tail/fields/context/send/ask/query/repl/exports
// alike, and --since completes durations everywhere. This is the single source
// for flag-value completion (the TestIntendedFlagsHaveCompletion tripwire
// keeps it honest); persistent flags (output/region/profile/fields) are
// registered once on root, and the structured filter flags are handled by name
// in applyCompletions.
func treeFlagCompleters() map[string]compFunc {
	return map[string]compFunc{
		"dataset":    completeDatasets,
		"select":     completeFields,
		"group-by":   completeFields,
		"saved":      completeKindFlag("saved-searches"),
		"since":      completeSince,
		"window":     completeSince,
		"collection": completeCollections,
		"direction":  completeDirection,
	}
}

// applyCompletions walks the command tree once (from root), wiring:
//   - value completers for every known flag (treeFlagCompleters + the
//     structured filter flags) on whichever command owns it,
//   - a default ValidArgsFunction on every runnable leaf that doesn't already
//     define its own positional completion — offering the command's flags
//     (flag hints) on an empty token instead of cobra's file fallback.
//
// Groups (commands with subcommands) are left alone so cobra keeps completing
// their subcommand names.
func applyCompletions(cmd *cobra.Command) {
	completers := treeFlagCompleters()
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if fn, ok := completers[f.Name]; ok {
				_ = c.RegisterFlagCompletionFunc(f.Name, fn)
			}
		})
		for _, name := range filterFlagNames {
			if c.LocalFlags().Lookup(name) == nil {
				continue
			}
			// Equality operators complete observed values; comparison/regex
			// operators complete only the field (the value is a threshold or
			// pattern the user types).
			fn := completeFilterField
			if name == "eq" || name == "ne" {
				fn = completeFilterFieldWithValues
			}
			_ = c.RegisterFlagCompletionFunc(name, fn)
		}
		if c.Runnable() && !c.HasSubCommands() && c.ValidArgsFunction == nil {
			c.ValidArgsFunction = defaultArgComplete
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(cmd)
}
