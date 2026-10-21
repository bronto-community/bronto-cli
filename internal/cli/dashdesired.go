package cli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// The API shapes of a metric definition, as apply sends them and as it
// compares them with what the API returns. Unknown response fields (query
// ids, metric_type) are dropped on decode.

type apiAgg struct {
	Time     string   `json:"time"`
	ReduceTo string   `json:"reduce_to,omitempty"`
	Params   []string `json:"time_function_params,omitempty"`
}

type apiQuery struct {
	Name        string         `json:"name"`
	Select      []string       `json:"select"`
	Where       string         `json:"where"`
	Groups      []string       `json:"groups"`
	Aggregation []apiAgg       `json:"aggregation,omitempty"`
	FromExpr    string         `json:"from_expr,omitempty"`
	From        []string       `json:"from,omitempty"`
	UnitConfig  map[string]any `json:"unit_config,omitempty"`
}

type apiFormulaAgg struct {
	ReduceTo string `json:"reduce_to,omitempty"`
}

type apiFormula struct {
	Name        string         `json:"name"`
	Expression  string         `json:"expression"`
	Aggregation *apiFormulaAgg `json:"aggregation,omitempty"`
	UnitConfig  map[string]any `json:"unit_config,omitempty"`
}

type apiDef struct {
	ID          string       `json:"id,omitempty"`
	Name        string       `json:"name,omitempty"`
	Description string       `json:"description,omitempty"`
	Queries     []apiQuery   `json:"queries"`
	Formulas    []apiFormula `json:"formulas,omitempty"`
}

// normalized fills in what the API fills in, so a definition apply sent
// compares equal to the one the API returns: groups default to [],
// reduce_to to avg (live 2026-10-21 a formula comes back with
// aggregation {reduce_to: avg} added), and empty maps and lists are nil.
func (d apiDef) normalized() apiDef {
	out := apiDef{}
	for _, q := range d.Queries {
		if q.Groups == nil {
			q.Groups = []string{}
		}
		aggs := make([]apiAgg, 0, len(q.Aggregation))
		for _, a := range q.Aggregation {
			if a.ReduceTo == "" {
				a.ReduceTo = "avg"
			}
			if len(a.Params) == 0 {
				a.Params = nil
			}
			aggs = append(aggs, a)
		}
		q.Aggregation = aggs
		if len(q.From) == 0 {
			q.From = nil
		}
		if len(q.UnitConfig) == 0 {
			q.UnitConfig = nil
		}
		out.Queries = append(out.Queries, q)
	}
	for _, f := range d.Formulas {
		if f.Aggregation == nil || f.Aggregation.ReduceTo == "" {
			f.Aggregation = &apiFormulaAgg{ReduceTo: "avg"}
		}
		if len(f.UnitConfig) == 0 {
			f.UnitConfig = nil
		}
		out.Formulas = append(out.Formulas, f)
	}
	return out
}

// sameQueries reports whether two definitions compute the same series:
// their queries and formulas match. Name and description don't count; a
// renamed widget keeps its definition.
func sameQueries(a, b apiDef) bool {
	ja, _ := json.Marshal(a.normalized())
	jb, _ := json.Marshal(b.normalized())
	return string(ja) == string(jb)
}

// datasetLookup resolves a dataset reference (name, collection/name, or
// id) the way -d does.
type datasetLookup func(ref string) (datasetInfo, error)

