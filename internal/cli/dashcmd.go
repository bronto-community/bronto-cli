package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

const manifestFormatHelp = `Manifest format (YAML or JSON, one dashboard per file):

  name: Checkout API health          # required: the dashboard's identity
  description: Requests, latency and errors   # set when the dashboard is created
  tags: {team: checkout}             # optional; when present, the dashboard's full tag set
  widgets:
    - name: Request rate             # required, unique in the file
      type: line                     # line (default), bar, score, top-list, area, table, ...
      queries:
        - name: requests             # default q1, q2, ...; formulas refer to it
          dataset: .traces/checkout-api   # name, collection/name or id; or a list
          select: count(*)
          where: "$span.kind = 'SPAN_KIND_SERVER'"   # default "" (everything)
    - name: p95 latency
      unit: nanoseconds              # nanoseconds|microseconds|milliseconds|seconds|bytes|...
      queries:
        - {name: p95, dataset: .traces/checkout-api, select: $span.duration_nano, agg: p95,
           where: "$span.kind = 'SPAN_KIND_SERVER'", group_by: [$span.name]}
    - name: Error rate %
      queries:
        - {name: errors, dataset: .traces/checkout-api, select: count(*),
           where: "$span.status_code = 'STATUS_CODE_ERROR'"}
        - {name: total, dataset: .traces/checkout-api, select: count(*)}
      formula: errors / total * 100  # one formula over the queries (the widget plots it)
    - ref: Shared widget             # an existing widget, by name or id: attached, never owned

Query fields: name, dataset | from_expr | from (dataset ids), select (count(*),
p95(field), or a field with agg), agg (count, sum, avg, min, max, p50, p75, p90,
p95, p99, rate, ...; default from the select, "none" for no aggregation),
reduce_to (avg (default), sum, min, max), agg_params, where, group_by (up to 5),
unit or unit_config. A widget takes up to 6 queries, and formula: or formulas:
[{name, expression}]. Trace fields: $service.name, $span.name,
$span.kind = 'SPAN_KIND_SERVER', $span.duration_nano (nanoseconds),
$span.status_code = 'STATUS_CODE_ERROR'.

A field you leave out (description, tags, layout, a widget's type,
description or aux) is left as it is. Without a layout the web UI lays the
widgets out in two columns, ordered by widget id; a layout: list of
{widget, x, y, w, h} entries sets positions.`

