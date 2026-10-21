package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// A dashboard manifest is the declarative form of a dashboard that
// `bronto dashboards apply` reconciles (Kubernetes style, #195/#198): the
// dashboard's name is its identity, inline widgets and their metric
// definitions belong to it, `ref:` widgets are only attached. The manifest
// uses names instead of ids (dataset references, widget names) and the
// query fields the metric definitions API takes.
//
// The YAML is walked as a yaml.Node tree rather than decoded into structs,
// so every error carries the file:line:column it is about and unknown keys
// are reported with the keys that are allowed there.

// manifest is one parsed dashboard file.
type manifest struct {
	File        string
	Name        string
	Description string
	HasDesc     bool
	Tags        map[string]string
	HasTags     bool
	Widgets     []manWidget
	Layout      []manLayout
	HasLayout   bool
}

// manWidget is an inline widget, or a reference to an existing one (Ref).
type manWidget struct {
	Pos         string // file:line:col, for errors
	Ref         string
	Name        string
	Type        string
	Description string
	HasDesc     bool
	Aux         map[string]any
	HasAux      bool
	Unit        string
	UnitConfig  map[string]any
	Queries     []manQuery
	Formulas    []manFormula
}

// manQuery is one query of an inline widget's metric definition.
type manQuery struct {
	Pos        string
	Name       string
	Select     string
	Where      string
	Datasets   []string
	FromExpr   string
	From       []string
	GroupBy    []string
	Agg        string // "" = default from Select; "none" = no aggregation
	ReduceTo   string
	AggParams  []string
	Unit       string
	UnitConfig map[string]any
}

// manFormula combines a widget's queries by name.
type manFormula struct {
	Pos        string
	Name       string
	Expression string
	ReduceTo   string
	Unit       string
	UnitConfig map[string]any
}

// manLayout positions one widget, by its manifest name.
type manLayout struct {
	Pos        string
	Widget     string
	X, Y, W, H float64
}

// Limits from the metric definitions API (api/openapi.yaml MetricQuery,
// MetricFormula, SearchTimeseries).
const (
	maxDefinitionFormulas = 5
	maxQueryGroups        = 5
	maxFromExprLen        = 255
	maxFormulaLen         = 100
)

// widgetTypes are the API's widget types an inline widget can have
// (Widget.type in the spec, minus group, which holds widgets rather than
// queries).
var widgetTypes = []string{"area", "bar", "donut", "events-list", "geomap", "line", "pie", "score", "table", "top-list", "treemap"}

// timeFunctions are the aggregation functions (TimeFunction in the spec).
var timeFunctions = []string{"sum", "avg", "average", "min", "max", "mean", "count", "median",
	"p25", "p50", "p75", "p90", "p95", "p99", "p999", "histogram", "rate"}

var reduceTos = []string{"avg", "sum", "min", "max"}

// unitNames maps the manifest's `unit:` shorthand to a unit_config type.
var unitNames = map[string]string{
	"nanoseconds": "time", "microseconds": "time", "milliseconds": "time", "seconds": "time",
	"bytes": "data_size", "kilobytes": "data_size", "megabytes": "data_size",
	"gigabytes": "data_size", "terabytes": "data_size", "petabytes": "data_size",
}

