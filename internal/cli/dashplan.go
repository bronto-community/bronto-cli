package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// The plan is what apply will do to make the account match a manifest,
// computed from the manifest and the live state without any request, so
// --dry-run prints exactly what a real run does and the unit tests cover
// every ownership decision without a server.

// Widget actions.
const (
	actCreate    = "create"
	actUpdate    = "update"
	actUnchanged = "unchanged"
	actDelete    = "delete"
	actDetach    = "detach"
	actAttach    = "attach"
)

type widgetChange struct {
	Action string `json:"action"`
	Name   string `json:"name"`
	ID     string `json:"id,omitempty"`
	Ref    bool   `json:"ref,omitempty"`
	// Adopt: an attached widget apply didn't create gets the ownership
	// tags (export, then apply, takes ownership like kubectl apply).
	Adopt  bool `json:"adopt,omitempty"`
	Attach bool `json:"attach,omitempty"`
	// NewDefinition: the queries changed. Definitions can't be updated
	// (the API answers 405), so apply creates a new one, repoints the
	// widget, and deletes the old one.
	NewDefinition     bool     `json:"new_definition,omitempty"`
	UpdateWidget      bool     `json:"update_widget,omitempty"`
	DeleteDefinitions []string `json:"delete_definitions,omitempty"`
	Details           []string `json:"details,omitempty"`

	want *wantWidget
	live *liveWidget
}

type tagChange struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
	Op    string `json:"op"` // add, update, delete
}

type dashChange struct {
	Action  string      `json:"action"` // create, update, unchanged
	Name    string      `json:"name"`
	ID      string      `json:"id,omitempty"`
	Tags    []tagChange `json:"tags,omitempty"`
	Layout  bool        `json:"layout,omitempty"`
	Details []string    `json:"details,omitempty"`
}