func newDashboardApplyCmd() *cobra.Command {
	var files []string
	cmd := &cobra.Command{
		Use:   "apply -f <file|dir|->",
		Short: "Create or update dashboards from manifest files",
		Long: `Makes the account match dashboard manifests, like kubectl apply: creates
the dashboard if no dashboard has the manifest's name, then creates, updates
or deletes its widgets and their metric definitions until they match the
file. Run it again with an unchanged file and it makes no API writes.

How apply decides:
  - The dashboard's name is its identity. Two dashboards with the name is an
    error that lists both; apply never deletes a dashboard.
  - Inline widgets, and the metric definition each one plots, belong to the
    dashboard. Apply tags the widgets it creates (managed-by: bronto-cli,
    bronto-cli-owner: <dashboard id>) and matches them by name. Remove a
    widget from the file and the next apply deletes it and its definition.
  - A changed query gets a new definition (the API can't update one): apply
    creates it, points the widget at it, then deletes the old one.
  - ref: widgets are attached and never changed or deleted. A widget on the
    dashboard that the file doesn't mention, and that apply doesn't own, is
    detached.
  - An inline widget that's on the dashboard but wasn't created by apply
    (export a dashboard built in the web UI, then apply it) is adopted:
    apply tags it and manages it from then on.
  - A dashboard made from a template can't be changed until it's detached
    from the template, so apply detaches it first when it has a change to
    make (the plan says so), and leaves it linked otherwise.

--dry-run prints what apply would create, change, detach and delete, and
writes nothing. -f takes a file, a directory (every .yaml and .yml file in
it) or - for stdin, and can be repeated.

` + manifestFormatHelp,
		Example: "  bronto dashboards apply -f dashboard.yaml --dry-run\n" +
			"  bronto dashboards apply -f dashboard.yaml\n" +
			"  bronto dashboards apply -f dashboards/\n" +
			"  bronto dashboards export \"Checkout API health\" > dashboard.yaml",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sources, err := readManifestSources(cmd.InOrStdin(), files)
			if err != nil {
				return err
			}
			manifests, err := parseManifests(sources)
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
			p, err := app.PrinterFor(format)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			lookup, err := datasetLookupFor(ctx, app, manifests)
			if err != nil {
				return err
			}
			total := 0
			for _, m := range manifests {
				plan, err := planManifest(ctx, app, m, lookup)
				if err != nil {
					return err
				}
				for _, w := range plan.Warnings {
					_, _ = fmt.Fprintf(app.Stderr, "Warning: %s\n", w)
				}
				applied := 0
				if !app.DryRun && plan.Writes > 0 {
					var id string
					id, applied, err = executePlan(ctx, app, plan)
					plan.Dashboard.ID = id
					if err != nil {
						return applyError(err, m.File, applied)
					}
					plan.Writes = applied
				}
				total += plan.Writes
				if err := printPlan(app, p, format, plan); err != nil {
					return err
				}
			}
			switch {
			case app.DryRun:
				app.Notef("Dry run: apply would make %s; nothing was changed.\n", countPhrase(total, "API write", "API writes"))
			case total == 0:
				app.Notef("No changes.\n")
			default:
				app.Notef("Applied: %s.\n", countPhrase(total, "API write", "API writes"))
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&files, "filename", "f", nil, "manifest file, directory of .yaml/.yml files, or - for stdin (repeatable)")
	return cmd
}

// applyError says how far a failed apply got; running it again finishes
// the job (see executePlan's ordering).
func applyError(err error, file string, writes int) error {
	var ce *clierr.Error
	if errors.As(err, &ce) && writes > 1 {
		cp := *ce
		cp.Message = fmt.Sprintf("%s: apply stopped after %d writes: %s", file, writes-1, ce.Message)
		if cp.Hint == "" {
			cp.Hint = "Fix the cause and run the same apply again: it picks up where this one stopped."
		}
		return &cp
	}
	return err
}

// datasetLookupFor lists the account's datasets once, when any manifest
// names a dataset.
func datasetLookupFor(ctx context.Context, app *App, manifests []*manifest) (datasetLookup, error) {
	for _, m := range manifests {
		if m.needsDatasets() {
			ds, err := listDatasets(ctx, app)
			if err != nil {
				return nil, err
			}
			return lookupIn(ds), nil
		}
	}
	return nil, nil
}

// planManifest resolves m and diffs it against the account.
func planManifest(ctx context.Context, app *App, m *manifest, lookup datasetLookup) (*dashPlan, error) {
	want, err := m.resolve(lookup)
	if err != nil {
		return nil, err
	}
	dash, err := findDashboardByName(ctx, app, m.Name)
	if err != nil {
		return nil, err
	}
	live := &liveState{Dashboard: dash}
	if err := fetchWidgetsAndDefs(ctx, app, live); err != nil {
		return nil, err
	}
	return computePlan(want, live)
}

var actionMarks = map[string]string{
	actCreate: "+", actAttach: "+", actUpdate: "~", actUnchanged: "=", actDelete: "-", actDetach: "-",
}

// printPlan prints one dashboard's plan: a JSON object for -o json/jsonl,
// otherwise one line per widget with its changes indented below.
func printPlan(app *App, p *output.Printer, format output.Format, plan *dashPlan) error {
	if format == output.FormatJSON || format == output.FormatJSONL {
		doc := map[string]any{"file": plan.File, "dry_run": app.DryRun, "dashboard": plan.Dashboard,
			"widgets": plan.Widgets, "writes": plan.Writes}
		if len(plan.Orphans) > 0 {
			doc["orphan_definitions"] = plan.Orphans
		}
		if len(plan.Warnings) > 0 {
			doc["warnings"] = plan.Warnings
		}
		return p.PrintJSON(doc)
	}
	var b strings.Builder
	d := plan.Dashboard
	head := fmt.Sprintf("dashboard %q: %s", d.Name, d.Action)
	if d.ID != "" {
		head += " (" + d.ID + ")"
	}
	b.WriteString(head + "\n")
	for _, line := range d.Details {
		b.WriteString("    " + line + "\n")
	}
	for _, w := range plan.Widgets {
		name := fmt.Sprintf("%q", w.Name)
		if w.Ref {
			name += " (ref)"
		}
		fmt.Fprintf(&b, "  %s widget %s: %s\n", actionMarks[w.Action], name, w.Action)
		for _, line := range w.Details {
			b.WriteString("      " + line + "\n")
		}
	}
	if n := len(plan.Orphans); n > 0 {
		fmt.Fprintf(&b, "  - %s left behind by an interrupted apply: delete\n", countPhrase(n, "unused definition", "unused definitions"))
	}
	_, err := io.WriteString(app.Stdout, b.String())
	return err
}

func newDashboardExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export <dashboard>",
		Short: "Write a dashboard as a manifest for 'dashboards apply'",
		Long: `Writes a dashboard as a manifest (YAML by default, -o json for JSON) that
'bronto dashboards apply' re-applies without changes: like kubectl get -o
yaml, it turns a dashboard built in the web UI into a file.

Each widget becomes an inline widget, with dataset: references for queries
over a collection and dataset. Inline widgets belong to the dashboard, so
applying the export of a dashboard apply didn't create takes ownership of
its widgets (a note on stderr says so): from then on, removing one from the
file deletes it. Widgets the manifest can't express (group widgets, widgets
with several definitions, widgets another manifest owns) are written as
ref: entries, which apply only attaches.

The dashboard can be an id or a unique name.`,
		Example: "  bronto dashboards export \"Checkout API health\" > dashboard.yaml\n" +
			"  bronto dashboards export <id> -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKindRef("dashboards"),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			asYAML := false
			switch app.OutputFlag {
			case "", "yaml":
				asYAML = true
				if app.JQ != nil {
					return clierr.New("usage_invalid_flags", "--jq requires -o json").
						WithHint("Pass -o json alongside --jq.")
				}
			case "json":
			default:
				return clierr.New("usage_invalid_output_format",
					fmt.Sprintf("dashboards export writes yaml or json, not %q", app.OutputFlag))
			}
			ctx := cmd.Context()
			id, err := resolveKindRef(ctx, app, "dashboards", args[0])
			if err != nil {
				return err
			}
			dash, err := getDashboard(ctx, app, id)
			if err != nil {
				return err
			}
			live := &liveState{Dashboard: dash}
			if err := fetchWidgetsAndDefs(ctx, app, live); err != nil {
				return err
			}
			m, notes := exportManifest(live)
			for _, n := range notes {
				app.Notef("Note: %s\n", n)
			}
			if !asYAML {
				p, err := app.PrinterFor(output.FormatJSON)
				if err != nil {
					return err
				}
				return p.PrintJSON(m)
			}
			var buf bytes.Buffer
			enc := yaml.NewEncoder(&buf)
			enc.SetIndent(2)
			if err := enc.Encode(m); err != nil {
				return err
			}
			_, err = app.Stdout.Write(buf.Bytes())
			return err
		},
	}
	return cmd
}

