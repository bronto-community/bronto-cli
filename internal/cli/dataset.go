package cli

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// uuidRe matches a canonical UUID. --dataset values that look like one are
// used as log ids directly; anything else is resolved as a dataset NAME —
// nobody should have to copy UUIDs around for an interactive query.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type datasetInfo struct {
	name       string
	collection string
	id         string
}

// qualified renders the collection/name form used to disambiguate
// duplicate dataset names across collections.
func (d datasetInfo) qualified() string {
	return d.collection + "/" + d.name
}

// listDatasets fetches the account's datasets via GET /logs (the same
// endpoint `bronto datasets list` uses), sorted by name.
func listDatasets(ctx context.Context, app *App) ([]datasetInfo, error) {
	payload, err := doJSONRequest(ctx, app, http.MethodGet, "/logs", nil)
	if err != nil {
		return nil, err
	}
	var out []datasetInfo
	for _, row := range rowsFromPayload(payload, "logs") {
		name, _ := row["log"].(string)
		collection, _ := row["collection"].(string)
		id, _ := row["log_id"].(string)
		if id != "" {
			out = append(out, datasetInfo{name: name, collection: collection, id: id})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].name != out[j].name {
			return out[i].name < out[j].name
		}
		return out[i].collection < out[j].collection
	})
	return out, nil
}

// datasetNames renders a readable, capped name list for hints. Names
// that appear in more than one collection are shown qualified
// (collection/name) — a pick-list containing "docs-analytics" twice
// tells the user nothing.
func datasetNames(ds []datasetInfo) string {
	counts := map[string]int{}
	for _, d := range ds {
		counts[d.name]++
	}
	names := make([]string, 0, len(ds))
	for _, d := range ds {
		switch {
		case d.name == "":
		case counts[d.name] > 1:
			names = append(names, d.qualified())
		default:
			names = append(names, d.name)
		}
	}
	const maxShown = 15
	if len(names) > maxShown {
		names = append(names[:maxShown], fmt.Sprintf("… +%d more", len(names)-maxShown))
	}
	return strings.Join(names, ", ")
}

// resolveDatasetRef turns one --dataset value into a log id: UUIDs pass
// through untouched; "collection/name" matches exactly one dataset in
// that collection; a bare name matches when it is unique across
// collections, and otherwise errors with the qualified candidates so the
// user learns the qualification syntax at the moment it is needed.
func resolveDatasetRef(ctx context.Context, app *App, ref string) (string, error) {
	if uuidRe.MatchString(ref) {
		return ref, nil
	}
	ds, err := listDatasets(ctx, app)
	if err != nil {
		return "", err
	}

	if collection, name, isQualified := strings.Cut(ref, "/"); isQualified {
		var qualifiedNames []string
		for _, d := range ds {
			if d.collection == collection && d.name == name {
				return d.id, nil
			}
			qualifiedNames = append(qualifiedNames, d.qualified())
		}
		return "", clierr.New("dataset_not_found", fmt.Sprintf("no dataset %q", ref)).
			WithHint(fmt.Sprintf("Use <collection>/<name>. This account has: %s.", capList(qualifiedNames)))
	}

	var matches []datasetInfo
	for _, d := range ds {
		if d.name == ref {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].id, nil
	case 0:
		hint := "Run 'bronto datasets list' to see all datasets."
		if len(ds) > 0 {
			hint = fmt.Sprintf("This account has: %s. Run 'bronto datasets list' for details.", datasetNames(ds))
		}
		return "", clierr.New("dataset_not_found", fmt.Sprintf("no dataset named %q", ref)).WithHint(hint)
	default:
		qualified := make([]string, 0, len(matches))
		for _, m := range matches {
			qualified = append(qualified, m.qualified())
		}
		return "", clierr.New("usage_ambiguous_dataset",
			fmt.Sprintf("%d datasets are named %q: %s", len(matches), ref, strings.Join(qualified, ", "))).
			WithHint(fmt.Sprintf("Qualify it as <collection>/<name> — e.g. -d %s — or pass the UUID from 'bronto datasets list'.", qualified[0]))
	}
}

// capList joins names, capped like datasetNames.
func capList(names []string) string {
	const maxShown = 15
	if len(names) > maxShown {
		names = append(names[:maxShown], fmt.Sprintf("… +%d more", len(names)-maxShown))
	}
	return strings.Join(names, ", ")
}

// resolveDataset picks the dataset scope for search/tail: explicit flags
// win (names resolved to log ids), then the default_dataset config value
// (an expression when it contains '=' or a space, else a name or UUID).
// With nothing selected, the account itself decides: a single dataset is
// used automatically; several become a pick-list error naming them all.
func resolveDataset(ctx context.Context, app *App, datasets []string, fromExpr string) ([]string, string, error) {
	if len(datasets) > 0 || fromExpr != "" {
		ids := make([]string, 0, len(datasets))
		for _, d := range datasets {
			id, err := resolveDatasetRef(ctx, app, d)
			if err != nil {
				return nil, "", err
			}
			ids = append(ids, id)
		}
		return ids, fromExpr, nil
	}
	if v, ok := app.Config.Get("default_dataset"); ok && v.Val != "" {
		if strings.ContainsAny(v.Val, "= ") {
			return nil, v.Val, nil
		}
		id, err := resolveDatasetRef(ctx, app, v.Val)
		if err != nil {
			return nil, "", err
		}
		return []string{id}, "", nil
	}
	ds, err := listDatasets(ctx, app)
	if err != nil {
		return nil, "", err
	}
	switch len(ds) {
	case 0:
		return nil, "", clierr.New("usage_missing_dataset", "this account has no datasets yet").
			WithHint("Ingest something first — e.g. echo hello | bronto send -d my-app — and the dataset is created automatically.")
	case 1:
		// Auto-pick, with a note in human mode only (machine-mode stderr is
		// reserved for the JSON error envelope).
		if stdoutIsTTY() {
			_, _ = fmt.Fprintf(app.Stderr, "Using dataset %q — the only dataset in this account.\n", ds[0].name)
		}
		return []string{ds[0].id}, "", nil
	default:
		return nil, "", clierr.New("usage_missing_dataset", "no dataset selected").
			WithHint(fmt.Sprintf("Pick one with -d <name>: this account has %s. Or set a default: bronto config set default_dataset <name>.", datasetNames(ds)))
	}
}