// lookupIn resolves references against an already fetched dataset list.
func lookupIn(ds []datasetInfo) datasetLookup {
	return func(ref string) (datasetInfo, error) {
		if uuidRe.MatchString(ref) {
			for _, d := range ds {
				if strings.EqualFold(d.id, ref) {
					return d, nil
				}
			}
			return datasetInfo{}, clierr.New("dataset_not_found", fmt.Sprintf("no dataset with id %s", ref))
		}
		if collection, name, ok := strings.Cut(ref, "/"); ok {
			for _, d := range ds {
				if d.collection == collection && d.name == name {
					return d, nil
				}
			}
			return datasetInfo{}, clierr.New("dataset_not_found", fmt.Sprintf("no dataset %q", ref)).
				WithHint("Run 'bronto datasets list' to see collections and datasets.")
		}
		var matches []datasetInfo
		for _, d := range ds {
			if d.name == ref {
				matches = append(matches, d)
			}
		}
		switch len(matches) {
		case 1:
			return matches[0], nil
		case 0:
			return datasetInfo{}, clierr.New("dataset_not_found", fmt.Sprintf("no dataset named %q", ref)).
				WithHint("Run 'bronto datasets list' to see collections and datasets.")
		}
		q := make([]string, 0, len(matches))
		for _, m := range matches {
			q = append(q, m.qualified())
		}
		return datasetInfo{}, clierr.New("usage_ambiguous_dataset",
			fmt.Sprintf("%d datasets are named %q: %s", len(matches), ref, strings.Join(q, ", "))).
			WithHint("Qualify it as <collection>/<name>, e.g. dataset: " + q[0])
	}
}

// fromExprFor builds the from_expr for one or more datasets:
// "collection" = 'c' AND "dataset" = 'd', OR-joined in parentheses for
// several. parseFromExpr inverts it, which is how export turns a
// definition back into dataset: references.
func fromExprFor(ds []datasetInfo) (string, error) {
	parts := make([]string, 0, len(ds))
	for _, d := range ds {
		if strings.Contains(d.collection, "'") || strings.Contains(d.name, "'") {
			return "", clierr.New("usage_invalid_manifest",
				fmt.Sprintf("the dataset %s has a quote in its name; write the query's from_expr yourself", d.qualified()))
		}
		parts = append(parts, fmt.Sprintf(`"collection" = '%s' AND "dataset" = '%s'`, d.collection, d.name))
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	for i, p := range parts {
		parts[i] = "(" + p + ")"
	}
	return strings.Join(parts, " OR "), nil
}

var fromExprPairRe = regexp.MustCompile(`^"collection" = '([^']*)' AND "dataset" = '([^']*)'$`)

// parseFromExpr returns the collection/name references of a from_expr
// that fromExprFor would have built, or ok=false for any other
// expression (which export keeps as a raw from_expr).
func parseFromExpr(expr string) (refs []string, ok bool) {
	var ds []datasetInfo
	pieces := []string{expr}
	if strings.HasPrefix(expr, "(") {
		pieces = strings.Split(expr, " OR ")
		for i, p := range pieces {
			if !strings.HasPrefix(p, "(") || !strings.HasSuffix(p, ")") {
				return nil, false
			}
			pieces[i] = p[1 : len(p)-1]
		}
	}
	for _, p := range pieces {
		m := fromExprPairRe.FindStringSubmatch(p)
		if m == nil || m[1] == "" || m[2] == "" {
			return nil, false
		}
		ds = append(ds, datasetInfo{collection: m[1], name: m[2]})
	}
	if rebuilt, err := fromExprFor(ds); err != nil || rebuilt != expr {
		return nil, false
	}
	for _, d := range ds {
		refs = append(refs, d.qualified())
	}
	return refs, true
}

// wantWidget is a manifest widget resolved to what apply sends.
type wantWidget struct {
	Pos  string
	Ref  string // an existing widget's name or id; nothing else is set
	Name string
	// Type, Description and Aux are nil/"" when the manifest leaves them
	// out: apply then keeps the widget's current value (a new widget is a
	// line chart).
	Type        string
	Description *string
	Aux         map[string]any
	Def         apiDef
}

// wantDashboard is a manifest resolved for planning.
type wantDashboard struct {
	File        string
	Name        string
	Description *string
	Tags        map[string]string // nil: leave the dashboard's tags alone
	Widgets     []wantWidget
	Layout      []manLayout // nil: leave the layout alone
}

// needsDatasets reports whether resolving m needs the account's datasets.
func (m *manifest) needsDatasets() bool {
	for _, w := range m.Widgets {
		for _, q := range w.Queries {
			if len(q.Datasets) > 0 {
				return true
			}
			for _, f := range q.From {
				if !uuidRe.MatchString(f) {
					return true
				}
			}
		}
	}
	return false
}

// resolve turns a parsed manifest into the definitions and widgets apply
// wants, resolving dataset references through lookup (nil when the
// manifest has none, see needsDatasets).
func (m *manifest) resolve(lookup datasetLookup) (*wantDashboard, error) {
	want := &wantDashboard{File: m.File, Name: m.Name}
	if m.HasDesc {
		d := m.Description
		want.Description = &d
	}
	if m.HasTags {
		want.Tags = m.Tags
		if want.Tags == nil {
			want.Tags = map[string]string{}
		}
	}
	if m.HasLayout {
		want.Layout = m.Layout
		if want.Layout == nil {
			want.Layout = []manLayout{}
		}
	}
	var errs []string
	for _, w := range m.Widgets {
		if w.Ref != "" {
			want.Widgets = append(want.Widgets, wantWidget{Pos: w.Pos, Ref: w.Ref})
			continue
		}
		ww := wantWidget{Pos: w.Pos, Name: w.Name, Type: w.Type}
		if w.HasDesc {
			d := w.Description
			ww.Description = &d
		}
		if w.HasAux {
			ww.Aux = w.Aux
			if ww.Aux == nil {
				ww.Aux = map[string]any{}
			}
		}
		widgetUnit := w.UnitConfig
		if widgetUnit == nil {
			widgetUnit = unitConfigFor(w.Unit)
		}
		def := apiDef{Name: w.Name}
		for i, q := range w.Queries {
			aq, err := q.resolve(i, lookup, widgetUnit)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: widget %q: %s", q.Pos, w.Name, err.Error()))
				continue
			}
			def.Queries = append(def.Queries, aq)
		}
		for _, f := range w.Formulas {
			af := apiFormula{Name: f.Name, Expression: f.Expression,
				Aggregation: &apiFormulaAgg{ReduceTo: orDefault(f.ReduceTo, "avg")}}
			switch {
			case f.UnitConfig != nil:
				af.UnitConfig = f.UnitConfig
			case f.Unit != "":
				af.UnitConfig = unitConfigFor(f.Unit)
			default:
				af.UnitConfig = widgetUnit
			}
			def.Formulas = append(def.Formulas, af)
		}
		ww.Def = def
		want.Widgets = append(want.Widgets, ww)
	}
	if len(errs) > 0 {
		return nil, clierr.New("usage_invalid_manifest", "invalid dashboard manifest:\n  "+strings.Join(errs, "\n  "))
	}
	return want, nil
}