func newDashboardValidateCmd() *cobra.Command {
	var files []string
	var live bool
	var since string
	cmd := &cobra.Command{
		Use:   "validate -f <file|dir|->",
		Short: "Check dashboard manifests, offline or against the account",
		Long: `Checks dashboard manifests without changing anything: the YAML, every
field, query and formula, with the file:line of each problem. It exits 2 on
a problem and 0 when every file is valid.

With --live it also resolves the datasets and widget refs against the
account and runs each widget's queries once (POST /timeseries/search, which
saves nothing). A widget whose series are all zero over --since gets a
warning, not an error: usually its where or dataset matches nothing. A
query the API rejects is an error (exit 1).

` + manifestFormatHelp,
		Example: "  bronto dashboards validate -f dashboard.yaml\n" +
			"  bronto dashboards validate -f dashboards/ --live --since 7d",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sources, err := readManifestSources(cmd.InOrStdin(), files)
			if err != nil {
				return err
			}
			out := cmd.ErrOrStderr()
			var manifests []*manifest
			bad := 0
			for _, s := range sources {
				m, err := parseManifest(s.name, s.data)
				if err != nil {
					bad++
					_, _ = fmt.Fprintf(out, "✗ %s\n", s.name)
					for _, line := range strings.Split(strings.TrimPrefix(err.Error(), "invalid dashboard manifest:\n"), "\n") {
						_, _ = fmt.Fprintf(out, "  %s\n", strings.TrimSpace(line))
					}
					continue
				}
				manifests = append(manifests, m)
			}
			if bad > 0 {
				return clierr.New("usage_invalid_manifest", fmt.Sprintf("%d of %d manifest(s) are invalid", bad, len(sources)))
			}
			if !live {
				for _, m := range manifests {
					_, _ = fmt.Fprintf(out, "✓ %s: dashboard %q, %s\n", m.File, m.Name, countPhrase(len(m.Widgets), "widget", "widgets"))
				}
				return nil
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			// validate never writes: its POST /timeseries/search saves
			// nothing, so --dry-run must not turn it into a request plan.
			app.DryRun = false
			return validateLive(cmd.Context(), app, out, manifests, since)
		},
	}
	cmd.Flags().StringArrayVarP(&files, "filename", "f", nil, "manifest file, directory of .yaml/.yml files, or - for stdin (repeatable)")
	cmd.Flags().BoolVar(&live, "live", false, "also resolve datasets and refs, and run every query once")
	cmd.Flags().StringVar(&since, "since", "24h", "with --live: the window the queries run over")
	return cmd
}

