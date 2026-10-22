package cli

import (
	"fmt"
	"strings"
)

// The manifest export writes, in apply's format. Field order is the
// order a reader expects; empty fields are left out, and apply reads a
// left-out field as its default (where "", agg from the select, formula
// name value) or as "keep the current value" (description, aux, sort), so
// the export of a dashboard re-applies with zero writes.

type exManifest struct {
	Name        string            `yaml:"name" json:"name"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Tags        map[string]string `yaml:"tags,omitempty" json:"tags,omitempty"`
	Widgets     []exWidget        `yaml:"widgets" json:"widgets"`
	Layout      []exLayout        `yaml:"layout,omitempty" json:"layout,omitempty"`
}

type exLayout struct {
	Widget string  `yaml:"widget" json:"widget"`
	X      float64 `yaml:"x" json:"x"`
	Y      float64 `yaml:"y" json:"y"`
	W      float64 `yaml:"w" json:"w"`
	H      float64 `yaml:"h" json:"h"`
}

type exWidget struct {
	Ref         string         `yaml:"ref,omitempty" json:"ref,omitempty"`
	Name        string         `yaml:"name,omitempty" json:"name,omitempty"`
	Type        string         `yaml:"type,omitempty" json:"type,omitempty"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty"`
	Unit        string         `yaml:"unit,omitempty" json:"unit,omitempty"`
	UnitConfig  map[string]any `yaml:"unit_config,omitempty" json:"unit_config,omitempty"`
	Queries     []exQuery      `yaml:"queries,omitempty" json:"queries,omitempty"`
	Formula     any            `yaml:"formula,omitempty" json:"formula,omitempty"` // an expression, or an exFormula
	Formulas    []exFormula    `yaml:"formulas,omitempty" json:"formulas,omitempty"`
	Sort        string         `yaml:"sort,omitempty" json:"sort,omitempty"`
	Aux         map[string]any `yaml:"aux,omitempty" json:"aux,omitempty"`
}

type exQuery struct {
	Name       string         `yaml:"name" json:"name"`
	Dataset    any            `yaml:"dataset,omitempty" json:"dataset,omitempty"`
	FromExpr   string         `yaml:"from_expr,omitempty" json:"from_expr,omitempty"`
	From       []string       `yaml:"from,omitempty" json:"from,omitempty"`
	Select     string         `yaml:"select" json:"select"`
	Agg        string         `yaml:"agg,omitempty" json:"agg,omitempty"`
	AggParams  []string       `yaml:"agg_params,omitempty" json:"agg_params,omitempty"`
	Where      string         `yaml:"where,omitempty" json:"where,omitempty"`
	GroupBy    []string       `yaml:"group_by,omitempty" json:"group_by,omitempty"`
	Unit       string         `yaml:"unit,omitempty" json:"unit,omitempty"`
	UnitConfig map[string]any `yaml:"unit_config,omitempty" json:"unit_config,omitempty"`
}

type exFormula struct {
	Name       string         `yaml:"name" json:"name"`
	Expression string         `yaml:"expression" json:"expression"`
	Unit       string         `yaml:"unit,omitempty" json:"unit,omitempty"`
	UnitConfig map[string]any `yaml:"unit_config,omitempty" json:"unit_config,omitempty"`
}