func sortedUnitNames() []string {
	names := make([]string, 0, len(unitNames))
	for n := range unitNames {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// unitConfigFor expands a unit shorthand: nanoseconds becomes {type: time,
// input: nanoseconds, output: auto}, which the web UI renders as "800 ms".
func unitConfigFor(unit string) map[string]any {
	t, ok := unitNames[unit]
	if !ok {
		return nil
	}
	return map[string]any{"type": t, "input": unit, "output": "auto"}
}

// unitNameFor is unitConfigFor's inverse: the shorthand for a unit_config
// it could have produced, or "".
func unitNameFor(uc map[string]any) string {
	if len(uc) != 3 {
		return ""
	}
	in, _ := uc["input"].(string)
	t, _ := uc["type"].(string)
	if out, _ := uc["output"].(string); out != "auto" || unitNames[in] != t || t == "" {
		return ""
	}
	return in
}

// manifestErrors collects every problem in a file, so one validate run
// reports all of them.
type manifestErrors struct {
	file string
	list []string
}

func (e *manifestErrors) at(n *yaml.Node, format string, args ...any) {
	e.list = append(e.list, e.pos(n)+": "+fmt.Sprintf(format, args...))
}

func (e *manifestErrors) pos(n *yaml.Node) string {
	if n == nil {
		return e.file
	}
	return fmt.Sprintf("%s:%d:%d", e.file, n.Line, n.Column)
}

func (e *manifestErrors) err() error {
	if len(e.list) == 0 {
		return nil
	}
	const maxShown = 25
	// In file order: the cross-field checks run after the walk.
	sort.SliceStable(e.list, func(i, j int) bool {
		li, ci := errPos(e.list[i])
		lj, cj := errPos(e.list[j])
		return li < lj || li == lj && ci < cj
	})
	list := e.list
	if len(list) > maxShown {
		list = append(list[:maxShown:maxShown], fmt.Sprintf("… and %d more", len(e.list)-maxShown))
	}
	msg := "invalid dashboard manifest:\n  " + strings.Join(list, "\n  ")
	return clierr.New("usage_invalid_manifest", msg).
		WithHint("See 'bronto dashboards apply --help' for the manifest format.")
}

var errPosRe = regexp.MustCompile(`:(\d+):(\d+): `)

// errPos is the line and column an error message starts with.
func errPos(msg string) (int, int) {
	m := errPosRe.FindStringSubmatch(msg)
	if m == nil {
		return 0, 0
	}
	l, _ := strconv.Atoi(m[1])
	c, _ := strconv.Atoi(m[2])
	return l, c
}

// pair is one key/value of a YAML mapping.
type pair struct {
	key  string
	k, v *yaml.Node
}

// fields returns a mapping's pairs, reporting keys not in allowed (and
// duplicates). A non-mapping node is reported as such.
func (e *manifestErrors) fields(n *yaml.Node, what string, allowed ...string) []pair {
	if n.Kind != yaml.MappingNode {
		e.at(n, "%s must be a mapping (key: value pairs)", what)
		return nil
	}
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	seen := map[string]bool{}
	var out []pair
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if !ok[k.Value] {
			e.at(k, "unknown field %q in %s (allowed: %s)", k.Value, what, strings.Join(allowed, ", "))
			continue
		}
		if seen[k.Value] {
			e.at(k, "%s repeats the field %q", what, k.Value)
			continue
		}
		seen[k.Value] = true
		out = append(out, pair{key: k.Value, k: k, v: v})
	}
	return out
}

// scalar returns a scalar's text. Quoted or not, YAML's typed scalars
// (yes, 1.0) keep their literal spelling: a where clause is text.
func (e *manifestErrors) scalar(n *yaml.Node, what string) string {
	if n.Kind != yaml.ScalarNode {
		e.at(n, "%s must be a single value, not a list or mapping", what)
		return ""
	}
	if n.Tag == "!!null" {
		return ""
	}
	return n.Value
}

// list accepts one value or a list of values.
func (e *manifestErrors) values(n *yaml.Node, what string) []string {
	if n.Kind == yaml.ScalarNode {
		if n.Tag == "!!null" {
			return nil
		}
		return []string{n.Value}
	}
	if n.Kind != yaml.SequenceNode {
		e.at(n, "%s must be a value or a list of values", what)
		return nil
	}
	out := make([]string, 0, len(n.Content))
	for _, c := range n.Content {
		out = append(out, e.scalar(c, what+" entry"))
	}
	return out
}

func (e *manifestErrors) number(n *yaml.Node, what string) float64 {
	s := e.scalar(n, what)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		e.at(n, "%s must be a number, got %q", what, s)
	}
	return f
}

// anyMap decodes a free-form mapping (aux, unit_config).
func (e *manifestErrors) anyMap(n *yaml.Node, what string) map[string]any {
	if n.Kind != yaml.MappingNode {
		e.at(n, "%s must be a mapping", what)
		return nil
	}
	var m map[string]any
	if err := n.Decode(&m); err != nil {
		e.at(n, "%s: %v", what, err)
	}
	return m
}

