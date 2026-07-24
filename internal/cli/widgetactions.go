package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// Widget-composition actions: attaching/removing widgets to dashboards and
// to other widgets, and detaching a dashboard from its template. These are
// action sub-verbs on sub-collections (POST/DELETE against fixed paths), so
// they don't fit the uniform list/get/create/update/delete factory or the
// AttachTo nesting — they're hand-written and attached alongside the
// generated subcommands (see root.go), mirroring the monitors events/mute
// extras. The parent (dashboard/widget) is resolved by unique name; a UUID
// short-circuits the lookup, so these work even while `widgets list` is
// unavailable server-side.

// singularKind turns a registry kind ("dashboards", "widgets") into its
// human singular for command help ("dashboard", "widget").
func singularKind(kind string) string {
	return strings.TrimSuffix(kind, "s")
}

// newAttachWidgetsCmd builds "<kind> attach-widgets <parent> --widget-ids …":
// POST /<kind>/{id}/widgets with {"widget_ids": [...]} (204).
func newAttachWidgetsCmd(kind string) *cobra.Command {
	var widgetIDs []string
	sing := singularKind(kind)
	cmd := &cobra.Command{
		Use:     "attach-widgets <" + sing + "> --widget-ids <id,...>",
		Short:   "Attach widgets to a " + sing,
		Example: "  bronto " + kind + " attach-widgets <" + sing + "> --widget-ids <id>,<id>",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			if len(widgetIDs) == 0 {
				return clierr.New("usage_invalid_flags",
					"--widget-ids is required (the widget IDs to attach)").
					WithHint("Pass one or more widget IDs, e.g. --widget-ids <id>,<id>.")
			}
			id, err := resolveKindRef(cmd.Context(), app, kind, args[0])
			if err != nil {
				return err
			}
			body, err := json.Marshal(map[string][]string{"widget_ids": widgetIDs})
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodPost,
				"/"+kind+"/"+url.PathEscape(id)+"/widgets", body)
			if err != nil {
				return err
			}
			if isDryRunPlan(payload) {
				_, _ = fmt.Fprintf(app.Stderr, "DRY RUN: would attach %d widget(s) to %s %s.\n", len(widgetIDs), sing, args[0])
				return nil
			}
			_, _ = fmt.Fprintf(app.Stderr, "Attached %d widget(s) to %s %s.\n", len(widgetIDs), sing, args[0])
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&widgetIDs, "widget-ids", nil, "widget IDs to attach (comma-separated or repeated)")
	return cmd
}

// newRemoveWidgetCmd builds "<kind> remove-widget <parent> <widget-id>":
// DELETE /<kind>/{id}/widgets/{widgetId} (204).
func newRemoveWidgetCmd(kind string) *cobra.Command {
	sing := singularKind(kind)
	return &cobra.Command{
		Use:     "remove-widget <" + sing + "> <widget-id>",
		Short:   "Remove a widget from a " + sing,
		Example: "  bronto " + kind + " remove-widget <" + sing + "> <widget-id>",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			id, err := resolveKindRef(cmd.Context(), app, kind, args[0])
			if err != nil {
				return err
			}
			widgetID := args[1]
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodDelete,
				"/"+kind+"/"+url.PathEscape(id)+"/widgets/"+url.PathEscape(widgetID), nil)
			if err != nil {
				return err
			}
			if isDryRunPlan(payload) {
				_, _ = fmt.Fprintf(app.Stderr, "DRY RUN: would remove widget %s from %s %s.\n", widgetID, sing, args[0])
				return nil
			}
			_, _ = fmt.Fprintf(app.Stderr, "Removed widget %s from %s %s.\n", widgetID, sing, args[0])
			return nil
		},
	}
}

// newDetachFromTemplateCmd builds "dashboards detach-from-template
// <dashboard>": POST /dashboards/{id}/detach-from-template (200 Dashboard).
func newDetachFromTemplateCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "detach-from-template <dashboard>",
		Short:   "Detach a dashboard from its template",
		Example: "  bronto dashboards detach-from-template <dashboard>",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			id, err := resolveKindRef(cmd.Context(), app, "dashboards", args[0])
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodPost,
				"/dashboards/"+url.PathEscape(id)+"/detach-from-template", nil)
			if err != nil {
				return err
			}
			// Success returns the updated Dashboard; --dry-run returns the
			// request plan. Print either through the normal engine.
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintJSON(payload)
		},
	}
}
