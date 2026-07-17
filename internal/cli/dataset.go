package cli

import (
	"strings"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// resolveDataset picks the dataset scope: explicit flags win, then the
// resolved default_dataset config value (an expression when it contains
// '=' or a space, a dataset UUID otherwise).
func resolveDataset(app *App, datasets []string, fromExpr string) ([]string, string, error) {
	if len(datasets) > 0 || fromExpr != "" {
		return datasets, fromExpr, nil
	}
	if v, ok := app.Config.Get("default_dataset"); ok && v.Val != "" {
		if strings.ContainsAny(v.Val, "= ") {
			return nil, v.Val, nil
		}
		return []string{v.Val}, "", nil
	}
	return nil, "", clierr.New("usage_missing_dataset", "no dataset selected").
		WithHint("Pass --dataset <uuid> or --from-expr \"...\", or set default_dataset via 'bronto config set default_dataset <uuid>'.")
}