func (e *manifestErrors) stringMap(n *yaml.Node, what string) map[string]string {
	out := map[string]string{}
	for _, p := range e.fieldsAny(n, what) {
		out[p.key] = e.scalar(p.v, what+" value")
	}
	return out
}

// fieldsAny is fields without a key whitelist.
func (e *manifestErrors) fieldsAny(n *yaml.Node, what string) []pair {
	if n.Kind != yaml.MappingNode {
		e.at(n, "%s must be a mapping (key: value pairs)", what)
		return nil
	}
	var out []pair
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, pair{key: n.Content[i].Value, k: n.Content[i], v: n.Content[i+1]})
	}
	return out
}

// parseManifest parses and validates one dashboard manifest. It does not
// talk to the API: dataset references are resolved later.
func parseManifest(file string, data []byte) (*manifest, error) {
	e := &manifestErrors{file: file}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, clierr.New("usage_invalid_manifest", file+": the file is empty")
		}
		return nil, clierr.New("usage_invalid_manifest", fmt.Sprintf("%s: not valid YAML: %v", file, err))
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		line := extra.Line
		if len(extra.Content) > 0 {
			line = extra.Content[0].Line
		}
		return nil, clierr.New("usage_invalid_manifest",
			fmt.Sprintf("%s:%d: a manifest holds one dashboard; put each dashboard in its own file", file, line))
	case !errors.Is(err, io.EOF):
		// A broken second document must not leave apply reconciling
		// only the first one.
		return nil, clierr.New("usage_invalid_manifest", fmt.Sprintf("%s: not valid YAML: %v", file, err))
	}
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	m := &manifest{File: file}
	for _, p := range e.fields(root, "the dashboard", "name", "description", "tags", "layout", "widgets") {
		switch p.key {
		case "name":
			m.Name = strings.TrimSpace(e.scalar(p.v, "name"))
		case "description":
			m.Description, m.HasDesc = e.scalar(p.v, "description"), true
		case "tags":
			m.Tags, m.HasTags = e.stringMap(p.v, "tags"), true
			for _, tp := range e.fieldsAny(p.v, "tags") {
				if isManagedTag(tp.key) {
					e.at(tp.k, "the tag %q is set by bronto dashboards apply; leave it out", tp.key)
				}
			}
		case "layout":
			m.HasLayout = true
			if p.v.Kind != yaml.SequenceNode {
				e.at(p.v, "layout must be a list of {widget, x, y, w, h}")
				continue
			}
			for _, ln := range p.v.Content {
				m.Layout = append(m.Layout, parseLayout(e, ln))
			}
		case "widgets":
			if p.v.Kind != yaml.SequenceNode {
				e.at(p.v, "widgets must be a list")
				continue
			}
			for _, wn := range p.v.Content {
				m.Widgets = append(m.Widgets, parseWidget(e, wn))
			}
		}
	}
	if m.Name == "" {
		e.at(root, "the dashboard needs a name (its identity: apply finds the dashboard by name)")
	}
	checkManifest(e, m)
	if err := e.err(); err != nil {
		return nil, err
	}
	return m, nil
}

func parseLayout(e *manifestErrors, n *yaml.Node) manLayout {
	l := manLayout{Pos: e.pos(n)}
	have := 0
	for _, p := range e.fields(n, "a layout entry", "widget", "x", "y", "w", "h") {
		have++
		switch p.key {
		case "widget":
			l.Widget = e.scalar(p.v, "widget")
		case "x":
			l.X = e.number(p.v, "x")
		case "y":
			l.Y = e.number(p.v, "y")
		case "w":
			l.W = e.number(p.v, "w")
		case "h":
			l.H = e.number(p.v, "h")
		}
	}
	if n.Kind == yaml.MappingNode && have != 5 {
		e.at(n, "a layout entry needs widget, x, y, w and h")
	}
	return l
}