// exportManifest builds the manifest for live.Dashboard. notes are the
// stderr lines about widgets exported as refs or about to be adopted.
func exportManifest(live *liveState) (exManifest, []string) {
	dash := live.Dashboard
	m := exManifest{Name: dash.Name, Description: dash.Description, Widgets: []exWidget{}}
	for k, v := range dash.Tags {
		if isManagedTag(k) {
			continue
		}
		if m.Tags == nil {
			m.Tags = map[string]string{}
		}
		m.Tags[k] = v
	}
	byID := map[string]*liveWidget{}
	for i := range live.Widgets {
		byID[live.Widgets[i].ID] = &live.Widgets[i]
	}
	// Names are counted among the attached widgets: apply matches inline
	// widgets and resolves a ref's name there first (matchRef).
	names := map[string]int{}
	for _, id := range dash.WidgetIDs {
		if w := byID[id]; w != nil {
			names[w.Name]++
		}
	}
	var notes []string
	var adopted []string
	layoutKey := map[string]string{}
	for _, id := range dash.WidgetIDs {
		w := byID[id]
		if w == nil {
			notes = append(notes, fmt.Sprintf("widget %s is attached but not in the widgets list; left out", id))
			continue
		}
		ex, reason := exportWidget(w, live.Defs)
		switch {
		case reason != "":
		case w.ownedElsewhere(dash.ID):
			reason = "it belongs to another dashboard's manifest"
		case names[w.Name] > 1:
			reason = fmt.Sprintf("%d widgets on the dashboard are named %q (an inline widget's name must be unique)", names[w.Name], w.Name)
		}
		if reason != "" {
			ref := w.Name
			if names[w.Name] > 1 || w.Name == "" {
				ref = w.ID
			}
			notes = append(notes, fmt.Sprintf("widget %q is exported as ref: %s, so apply attaches it but doesn't manage it", w.Name, reason))
			m.Widgets = append(m.Widgets, exWidget{Ref: ref})
			layoutKey[id] = ref
			continue
		}
		if !w.ownedBy(dash.ID) {
			adopted = append(adopted, w.Name)
		}
		m.Widgets = append(m.Widgets, ex)
		layoutKey[id] = w.Name
	}
	if len(adopted) > 0 {
		notes = append(notes, fmt.Sprintf("%s weren't created by bronto dashboards apply (%s). They are exported inline, "+
			"so applying this manifest takes ownership of them and their definitions: a later apply without them deletes them.",
			countPhrase(len(adopted), "widget", "widgets"), strings.Join(quoteAll(adopted), ", ")))
	}
	for _, l := range dash.Layout.WidgetLayouts {
		key, ok := layoutKey[l.ID]
		if !ok {
			notes = append(notes, fmt.Sprintf("the layout positions widget %s, which isn't on the dashboard; left out", l.ID))
			continue
		}
		m.Layout = append(m.Layout, exLayout{Widget: key, X: l.X, Y: l.Y, W: l.W, H: l.H})
	}
	return m, notes
}