func (q manQuery) resolve(i int, lookup datasetLookup, widgetUnit map[string]any) (apiQuery, error) {
	aq := apiQuery{Name: orDefault(q.Name, defaultQueryName(i)), Select: []string{q.Select},
		Where: q.Where, Groups: q.GroupBy}
	if aq.Groups == nil {
		aq.Groups = []string{}
	}
	if q.Agg != "none" {
		aq.Aggregation = []apiAgg{{Time: orDefault(q.Agg, defaultAgg(q.Select)),
			ReduceTo: orDefault(q.ReduceTo, "avg"), Params: q.AggParams}}
	}
	switch {
	case len(q.Datasets) > 0:
		ds := make([]datasetInfo, 0, len(q.Datasets))
		for _, ref := range q.Datasets {
			d, err := lookup(ref)
			if err != nil {
				return aq, err
			}
			ds = append(ds, d)
		}
		expr, err := fromExprFor(ds)
		if err != nil {
			return aq, err
		}
		if len(expr) > maxFromExprLen {
			return aq, fmt.Errorf("the datasets make a from_expr of %d characters; the API takes at most %d (use fewer datasets or a shorter from_expr)",
				len(expr), maxFromExprLen)
		}
		aq.FromExpr = expr
	case q.FromExpr != "":
		aq.FromExpr = q.FromExpr
	default:
		for _, ref := range q.From {
			if uuidRe.MatchString(ref) {
				aq.From = append(aq.From, ref)
				continue
			}
			d, err := lookup(ref)
			if err != nil {
				return aq, err
			}
			aq.From = append(aq.From, d.id)
		}
	}
	switch {
	case q.UnitConfig != nil:
		aq.UnitConfig = q.UnitConfig
	case q.Unit != "":
		aq.UnitConfig = unitConfigFor(q.Unit)
	default:
		aq.UnitConfig = widgetUnit
	}
	return aq, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