func parseWidget(e *manifestErrors, n *yaml.Node) manWidget {
	w := manWidget{Pos: e.pos(n)}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "ref" {
				for _, p := range e.fields(n, "a ref widget", "ref") {
					w.Ref = strings.TrimSpace(e.scalar(p.v, "ref"))
				}
				if w.Ref == "" {
					e.at(n, "ref needs the name or id of an existing widget")
				}
				return w
			}
		}
	}
	var formulaNode, formulasNode *yaml.Node
	for _, p := range e.fields(n, "a widget", "name", "type", "description", "unit", "unit_config",
		"queries", "formula", "formulas", "aux", "ref") {
		switch p.key {
		case "name":
			w.Name = strings.TrimSpace(e.scalar(p.v, "name"))
		case "type":
			w.Type = e.scalar(p.v, "type")
		case "description":
			w.Description, w.HasDesc = e.scalar(p.v, "description"), true
		case "unit":
			w.Unit = e.scalar(p.v, "unit")
			checkUnit(e, p.v, w.Unit)
		case "unit_config":
			w.UnitConfig = e.anyMap(p.v, "unit_config")
		case "aux":
			w.Aux, w.HasAux = e.anyMap(p.v, "aux"), true
		case "queries":
			if p.v.Kind != yaml.SequenceNode {
				e.at(p.v, "queries must be a list")
				continue
			}
			for _, qn := range p.v.Content {
				w.Queries = append(w.Queries, parseQuery(e, qn))
			}
		case "formula":
			formulaNode = p.v
			expr := strings.TrimSpace(e.scalar(p.v, "formula"))
			w.Formulas = append(w.Formulas, manFormula{Pos: e.pos(p.v), Expression: expr})
		case "formulas":
			formulasNode = p.v
			if p.v.Kind != yaml.SequenceNode {
				e.at(p.v, "formulas must be a list of {name, expression}")
				continue
			}
			for _, fn := range p.v.Content {
				w.Formulas = append(w.Formulas, parseFormula(e, fn))
			}
		}
	}
	if formulaNode != nil && formulasNode != nil {
		e.at(formulasNode, "use formula (one expression) or formulas (a list), not both")
	}
	if formulaNode != nil && len(w.Formulas) == 1 {
		w.Formulas[0].Name = formulaName(w.Name, w.Queries)
	}
	if w.Unit != "" && w.UnitConfig != nil {
		e.at(n, "use unit or unit_config, not both")
	}
	return w
}