// exportWidget writes w inline, or returns why it can only be a ref: a
// group widget, several (or no) definitions, a missing definition, or a
// query the manifest can't express.
func exportWidget(w *liveWidget, defs map[string]apiDef) (exWidget, string) {
	switch {
	case len(w.WidgetIDs) > 0:
		return exWidget{}, "it is a group widget"
	case len(w.MetricIDs) != 1:
		return exWidget{}, fmt.Sprintf("it plots %d definitions (an inline widget has one)", len(w.MetricIDs))
	}
	def, ok := defs[w.MetricIDs[0]]
	if !ok {
		return exWidget{}, fmt.Sprintf("its definition %s doesn't exist", w.MetricIDs[0])
	}
	def = def.normalized()
	ex := exWidget{Name: w.Name, Type: w.Type, Description: w.Description}
	ex.Aux = w.Aux
	if s, ok := sortFromAux(w.Aux); ok && w.Type == "top-list" {
		ex.Sort = s.String()
		ex.Aux = map[string]any{}
		for k, v := range w.Aux {
			if k != auxSortBy && k != auxSortDirection {
				ex.Aux[k] = v
			}
		}
	}
	if len(ex.Aux) == 0 {
		ex.Aux = nil
	}
	if len(def.Queries) == 0 {
		return exWidget{}, "its definition has no queries"
	}

	// One unit for the whole widget when every query and formula has the
	// same unit_config; otherwise each keeps its own.
	var ucs []map[string]any
	for _, q := range def.Queries {
		ucs = append(ucs, q.UnitConfig)
	}
	for _, f := range def.Formulas {
		ucs = append(ucs, f.UnitConfig)
	}
	shared := ucs[0] != nil
	for _, uc := range ucs[1:] {
		if !sameJSON(uc, ucs[0]) {
			shared = false
		}
	}
	unitOf := func(uc map[string]any) (string, map[string]any) {
		if uc == nil || shared {
			return "", nil
		}
		if n := unitNameFor(uc); n != "" {
			return n, nil
		}
		return "", uc
	}
	if shared {
		if n := unitNameFor(ucs[0]); n != "" {
			ex.Unit = n
		} else {
			ex.UnitConfig = ucs[0]
		}
	}

	var mqs []manQuery
	qnames := map[string]bool{}
	for _, q := range def.Queries {
		if !expressible(q, len(def.Formulas) > 0) || qnames[q.Name] {
			return exWidget{}, fmt.Sprintf("its query %q can't be written as a manifest query", q.Name)
		}
		qnames[q.Name] = true
		eq := exQuery{Name: q.Name, Select: q.Select[0], Where: q.Where, GroupBy: q.Groups, From: q.From}
		if len(q.Aggregation) == 1 {
			a := q.Aggregation[0]
			if a.Time != defaultAgg(eq.Select) {
				eq.Agg = a.Time
			}
			eq.AggParams = a.Params
		}
		if q.FromExpr != "" {
			if refs, ok := parseFromExpr(q.FromExpr); ok {
				if len(refs) == 1 {
					eq.Dataset = refs[0]
				} else {
					eq.Dataset = refs
				}
			} else {
				eq.FromExpr = q.FromExpr
			}
		}
		if len(eq.GroupBy) == 0 {
			eq.GroupBy = nil
		}
		eq.Unit, eq.UnitConfig = unitOf(q.UnitConfig)
		ex.Queries = append(ex.Queries, eq)
		mqs = append(mqs, manQuery{Name: q.Name})
	}
	for _, f := range def.Formulas {
		for _, id := range formulaRefs(f.Expression) {
			if !qnames[id] {
				return exWidget{}, fmt.Sprintf("its formula %q refers to %q, which isn't one of its queries", f.Name, id)
			}
		}
		if f.Name == "" || qnames[f.Name] || (f.Aggregation != nil && !contains(reduceTos, f.Aggregation.ReduceTo)) {
			return exWidget{}, fmt.Sprintf("its formula %q can't be written as a manifest formula", f.Name)
		}
		ef := exFormula{Name: f.Name, Expression: f.Expression}
		ef.Unit, ef.UnitConfig = unitOf(f.UnitConfig)
		ex.Formulas = append(ex.Formulas, ef)
	}
	// One formula is written as formula:, as its expression alone when
	// apply would give it the same name.
	if len(ex.Formulas) == 1 {
		f := ex.Formulas[0]
		if f.Name == formulaName(mqs) && f.Unit == "" && f.UnitConfig == nil {
			ex.Formula = f.Expression
		} else {
			ex.Formula = f
		}
		ex.Formulas = nil
	}
	return ex, ""
}

// expressible reports whether a manifest query can say what q does: one
// select, at most one aggregation the manifest accepts, a name a formula
// can use, and a source.
func expressible(q apiQuery, hasFormulas bool) bool {
	if len(q.Select) != 1 || strings.TrimSpace(q.Select[0]) == "" || len(q.Aggregation) > 1 || q.Name == "" {
		return false
	}
	if hasFormulas && strings.ContainsAny(q.Name, " \t") {
		return false
	}
	if q.FromExpr == "" && len(q.From) == 0 || q.FromExpr != "" && len(q.From) > 0 {
		return false
	}
	if len(q.Groups) > maxQueryGroups {
		return false
	}
	if len(q.Aggregation) == 1 {
		a := q.Aggregation[0]
		fn := defaultAgg(q.Select[0])
		return contains(timeFunctions, a.Time) && contains(reduceTos, a.ReduceTo) && (fn == "" || fn == a.Time) &&
			(len(a.Params) == 0 || contains(paramAggs, a.Time))
	}
	return defaultAgg(q.Select[0]) != "" // apply aggregates with the select's function
}

func quoteAll(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, fmt.Sprintf("%q", s))
	}
	return out
}