// validateLive resolves each manifest against the account and runs every
// inline widget's queries once.
func validateLive(ctx context.Context, app *App, out io.Writer, manifests []*manifest, since string) error {
	spec, err := timerange.Resolve(since, "", "", nil)
	if err != nil {
		return err
	}
	timeRange := map[string]any{"natural": spec.TimeRange}
	if spec.TimeRange == "" {
		timeRange = map[string]any{"from_ts": spec.FromTs, "to_ts": spec.ToTs}
	}
	lookup, err := datasetLookupFor(ctx, app, manifests)
	if err != nil {
		return err
	}
	var widgets []liveWidget
	needRefs := false
	for _, m := range manifests {
		needRefs = needRefs || m.hasRefs()
	}
	if needRefs {
		st := &liveState{}
		payload, err := doJSONRequest(ctx, app, http.MethodGet, "/widgets", nil)
		if err != nil {
			return err
		}
		for _, row := range rowsFromPayload(payload, "widgets") {
			var w liveWidget
			if recode(row, &w) == nil {
				st.Widgets = append(st.Widgets, w)
			}
		}
		widgets = st.Widgets
	}
	problems, warnings := 0, 0
	for _, m := range manifests {
		var lines []string
		mark := "✓"
		want, err := m.resolve(lookup)
		if err != nil {
			problems++
			_, _ = fmt.Fprintf(out, "✗ %s: dashboard %q\n", m.File, m.Name)
			_, _ = fmt.Fprintf(out, "  %s\n", strings.ReplaceAll(strings.TrimPrefix(err.Error(), "invalid dashboard manifest:\n  "), "\n", "\n  "))
			continue
		}
		// Refs resolve the way apply resolves them: a name several widgets
		// share is fine when exactly one of them is on this dashboard.
		var attached map[string]bool
		if m.hasRefs() {
			dash, err := findDashboardByName(ctx, app, m.Name)
			if err != nil {
				problems++
				_, _ = fmt.Fprintf(out, "✗ %s: dashboard %q\n  %v\n", m.File, m.Name, err)
				continue
			}
			if dash != nil {
				attached = map[string]bool{}
				for _, id := range dash.WidgetIDs {
					attached[id] = true
				}
			}
		}
		for _, w := range want.Widgets {
			if w.Ref != "" {
				if _, err := matchRef(w.Ref, widgets, attached); err != nil {
					problems++
					mark = "✗"
					lines = append(lines, fmt.Sprintf("✗ %s: %s", w.Pos, err))
				}
				continue
			}
			body := map[string]any{"time_range": timeRange, "num_of_slices": 12, "queries": w.Def.Queries}
			if len(w.Def.Formulas) > 0 {
				body["formulas"] = w.Def.Formulas
			}
			b, _ := json.Marshal(body)
			payload, err := doJSONRequest(ctx, app, http.MethodPost, "/timeseries/search", b)
			if err != nil {
				problems++
				mark = "✗"
				lines = append(lines, fmt.Sprintf("✗ widget %q (%s): the API rejected its queries: %v", w.Name, w.Pos, err))
				continue
			}
			rows := plottedRows(timeseriesRows(payload, ""), w.Def)
			switch {
			case len(rows) == 0 || allZero(rows):
				warnings++
				if mark == "✓" {
					mark = "!"
				}
				lines = append(lines, fmt.Sprintf("! widget %q (%s): every series is zero over %s, so the widget would be empty. "+
					"Check its dataset and where (data ingested in the last few minutes may not show yet).", w.Name, w.Pos, since))
			default:
				lines = append(lines, fmt.Sprintf("✓ widget %q: %s", w.Name, countPhrase(len(rows), "series", "series")))
			}
		}
		_, _ = fmt.Fprintf(out, "%s %s: dashboard %q, %s\n", mark, m.File, m.Name, countPhrase(len(m.Widgets), "widget", "widgets"))
		for _, l := range lines {
			_, _ = fmt.Fprintf(out, "  %s\n", l)
		}
	}
	_, _ = fmt.Fprintf(out, "%s checked: %s, %s.\n", countPhrase(len(manifests), "manifest", "manifests"),
		countPhrase(problems, "problem", "problems"), countPhrase(warnings, "warning", "warnings"))
	if problems > 0 {
		return clierr.New("dashboard_check_failed", fmt.Sprintf("%s in the dashboard manifests", countPhrase(problems, "problem", "problems")))
	}
	return nil
}

func (m *manifest) hasRefs() bool {
	for _, w := range m.Widgets {
		if w.Ref != "" {
			return true
		}
	}
	return false
}

// plottedRows keeps the series a widget shows: with a formula the web UI
// plots only the formulas, so nonzero inputs don't make it non-empty.
func plottedRows(rows []map[string]any, def apiDef) []map[string]any {
	if len(def.Formulas) == 0 {
		return rows
	}
	formulas := map[string]bool{}
	for _, f := range def.Formulas {
		formulas[f.Name] = true
	}
	var out []map[string]any
	for _, r := range rows {
		if name, _ := r["series"].(string); formulas[name] {
			out = append(out, r)
		}
	}
	return out
}
