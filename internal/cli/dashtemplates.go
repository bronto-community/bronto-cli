package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/bronto-community/bronto-cli/internal/termsafe"
)

// Dashboard templates are Bronto's shared, read-only dashboard catalog
// (GET /dashboards/templates, not in the spec). The registry generates
// `dashboards templates list|get`; this file adds `templates show`, which
// renders a template through POST /dashboards/templates/{id}/preview, and
// `dashboards create --template`, which instantiates one through POST
// /dashboards/templates/{id}/instance. Both take the same body:
// {"arguments": {"<param>": {"literal": "<value>"}}}, keyed by the
// template's `parameters`.
//
// Live-verified 2026-10-21 (EU):
//   - preview saves nothing: GET on the returned dashboard, widget and
//     definition ids is 404 (500 for definitions). It answers 400 "Missing
//     argument for key=<param>" unless every parameter has a value, so show
//     fills the ones the user didn't pass with a "{{param}}" placeholder.
//   - instance copies the template's widgets and metric definitions into
//     new objects and returns the dashboard with template: {id, arguments}.
//     It ignores a name in the body, and a dashboard linked to a template
//     can't be renamed (PATCH answers 404) until detach-from-template.
//   - deleting the dashboard leaves the copied widgets and definitions.
//
// #206 tracks reporting these upstream.

// templateParamPlaceholder is what show passes for a parameter the user
// left out: the rendered queries then show where the value goes.
func templateParamPlaceholder(key string) string { return "{{" + key + "}}" }

// dashboardTemplateDesc is the registry entry behind `dashboards templates`.
func dashboardTemplateDesc() resourceDesc {
	for _, d := range resourceRegistry {
		if d.AttachTo == "dashboards" && d.Name == "templates" {
			return d
		}
	}
	panic("dashboards templates missing from resourceRegistry")
}

// attachedResourceExtras are the hand-written subcommands of a nested
// registry resource (see newResourceCmd's extras).
func attachedResourceExtras(d resourceDesc) []*cobra.Command {
	if d.AttachTo == "dashboards" && d.Name == "templates" {
		return []*cobra.Command{newDashboardTemplateShowCmd(d)}
	}
	if d.AttachTo == "metrics" && d.Name == "definitions" {
		return []*cobra.Command{newDefinitionGetCmd(d), newDefinitionQueryCmd()}
	}
	return nil
}

// templateListRows shapes template list rows for table/csv: parameters as
// their comma-joined names, widgets as a count.
func templateListRows(rows []map[string]any, _ output.Format) []map[string]any {
	for _, row := range rows {
		if params, ok := row["parameters"].(map[string]any); ok {
			row["parameters"] = strings.Join(slices.Sorted(maps.Keys(params)), ", ")
		}
		if ws, ok := row["widgets"].([]any); ok {
			row["widgets"] = len(ws)
		}
	}
	return rows
}

// fetchDashboardTemplate resolves ref (id or unique name) to the template
// object, which carries its parameters. A name is matched in the list,
// whose rows are the full templates, so it costs one request either way.
func fetchDashboardTemplate(ctx context.Context, app *App, ref string) (map[string]any, error) {
	desc := dashboardTemplateDesc()
	if uuidRe.MatchString(ref) {
		payload, err := doJSONRequest(ctx, app, http.MethodGet, desc.idBase()+"/"+url.PathEscape(ref), nil)
		if err != nil {
			return nil, err
		}
		tmpl, _ := payload.(map[string]any)
		if tmpl == nil {
			return nil, clierr.New("api_unexpected_response", "the dashboard template response is not a JSON object")
		}
		return tmpl, nil
	}
	payload, err := doJSONRequest(ctx, app, http.MethodGet, desc.listPath(), nil)
	if err != nil {
		return nil, err
	}
	rows := rowsFromPayload(payload, desc.ListRowKeys...)
	id, err := matchResourceRef(desc, rows, ref)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if desc.rowID(row) == id {
			return row, nil
		}
	}
	return nil, clierr.New("resource_not_found", fmt.Sprintf("no dashboard template named %q", ref))
}

// templateParams returns a template's declared parameters (name -> spec).
func templateParams(tmpl map[string]any) map[string]any {
	params, _ := tmpl["parameters"].(map[string]any)
	return params
}