func parseQuery(e *manifestErrors, n *yaml.Node) manQuery {
	q := manQuery{Pos: e.pos(n)}
	var sources []*yaml.Node
	for _, p := range e.fields(n, "a query", "name", "dataset", "from_expr", "from", "select",
		"agg", "reduce_to", "agg_params", "where", "group_by", "unit", "unit_config") {
		switch p.key {
		case "name":
			q.Name = strings.TrimSpace(e.scalar(p.v, "name"))
		case "dataset":
			q.Datasets = e.values(p.v, "dataset")
			sources = append(sources, p.k)
		case "from_expr":
			q.FromExpr = e.scalar(p.v, "from_expr")
			sources = append(sources, p.k)
		case "from":
			q.From = e.values(p.v, "from")
			sources = append(sources, p.k)
		case "select":
			q.Select = strings.TrimSpace(e.scalar(p.v, "select"))
		case "agg":
			q.Agg = strings.ToLower(strings.TrimSpace(e.scalar(p.v, "agg")))
		case "reduce_to":
			q.ReduceTo = strings.ToLower(strings.TrimSpace(e.scalar(p.v, "reduce_to")))
		case "agg_params":
			q.AggParams = e.values(p.v, "agg_params")
		case "where":
			q.Where = e.scalar(p.v, "where")
		case "group_by":
			q.GroupBy = e.values(p.v, "group_by")
		case "unit":
			q.Unit = e.scalar(p.v, "unit")
			checkUnit(e, p.v, q.Unit)
		case "unit_config":
			q.UnitConfig = e.anyMap(p.v, "unit_config")
		}
	}
	label := "a query"
	if q.Name != "" {
		label = fmt.Sprintf("query %q", q.Name)
	}
	switch {
	case len(sources) == 0 && n.Kind == yaml.MappingNode:
		e.at(n, "%s needs a dataset (or from_expr, or from)", label)
	case len(sources) > 1:
		e.at(sources[1], "%s: use one of dataset, from_expr or from", label)
	case len(q.Datasets) == 0 && strings.TrimSpace(q.FromExpr) == "" && len(q.From) == 0 && n.Kind == yaml.MappingNode:
		// The key is there but empty (dataset: [], from_expr: ''): the
		// API would reject the definition after apply had started writing.
		e.at(sources[0], "%s: %s is empty", label, sources[0].Value)
	}
	for _, f := range q.From {
		if strings.TrimSpace(f) == "" {
			e.at(n, "%s: a from entry is empty", label)
		}
	}
	if n.Kind == yaml.MappingNode && q.Select == "" {
		e.at(n, "%s needs a select, e.g. count(*) or $span.duration_nano", label)
	}
	if q.Unit != "" && q.UnitConfig != nil {
		e.at(n, "%s: use unit or unit_config, not both", label)
	}
	if q.Agg != "" && q.Agg != "none" && !contains(timeFunctions, q.Agg) {
		e.at(n, "%s: unknown agg %q (one of %s, or none)", label, q.Agg, strings.Join(timeFunctions, ", "))
	}
	if q.ReduceTo != "" && !contains(reduceTos, q.ReduceTo) {
		e.at(n, "%s: unknown reduce_to %q (one of %s)", label, q.ReduceTo, strings.Join(reduceTos, ", "))
	}
	if q.Select != "" && q.Agg == "" && defaultAgg(q.Select) == "" {
		e.at(n, "%s: select %q is a field, so set agg to say how to aggregate it (e.g. agg: p95, avg, sum or max)", label, q.Select)
	}
	if q.Agg == "none" && (q.ReduceTo != "" || len(q.AggParams) > 0) {
		e.at(n, "%s: agg: none can't have reduce_to or agg_params", label)
	}
	if len(q.GroupBy) > maxQueryGroups {
		e.at(n, "%s: group_by takes at most %d fields, got %d", label, maxQueryGroups, len(q.GroupBy))
	}
	for _, d := range q.Datasets {
		if strings.TrimSpace(d) == "" {
			e.at(n, "%s: a dataset reference is empty", label)
		}
	}
	if len(q.FromExpr) > maxFromExprLen {
		e.at(n, "%s: from_expr is %d characters; the API takes at most %d", label, len(q.FromExpr), maxFromExprLen)
	}
	return q
}

func parseFormula(e *manifestErrors, n *yaml.Node) manFormula {
	f := manFormula{Pos: e.pos(n)}
	for _, p := range e.fields(n, "a formula", "name", "expression", "reduce_to", "unit", "unit_config") {
		switch p.key {
		case "name":
			f.Name = strings.TrimSpace(e.scalar(p.v, "name"))
		case "expression":
			f.Expression = strings.TrimSpace(e.scalar(p.v, "expression"))
		case "reduce_to":
			f.ReduceTo = strings.ToLower(strings.TrimSpace(e.scalar(p.v, "reduce_to")))
		case "unit":
			f.Unit = e.scalar(p.v, "unit")
			checkUnit(e, p.v, f.Unit)
		case "unit_config":
			f.UnitConfig = e.anyMap(p.v, "unit_config")
		}
	}
	if n.Kind == yaml.MappingNode && (f.Name == "" || f.Expression == "") {
		e.at(n, "a formula needs a name and an expression")
	}
	if f.ReduceTo != "" && !contains(reduceTos, f.ReduceTo) {
		e.at(n, "formula %q: unknown reduce_to %q (one of %s)", f.Name, f.ReduceTo, strings.Join(reduceTos, ", "))
	}
	return f
}

func checkUnit(e *manifestErrors, n *yaml.Node, unit string) {
	if _, ok := unitNames[unit]; !ok {
		e.at(n, "unknown unit %q (one of %s; or set unit_config)", unit, strings.Join(sortedUnitNames(), ", "))
	}
}