type dashPlan struct {
	File      string         `json:"file"`
	Dashboard dashChange     `json:"dashboard"`
	Widgets   []widgetChange `json:"widgets"`
	// Orphans are definitions an interrupted apply created for this
	// dashboard (they carry definitionMarker) that no widget references.
	Orphans  []string `json:"orphan_definitions,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Writes   int      `json:"writes"`

	want *wantDashboard
	live *liveState
}

// computePlan diffs a resolved manifest against the live state.
func computePlan(want *wantDashboard, live *liveState) (*dashPlan, error) {
	p := &dashPlan{File: want.File, want: want, live: live}
	dash := live.Dashboard
	dashID := ""
	var attached map[string]bool
	if dash != nil {
		dashID = dash.ID
		attached = map[string]bool{}
		for _, id := range dash.WidgetIDs {
			attached[id] = true
		}
	}
	p.Dashboard = planDashboard(want, dash, p)

	byID := map[string]*liveWidget{}
	refCount := map[string]int{}
	for i := range live.Widgets {
		w := &live.Widgets[i]
		byID[w.ID] = w
		for _, m := range w.MetricIDs {
			refCount[m]++
		}
	}
	// ownedDefs are the definitions of w that apply may delete: they
	// exist, and w is the only widget referencing them.
	ownedDefs := func(w *liveWidget) []string {
		var out []string
		for _, m := range w.MetricIDs {
			if _, ok := live.Defs[m]; ok && refCount[m] == 1 {
				out = append(out, m)
			}
		}
		return out
	}

	var errs []string
	claimed := map[string]bool{}
	for i := range want.Widgets {
		ww := &want.Widgets[i]
		if ww.Ref != "" {
			w, err := matchRef(ww.Ref, live.Widgets, attached)
			if err != nil {
				errs = append(errs, ww.Pos+": "+err.Error())
				continue
			}
			claimed[w.ID] = true
			ch := widgetChange{Action: actUnchanged, Name: w.Name, ID: w.ID, Ref: true, want: ww, live: w}
			if !attached[w.ID] {
				ch.Action, ch.Attach = actAttach, true
			}
			p.Widgets = append(p.Widgets, ch)
			continue
		}

		var owned, adoptable []*liveWidget
		for j := range live.Widgets {
			w := &live.Widgets[j]
			if w.Name != ww.Name || claimed[w.ID] {
				continue
			}
			switch {
			case w.ownedBy(dashID):
				owned = append(owned, w)
			case attached[w.ID] && !w.ownedElsewhere(dashID):
				adoptable = append(adoptable, w)
			}
		}
		var match *liveWidget
		adopt := false
		switch {
		case len(owned) > 1:
			errs = append(errs, fmt.Sprintf("%s: %d widgets named %q belong to this dashboard: %s", ww.Pos, len(owned), ww.Name,
				strings.Join(widgetIDs(owned), ", "))+
				" (delete the extra ones with 'bronto widgets delete <id>')")
			continue
		case len(owned) == 1:
			match = owned[0]
		case len(adoptable) > 1:
			errs = append(errs, fmt.Sprintf("%s: %d widgets on this dashboard are named %q: %s (rename or remove the extra ones)",
				ww.Pos, len(adoptable), ww.Name, strings.Join(widgetIDs(adoptable), ", ")))
			continue
		case len(adoptable) == 1:
			match, adopt = adoptable[0], true
		}

		if match == nil {
			typ := orDefault(ww.Type, "line")
			ch := widgetChange{Action: actCreate, Name: ww.Name, NewDefinition: true, Attach: true, want: ww,
				Details: []string{fmt.Sprintf("%s, %s", typ, countPhrase(len(ww.Def.Queries), "query", "queries")+
					formulaPhrase(len(ww.Def.Formulas)))}}
			p.Widgets = append(p.Widgets, ch)
			continue
		}
		claimed[match.ID] = true
		ch := widgetChange{Action: actUnchanged, Name: ww.Name, ID: match.ID, Adopt: adopt, Attach: !attached[match.ID],
			want: ww, live: match}
		if adopt {
			ch.Details = append(ch.Details, "take ownership: it was attached but not created by apply")
		}
		if ww.Type != "" && ww.Type != match.Type {
			ch.UpdateWidget = true
			ch.Details = append(ch.Details, fmt.Sprintf("type: %s -> %s", match.Type, ww.Type))
		}
		if ww.Description != nil && *ww.Description != match.Description {
			ch.UpdateWidget = true
			ch.Details = append(ch.Details, fmt.Sprintf("description: %q -> %q", match.Description, *ww.Description))
		}
		if ww.Aux != nil && !sameJSON(ww.Aux, nonNilMap(match.Aux)) {
			ch.UpdateWidget = true
			ch.Details = append(ch.Details, fmt.Sprintf("aux: %s -> %s", compactJSON(nonNilMap(match.Aux)), compactJSON(ww.Aux)))
		}
		var cur *apiDef
		if len(match.MetricIDs) == 1 {
			if d, ok := live.Defs[match.MetricIDs[0]]; ok {
				cur = &d
			}
		}
		if cur == nil || !sameQueries(*cur, ww.Def) {
			ch.NewDefinition, ch.UpdateWidget = true, true
			ch.DeleteDefinitions = ownedDefs(match)
			switch {
			case len(match.MetricIDs) == 0:
				ch.Details = append(ch.Details, "the widget has no definition: create one")
			case len(match.MetricIDs) > 1:
				ch.Details = append(ch.Details, fmt.Sprintf("the widget plots %d definitions: replace them with one", len(match.MetricIDs)))
			case cur == nil:
				ch.Details = append(ch.Details, fmt.Sprintf("its definition %s is missing: create a new one", match.MetricIDs[0]))
			default:
				ch.Details = append(ch.Details, diffDefs(*cur, ww.Def)...)
			}
			ch.Details = append(ch.Details, "create a new definition and repoint the widget"+
				map[bool]string{true: ", then delete the old one", false: ""}[len(ch.DeleteDefinitions) > 0])
		}
		if ch.Attach {
			ch.Details = append(ch.Details, "attach it to the dashboard")
		}
		if ch.Adopt || ch.Attach || ch.UpdateWidget || ch.NewDefinition {
			ch.Action = actUpdate
		}
		p.Widgets = append(p.Widgets, ch)
	}
	if len(errs) > 0 {
		return nil, clierr.New("usage_manifest_conflict", fmt.Sprintf("%s: can't apply:\n  %s", want.File, strings.Join(errs, "\n  ")))
	}

	if dash != nil {
		// Owned widgets the manifest no longer has: delete them and their
		// definitions. Attached widgets apply doesn't own: detach only.
		seen := map[string]bool{}
		order := append(append([]string{}, dash.WidgetIDs...), sortedWidgetIDs(live.Widgets)...)
		for _, id := range order {
			w := byID[id]
			if seen[id] || claimed[id] {
				continue
			}
			seen[id] = true
			switch {
			case w != nil && w.ownedBy(dashID):
				defs := ownedDefs(w)
				p.Widgets = append(p.Widgets, widgetChange{Action: actDelete, Name: w.Name, ID: w.ID,
					DeleteDefinitions: defs, live: w,
					Details: []string{"removed from the manifest: delete it" + defPhrase(len(defs))}})
			case attached[id]:
				name := id
				if w != nil {
					name = w.Name
				}
				p.Widgets = append(p.Widgets, widgetChange{Action: actDetach, Name: name, ID: id, live: w,
					Details: []string{"not in the manifest and not owned by it: detach it (the widget is kept)"}})
			}
		}
		marker := definitionMarker(dashID)
		for id, d := range live.Defs {
			if d.Description == marker && refCount[id] == 0 {
				p.Orphans = append(p.Orphans, id)
			}
		}
		sort.Strings(p.Orphans)
	}

	p.Dashboard.Layout = layoutChanged(want, dash, p.Widgets)
	if p.Dashboard.Layout {
		p.Dashboard.Details = append(p.Dashboard.Details, fmt.Sprintf("layout: set %d widget position(s)", len(want.Layout)))
	}
	if p.Dashboard.Action == actUnchanged && (len(p.Dashboard.Tags) > 0 || p.Dashboard.Layout) {
		p.Dashboard.Action = actUpdate
	}
	p.Writes = p.plannedWrites()
	return p, nil
}

func planDashboard(want *wantDashboard, dash *liveDashboard, p *dashPlan) dashChange {
	desired := map[string]string{}
	var current map[string]string
	if dash != nil {
		current = dash.Tags
		if want.Tags == nil {
			for k, v := range dash.Tags {
				desired[k] = v
			}
		}
	}
	for k, v := range want.Tags {
		desired[k] = v
	}
	desired[tagManagedBy] = tagManagedByValue

	dc := dashChange{Action: actUnchanged, Name: want.Name}
	if dash == nil {
		dc.Action = actCreate
	} else {
		dc.ID = dash.ID
		if want.Description != nil && *want.Description != dash.Description {
			// Live (2026-10-21): PATCH /dashboards takes only name or
			// layout (a description answers 400), and PUT answers 405.
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"dashboard %q: the API can't change a dashboard's description after creation, so it stays %q",
				want.Name, dash.Description))
		}
	}
	for _, k := range sortedStringKeys(desired) {
		cur, ok := current[k]
		switch {
		case !ok:
			dc.Tags = append(dc.Tags, tagChange{Name: k, Value: desired[k], Op: "add"})
		case cur != desired[k]:
			dc.Tags = append(dc.Tags, tagChange{Name: k, Value: desired[k], Op: "update"})
		}
	}
	if want.Tags != nil {
		for _, k := range sortedStringKeys(current) {
			if _, keep := desired[k]; !keep && !isManagedTag(k) {
				dc.Tags = append(dc.Tags, tagChange{Name: k, Op: "delete"})
			}
		}
	}
	for _, t := range dc.Tags {
		switch t.Op {
		case "delete":
			dc.Details = append(dc.Details, fmt.Sprintf("tag %s: remove", t.Name))
		default:
			dc.Details = append(dc.Details, fmt.Sprintf("tag %s: %s %s", t.Name, t.Op, t.Value))
		}
	}
	return dc
}

// matchRef finds the widget a ref: names, by id or by unique name. When
// several share the name but exactly one is on this dashboard, that one.
func matchRef(ref string, widgets []liveWidget, attached map[string]bool) (*liveWidget, error) {
	var cands []*liveWidget
	for i := range widgets {
		w := &widgets[i]
		if w.ID == ref || (!uuidRe.MatchString(ref) && w.Name == ref) {
			cands = append(cands, w)
		}
	}
	if len(cands) > 1 {
		var on []*liveWidget
		for _, w := range cands {
			if attached[w.ID] {
				on = append(on, w)
			}
		}
		if len(on) == 1 {
			return on[0], nil
		}
	}
	switch len(cands) {
	case 1:
		return cands[0], nil
	case 0:
		return nil, fmt.Errorf("ref: no widget named %q (run 'bronto widgets list' to see widgets)", ref)
	}
	return nil, fmt.Errorf("ref: %d widgets are named %q: %s (use ref: <id>)", len(cands), ref, strings.Join(widgetIDs(cands), ", "))
}

// layoutChanged reports whether the manifest's layout differs from the
// dashboard's. A layout naming a widget apply is about to create always
// counts as a change; without a layout key the layout is left alone.
func layoutChanged(want *wantDashboard, dash *liveDashboard, changes []widgetChange) bool {
	if want.Layout == nil {
		return false
	}
	desired, complete := desiredLayout(want, changes)
	if dash == nil {
		return len(desired) > 0
	}
	if !complete {
		return true
	}
	return !sameLayout(desired, dash.Layout.WidgetLayouts)
}

// desiredLayout maps the manifest's layout to widget ids. complete is
// false when a widget doesn't exist yet (its id is known after creation).
func desiredLayout(want *wantDashboard, changes []widgetChange) (out []liveLayout, complete bool) {
	ids := map[string]string{}
	for _, ch := range changes {
		if ch.want == nil {
			continue
		}
		key := ch.want.Name
		if ch.want.Ref != "" {
			key = ch.want.Ref
		}
		ids[key] = ch.ID
	}
	complete = true
	for _, l := range want.Layout {
		id := ids[l.Widget]
		if id == "" {
			complete = false
		}
		out = append(out, liveLayout{ID: id, X: l.X, Y: l.Y, W: l.W, H: l.H})
	}
	return out, complete
}

func sameLayout(a, b []liveLayout) bool {
	norm := func(l []liveLayout) []liveLayout {
		out := append([]liveLayout{}, l...)
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out
	}
	return reflect.DeepEqual(norm(a), norm(b))
}

// plannedWrites counts the API writes executePlan makes for p.
func (p *dashPlan) plannedWrites() int {
	n := len(p.Dashboard.Tags) + len(p.Orphans)
	if p.Dashboard.Action == actCreate {
		n++
	}
	if p.Dashboard.Layout {
		n++
	}
	for _, ch := range p.Widgets {
		switch ch.Action {
		case actCreate:
			n += 5 // definition, widget, attach, two ownership tags
		case actUpdate:
			if ch.Adopt {
				n += len(adoptTags(ch.live, p.Dashboard.ID))
			}
			if ch.NewDefinition {
				n++
			}
			if ch.UpdateWidget {
				n++
			}
			n += len(ch.DeleteDefinitions)
			if ch.Attach {
				n++
			}
		case actAttach, actDetach:
			n++
		case actDelete:
			n += 1 + len(ch.DeleteDefinitions)
		}
	}
	return n
}

// adoptTags are the ownership tags an adopted widget is missing.
func adoptTags(w *liveWidget, dashID string) []tagChange {
	var out []tagChange
	for _, t := range []tagChange{{Name: tagManagedBy, Value: tagManagedByValue}, {Name: tagOwner, Value: dashID}} {
		cur, ok := w.Tags[t.Name]
		switch {
		case !ok:
			t.Op = "add"
			out = append(out, t)
		case cur != t.Value:
			t.Op = "update"
			out = append(out, t)
		}
	}
	return out
}

// diffDefs describes how a definition's queries and formulas changed,
// one line per changed field, for apply's output and --dry-run.
func diffDefs(old, want apiDef) []string {
	o, n := old.normalized(), want.normalized()
	var lines []string
	oq := map[string]apiQuery{}
	for _, q := range o.Queries {
		oq[q.Name] = q
	}
	nq := map[string]bool{}
	for _, q := range n.Queries {
		nq[q.Name] = true
		prev, ok := oq[q.Name]
		if !ok {
			lines = append(lines, fmt.Sprintf("query %s: add", q.Name))
			continue
		}
		lines = append(lines, fieldDiffs("query "+q.Name, queryFields(prev), queryFields(q))...)
	}
	for _, q := range o.Queries {
		if !nq[q.Name] {
			lines = append(lines, fmt.Sprintf("query %s: remove", q.Name))
		}
	}
	of := map[string]apiFormula{}
	for _, f := range o.Formulas {
		of[f.Name] = f
	}
	nf := map[string]bool{}
	for _, f := range n.Formulas {
		nf[f.Name] = true
		prev, ok := of[f.Name]
		if !ok {
			lines = append(lines, fmt.Sprintf("formula %s: add %q", f.Name, f.Expression))
			continue
		}
		lines = append(lines, fieldDiffs("formula "+f.Name, formulaFields(prev), formulaFields(f))...)
	}
	for _, f := range o.Formulas {
		if !nf[f.Name] {
			lines = append(lines, fmt.Sprintf("formula %s: remove", f.Name))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "query order changed")
	}
	return lines
}

func queryFields(q apiQuery) [][2]string {
	return [][2]string{
		{"select", compactJSON(q.Select)}, {"where", compactJSON(q.Where)}, {"group_by", compactJSON(q.Groups)},
		{"aggregation", compactJSON(q.Aggregation)}, {"from_expr", compactJSON(q.FromExpr)},
		{"from", compactJSON(q.From)}, {"unit_config", compactJSON(q.UnitConfig)},
	}
}

func formulaFields(f apiFormula) [][2]string {
	return [][2]string{{"expression", compactJSON(f.Expression)}, {"aggregation", compactJSON(f.Aggregation)},
		{"unit_config", compactJSON(f.UnitConfig)}}
}

func fieldDiffs(label string, a, b [][2]string) []string {
	var out []string
	for i := range a {
		if a[i][1] != b[i][1] {
			out = append(out, fmt.Sprintf("%s: %s: %s -> %s", label, a[i][0], a[i][1], b[i][1]))
		}
	}
	return out
}

func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func sameJSON(a, b any) bool { return compactJSON(a) == compactJSON(b) }

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func widgetIDs(ws []*liveWidget) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.ID)
	}
	return out
}

func sortedWidgetIDs(ws []liveWidget) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.ID)
	}
	sort.Strings(out)
	return out
}

func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func countPhrase(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func formulaPhrase(n int) string {
	if n == 0 {
		return ""
	}
	return ", " + countPhrase(n, "formula", "formulas")
}

func defPhrase(n int) string {
	if n == 0 {
		return ""
	}
	if n == 1 {
		return " and its definition"
	}
	return fmt.Sprintf(" and its %d definitions", n)
}