// parseTemplateParams parses repeated --param key=value flags. Values are
// literal strings: a template argument is always {"literal": "<string>"}.
func parseTemplateParams(raw []string) (map[string]string, error) {
	out := make(map[string]string, len(raw))
	for _, kv := range raw {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, clierr.New("usage_invalid_param", fmt.Sprintf("--param %q is not key=value", kv)).
				WithHint(`Example: --param fromExpr="\"collection\" = 'web'"`)
		}
		if _, dup := out[k]; dup {
			return nil, clierr.New("usage_invalid_param", fmt.Sprintf("--param %q is given more than once", k))
		}
		out[k] = v
	}
	return out, nil
}

// checkTemplateParams refuses --param keys the template doesn't declare
// and, when requireAll, declared parameters without a value: the API
// answers either with a bare 400.
func checkTemplateParams(tmpl map[string]any, given map[string]string, requireAll bool) error {
	declared := templateParams(tmpl)
	names := slices.Sorted(maps.Keys(declared))
	name, _ := tmpl["name"].(string)
	validKeys := "It takes no parameters."
	if len(names) > 0 {
		validKeys = "Valid keys: " + strings.Join(names, ", ") + "."
	}
	var unknown []string
	for k := range given {
		if _, ok := declared[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return clierr.New("usage_unknown_param",
			fmt.Sprintf("dashboard template %q has no parameter %s", name, strings.Join(unknown, ", "))).
			WithHint(validKeys + " See 'bronto dashboards templates show <template>'.")
	}
	if !requireAll {
		return nil
	}
	var missing []string
	for _, k := range names {
		if _, ok := given[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return clierr.New("usage_missing_param",
			fmt.Sprintf("dashboard template %q needs a value for %s", name, strings.Join(missing, ", "))).
			WithHint(missingParamHint(declared, missing))
	}
	return nil
}

// missingParamHint shows the --param flags to add, with each parameter's
// description.
func missingParamHint(declared map[string]any, missing []string) string {
	flags := make([]string, 0, len(missing))
	var notes []string
	for _, k := range missing {
		flags = append(flags, "--param "+k+"=<value>")
		spec, _ := declared[k].(map[string]any)
		if d, _ := spec["description"].(string); d != "" {
			notes = append(notes, k+": "+d)
		}
	}
	hint := "Pass " + strings.Join(flags, " ") + "."
	if len(notes) > 0 {
		hint += " (" + strings.Join(notes, "; ") + ")"
	}
	return hint
}

// templateArgumentsBody builds the preview/instance request body.
func templateArgumentsBody(args map[string]string) ([]byte, error) {
	arguments := make(map[string]any, len(args))
	for k, v := range args {
		arguments[k] = map[string]string{"literal": v}
	}
	return json.Marshal(map[string]any{"arguments": arguments})
}

func newDashboardTemplateShowCmd(desc resourceDesc) *cobra.Command {
	var params []string
	cmd := &cobra.Command{
		Use:   "show <template>",
		Short: "Show a dashboard template with its widgets and queries",
		Long: "Show a dashboard template, by id or unique name, as the dashboard it would create:\n" +
			"its parameters, each widget and each widget's queries and formulas.\n\n" +
			"The template is rendered by Bronto's preview, which saves nothing. A parameter\n" +
			"you don't pass with --param shows as a {{name}} placeholder in the queries.\n" +
			"Pass the same --param values to 'bronto dashboards create --template'.\n\n" +
			"-o json prints the preview response, with the template's id and its parameters\n" +
			"added. The widget and metric definition ids inside it are placeholders.",
		Example: "  bronto dashboards templates show \"Nginx Overview\"\n" +
			"  bronto dashboards templates show \"Nginx Overview\" --param fromExpr=\"\\\"collection\\\" = 'nginx'\"\n" +
			"  bronto dashboards templates show <id> -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeResourceRef(desc),
		RunE: func(cmd *cobra.Command, args []string) error {
			given, err := parseTemplateParams(params)
			if err != nil {
				return err
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			format, err := app.DetectFormat(false)
			if err != nil {
				return err
			}
			if format == output.FormatTable {
				if err := rejectFieldsForCustomRender(app); err != nil {
					return err
				}
			}
			tmpl, err := fetchDashboardTemplate(cmd.Context(), app, args[0])
			if err != nil {
				return err
			}
			if err := checkTemplateParams(tmpl, given, false); err != nil {
				return err
			}
			arguments := maps.Clone(given)
			for k := range templateParams(tmpl) {
				if _, ok := arguments[k]; !ok {
					arguments[k] = templateParamPlaceholder(k)
				}
			}
			body, err := templateArgumentsBody(arguments)
			if err != nil {
				return err
			}
			id, _ := tmpl["id"].(string)
			// Preview is a POST that saves nothing, so it runs under
			// --dry-run like any other read.
			dryRun := app.DryRun
			app.DryRun = false
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodPost,
				desc.idBase()+"/"+url.PathEscape(id)+"/preview", body)
			app.DryRun = dryRun
			if err != nil {
				return err
			}
			preview, _ := payload.(map[string]any)
			if preview == nil {
				return clierr.New("api_unexpected_response", "the template preview is not a JSON object")
			}
			preview["id"] = id
			if params := templateParams(tmpl); params != nil {
				preview["parameters"] = params
			} else {
				preview["parameters"] = map[string]any{}
			}
			if format == output.FormatTable {
				renderTemplateSummary(app.Stdout, preview)
				return nil
			}
			p, err := app.PrinterFor(format)
			if err != nil {
				return err
			}
			return p.PrintJSON(preview)
		},
	}
	cmd.Flags().StringArrayVar(&params, "param", nil, "template parameter as key=value, filled into the queries (repeatable)")
	return cmd
}

// safeField is m[k] as a string, made safe to print on a terminal: API
// strings (and --param values substituted into queries) could carry
// escape sequences or line breaks.
func safeField(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return termsafe.Line(s)
}

// renderTemplateSummary prints a rendered template for people: its
// parameters, then each widget with its queries and formulas. Every API
// string goes through termsafe; -o json prints the payload unchanged.
func renderTemplateSummary(w io.Writer, preview map[string]any) {
	str := safeField
	_, _ = fmt.Fprintf(w, "%s\n", str(preview, "name"))
	_, _ = fmt.Fprintf(w, "id: %s\n", str(preview, "id"))
	if d, _ := preview["description"].(string); d != "" {
		_, _ = fmt.Fprintf(w, "%s\n", termsafe.Text(d))
	}
	params, _ := preview["parameters"].(map[string]any)
	if len(params) == 0 {
		_, _ = fmt.Fprintf(w, "\nParameters: none\n")
	} else {
		_, _ = fmt.Fprintf(w, "\nParameters (pass with --param key=value):\n")
		for _, k := range slices.Sorted(maps.Keys(params)) {
			spec, _ := params[k].(map[string]any)
			line := "  " + termsafe.Line(k)
			if t := str(spec, "type"); t != "" {
				line += "  " + t
			}
			if d := str(spec, "description"); d != "" {
				line += "  " + d
			}
			_, _ = fmt.Fprintln(w, line)
		}
	}
	widgets, _ := preview["widgets"].([]any)
	_, _ = fmt.Fprintf(w, "\nWidgets (%d):\n", len(widgets))
	renderTemplateWidgets(w, widgets, "  ")
}

func renderTemplateWidgets(w io.Writer, widgets []any, indent string) {
	str := safeField
	for _, raw := range widgets {
		widget, _ := raw.(map[string]any)
		if widget == nil {
			continue
		}
		_, _ = fmt.Fprintf(w, "\n%s%s (%s)\n", indent, str(widget, "name"), str(widget, "type"))
		metrics, _ := widget["metrics"].([]any)
		for _, m := range metrics {
			def, _ := m.(map[string]any)
			if def == nil {
				continue
			}
			queries, _ := def["queries"].([]any)
			for _, q := range queries {
				query, _ := q.(map[string]any)
				if query == nil {
					continue
				}
				renderTemplateQuery(w, query, indent+"  ")
			}
			formulas, _ := def["formulas"].([]any)
			for _, f := range formulas {
				formula, _ := f.(map[string]any)
				if formula == nil {
					continue
				}
				_, _ = fmt.Fprintf(w, "%sformula %s: %s\n", indent+"  ", str(formula, "name"), str(formula, "expression"))
			}
		}
		if children, _ := widget["widgets"].([]any); len(children) > 0 {
			renderTemplateWidgets(w, children, indent+"  ")
		}
	}
}

func renderTemplateQuery(w io.Writer, query map[string]any, indent string) {
	_, _ = fmt.Fprintf(w, "%squery %s\n", indent, safeField(query, "name"))
	field := func(label, value string) {
		value = termsafe.Line(value)
		if value == "" {
			value = "(none)"
		}
		_, _ = fmt.Fprintf(w, "%s  %-10s %s\n", indent, label+":", value)
	}
	field("select", joinAny(query["select"]))
	where, _ := query["where"].(string)
	field("where", where)
	if fe, _ := query["from_expr"].(string); fe != "" {
		field("from_expr", fe)
	} else {
		field("from", joinAny(query["from"]))
	}
	field("groups", joinAny(query["groups"]))
}

// joinAny joins a JSON array's elements with ", " (strings verbatim, other
// values as JSON); a non-array prints as its string or JSON form.
func joinAny(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, joinAny(e))
		}
		return strings.Join(parts, ", ")
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// newDashboardCreateCmd is the generic `dashboards create` plus
// --template/--param, which instantiate a dashboard template instead of
// sending a body. Without --template it is the generic create, unchanged.
func newDashboardCreateCmd(desc resourceDesc) *cobra.Command {
	cmd := newResourceCreateCmd(desc)
	generic := cmd.RunE
	var template string
	var params []string
	cmd.Long = "Create a dashboard from a request body (-f fields or --input), or from one of\n" +
		"Bronto's dashboard templates with --template.\n\n" +
		"With --template (an id or unique name, see 'bronto dashboards templates list'),\n" +
		"Bronto copies the template's widgets and metric definitions into new objects\n" +
		"and links the dashboard to the template. Pass a value for each of the template's\n" +
		"parameters with --param key=value ('bronto dashboards templates show' lists them).\n" +
		"The dashboard takes the template's name: the API has no name override, and a\n" +
		"dashboard linked to a template can't be renamed until you run\n" +
		"'bronto dashboards detach-from-template'. Deleting the dashboard later leaves\n" +
		"the copied widgets and metric definitions in place."
	cmd.Example += "\n  bronto dashboards create --template \"Nginx Overview\" --param fromExpr=\"\\\"collection\\\" = 'nginx'\""
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if template == "" {
			if len(params) > 0 {
				return clierr.New("usage_invalid_flags", "--param needs --template").
					WithHint("Example: bronto dashboards create --template <template> --param key=value")
			}
			return generic(cmd, args)
		}
		if cmd.Flags().Changed("field") || cmd.Flags().Changed("input") {
			return clierr.New("usage_conflicting_flags",
				"--template takes no -f or --input: the dashboard is built from the template").
				WithHint("The dashboard takes the template's name. To rename it, run " +
					"'bronto dashboards detach-from-template <dashboard>' and then " +
					"'bronto dashboards update <dashboard> -f name=<name>'.")
		}
		given, err := parseTemplateParams(params)
		if err != nil {
			return err
		}
		app, err := NewApp(cmd)
		if err != nil {
			return err
		}
		tmpl, err := fetchDashboardTemplate(cmd.Context(), app, template)
		if err != nil {
			return err
		}
		if err := checkTemplateParams(tmpl, given, true); err != nil {
			return err
		}
		body, err := templateArgumentsBody(given)
		if err != nil {
			return err
		}
		id, _ := tmpl["id"].(string)
		payload, err := doJSONRequest(cmd.Context(), app, http.MethodPost,
			dashboardTemplateDesc().idBase()+"/"+url.PathEscape(id)+"/instance", body)
		if err != nil {
			return err
		}
		p, err := app.Printer(false)
		if err != nil {
			return err
		}
		if err := p.PrintJSON(payload); err != nil {
			return err
		}
		if dash, ok := payload.(map[string]any); ok && !isDryRunPlan(payload) {
			widgets, _ := dash["widget_ids"].([]any)
			name, _ := tmpl["name"].(string)
			app.Notef("Created dashboard %v from template %q with %d widgets.\n", dash["id"], name, len(widgets))
		}
		return nil
	}
	cmd.Flags().StringVar(&template, "template", "", "create the dashboard from this dashboard template (id or unique name)")
	cmd.Flags().StringArrayVar(&params, "param", nil, "with --template: template parameter as key=value (repeatable)")
	return cmd
}