var (
	selectFuncRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)\s*\(.*\)$`)
	identRe      = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	slugRe       = regexp.MustCompile(`[^a-z0-9]+`)
)

// defaultAgg is the aggregation a select implies: count(*) counts,
// p95(x) takes the p95, sum(x) sums. A bare field has none, so the
// manifest must say. AVERAGE(x), common in Bronto's templates, is avg.
func defaultAgg(sel string) string {
	m := selectFuncRe.FindStringSubmatch(strings.TrimSpace(sel))
	if m == nil {
		return ""
	}
	fn := strings.ToLower(m[1])
	if fn == "average" {
		fn = "avg"
	}
	if !contains(timeFunctions, fn) {
		return ""
	}
	return fn
}

// formulaName names the formula `formula:` creates after the widget:
// "Error rate %" becomes error_rate. The name is the series' label in the
// web UI; it must not contain spaces or clash with a query name.
func formulaName(widget string, queries []manQuery) string {
	name := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(widget), "_"), "_")
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		name = "formula_" + name
		name = strings.TrimSuffix(name, "_")
	}
	if len(name) > maxFormulaLen {
		name = name[:maxFormulaLen]
	}
	for _, q := range queries {
		if q.Name == name {
			return name + "_formula"
		}
	}
	return name
}

// checkManifest runs the cross-field checks: unique names, query names a
// formula can use, formula references, layout entries naming widgets.
func checkManifest(e *manifestErrors, m *manifest) {
	names := map[string]bool{}
	refs := map[string]bool{}
	for _, w := range m.Widgets {
		at := func(format string, args ...any) {
			e.list = append(e.list, w.Pos+": "+fmt.Sprintf(format, args...))
		}
		if w.Ref != "" {
			if refs[w.Ref] {
				at("the widget %q is referenced twice", w.Ref)
			}
			refs[w.Ref] = true
			continue
		}
		if w.Name == "" {
			at("a widget needs a name (or ref: to attach an existing widget)")
			continue
		}
		if names[w.Name] {
			at("two widgets are named %q; apply matches widgets by name, so names must be unique", w.Name)
		}
		names[w.Name] = true
		label := fmt.Sprintf("widget %q", w.Name)
		if w.Type != "" && !contains(widgetTypes, w.Type) {
			at("%s: unknown type %q (one of %s)", label, w.Type, strings.Join(widgetTypes, ", "))
		}
		switch {
		case len(w.Queries) == 0:
			at("%s needs at least one query", label)
		case len(w.Queries) > maxDefinitionQueries:
			at("%s has %d queries; a widget takes at most %d", label, len(w.Queries), maxDefinitionQueries)
		}
		if len(w.Formulas) > maxDefinitionFormulas {
			at("%s has %d formulas; a widget takes at most %d", label, len(w.Formulas), maxDefinitionFormulas)
		}
		qnames := map[string]bool{}
		for i, q := range w.Queries {
			name := q.Name
			if name == "" {
				name = defaultQueryName(i)
			}
			if qnames[name] {
				e.list = append(e.list, fmt.Sprintf("%s: %s: two queries are named %q", q.Pos, label, name))
			}
			qnames[name] = true
			if len(w.Formulas) > 0 && strings.ContainsAny(name, " \t") {
				e.list = append(e.list, fmt.Sprintf("%s: %s: query name %q has a space, so a formula can't refer to it", q.Pos, label, name))
			}
		}
		fnames := map[string]bool{}
		for _, f := range w.Formulas {
			if f.Expression == "" {
				e.list = append(e.list, fmt.Sprintf("%s: %s: the formula is empty", f.Pos, label))
				continue
			}
			if fnames[f.Name] || qnames[f.Name] {
				e.list = append(e.list, fmt.Sprintf("%s: %s: the name %q is used twice", f.Pos, label, f.Name))
			}
			fnames[f.Name] = true
			if len(f.Expression) > maxFormulaLen {
				e.list = append(e.list, fmt.Sprintf("%s: %s: formula is %d characters; the API takes at most %d", f.Pos, label, len(f.Expression), maxFormulaLen))
			}
			for _, id := range formulaRefs(f.Expression) {
				if !qnames[id] {
					e.list = append(e.list, fmt.Sprintf("%s: %s: formula refers to %q, which isn't a query of this widget (queries: %s)",
						f.Pos, label, id, strings.Join(sortedSet(qnames), ", ")))
				}
			}
		}
	}
	if m.HasLayout {
		seen := map[string]bool{}
		for _, l := range m.Layout {
			if l.Widget == "" {
				continue
			}
			if !names[l.Widget] && !refs[l.Widget] {
				e.list = append(e.list, fmt.Sprintf("%s: layout names the widget %q, which isn't in widgets", l.Pos, l.Widget))
			}
			if seen[l.Widget] {
				e.list = append(e.list, fmt.Sprintf("%s: layout positions %q twice", l.Pos, l.Widget))
			}
			seen[l.Widget] = true
		}
	}
}

// formulaRefs lists the identifiers a formula expression uses as query
// names: words not followed by "(" (a function) and not numbers.
func formulaRefs(expr string) []string {
	var out []string
	for _, loc := range identRe.FindAllStringIndex(expr, -1) {
		rest := strings.TrimLeft(expr[loc[1]:], " \t")
		if strings.HasPrefix(rest, "(") {
			continue
		}
		if loc[0] > 0 && (expr[loc[0]-1] >= '0' && expr[loc[0]-1] <= '9' || expr[loc[0]-1] == '.') {
			continue // the exponent or fraction of a number, e.g. 1e9
		}
		out = append(out, expr[loc[0]:loc[1]])
	}
	return out
}

func defaultQueryName(i int) string { return fmt.Sprintf("q%d", i+1) }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// manifestSource is one file's bytes, named for errors.
type manifestSource struct {
	name string
	data []byte
}

// readManifestSources expands -f values: a file, a directory (its *.yaml
// and *.yml files, sorted, not recursive), or - for stdin.
func readManifestSources(stdin io.Reader, paths []string) ([]manifestSource, error) {
	if len(paths) == 0 {
		return nil, clierr.New("usage_missing_flag", "pass the manifest with -f <file>, -f <directory> or -f -")
	}
	var out []manifestSource
	for _, p := range paths {
		if p == "-" {
			data, err := io.ReadAll(stdin)
			if err != nil {
				return nil, clierr.New("usage_input_file", fmt.Sprintf("reading stdin: %v", err))
			}
			out = append(out, manifestSource{name: "<stdin>", data: data})
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, clierr.New("usage_input_file", err.Error())
		}
		if !info.IsDir() {
			data, err := os.ReadFile(p) // #nosec G304 -- the user's manifest
			if err != nil {
				return nil, clierr.New("usage_input_file", err.Error())
			}
			out = append(out, manifestSource{name: p, data: data})
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, clierr.New("usage_input_file", err.Error())
		}
		n := 0
		for _, ent := range entries {
			ext := strings.ToLower(filepath.Ext(ent.Name()))
			if ent.IsDir() || (ext != ".yaml" && ext != ".yml") {
				continue
			}
			path := filepath.Join(p, ent.Name())
			data, err := os.ReadFile(path) // #nosec G304 -- the user's manifest directory
			if err != nil {
				return nil, clierr.New("usage_input_file", err.Error())
			}
			out = append(out, manifestSource{name: path, data: data})
			n++
		}
		if n == 0 {
			return nil, clierr.New("usage_input_file", fmt.Sprintf("%s has no .yaml or .yml files", p))
		}
	}
	return out, nil
}

// parseManifests parses every source, reporting all files' errors at
// once, and refuses two files that declare the same dashboard.
func parseManifests(sources []manifestSource) ([]*manifest, error) {
	var out []*manifest
	var msgs []string
	byName := map[string]string{}
	for _, s := range sources {
		m, err := parseManifest(s.name, s.data)
		if err != nil {
			msgs = append(msgs, strings.TrimPrefix(err.Error(), "invalid dashboard manifest:\n  "))
			continue
		}
		if prev, dup := byName[m.Name]; dup {
			msgs = append(msgs, fmt.Sprintf("%s: the dashboard %q is also declared in %s", s.name, m.Name, prev))
			continue
		}
		byName[m.Name] = s.name
		out = append(out, m)
	}
	if len(msgs) > 0 {
		return nil, clierr.New("usage_invalid_manifest", "invalid dashboard manifest:\n  "+strings.Join(msgs, "\n  ")).
			WithHint("See 'bronto dashboards apply --help' for the manifest format.")
	}
	return out, nil
}
