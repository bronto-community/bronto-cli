package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

// Hand-written sub-verbs for the #122 endpoints that don't fit the
// list/get/create/update/delete factory: group membership changes, a
// user's groups, monitor notifications, metric top-keys and dataset parser
// assignment. Each resolves its parent by id or unique name, supports
// --dry-run for mutations, and confirms 204s through App.Notef.

// newGroupMembershipCmd builds "groups add-members|remove-members <group>":
// POST or DELETE /groups/{id}/members with {"members": [{member_type,
// member_id}]}. --users take ids or emails, --groups ids or names.
func newGroupMembershipCmd(add bool) *cobra.Command {
	var users, groups []string
	use, method, verb, past := "remove-members", http.MethodDelete, "remove", "Removed"
	if add {
		use, method, verb, past = "add-members", http.MethodPost, "add", "Added"
	}
	cmd := &cobra.Command{
		Use:   use + " <group> [--users <id|email,...>] [--groups <id|name,...>]",
		Short: map[bool]string{true: "Add users or groups to a group", false: "Remove users or groups from a group"}[add],
		Example: "  bronto groups " + use + " oncall --users alice@example.com,bob@example.com\n" +
			"  bronto groups " + use + " oncall --groups sre",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKindRef("groups"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(users) == 0 && len(groups) == 0 {
				return clierr.New("usage_invalid_flags", "pass --users and/or --groups (the members to "+verb+")").
					WithHint("e.g. bronto groups " + use + " <group> --users <email>,<email>")
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			id, err := resolveKindRef(cmd.Context(), app, "groups", args[0])
			if err != nil {
				return err
			}
			type member struct {
				Type string `json:"member_type"`
				ID   string `json:"member_id"`
			}
			var members []member
			for _, kind := range []struct {
				refs []string
				reg  string
				typ  string
			}{{users, "users", "USER"}, {groups, "groups", "GROUP"}} {
				for _, ref := range kind.refs {
					mid, err := resolveKindRef(cmd.Context(), app, kind.reg, ref)
					if err != nil {
						return err
					}
					members = append(members, member{kind.typ, mid})
				}
			}
			body, err := json.Marshal(map[string][]member{"members": members})
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, method, "/groups/"+url.PathEscape(id)+"/members", body)
			if err != nil {
				return err
			}
			if isDryRunPlan(payload) {
				_, _ = fmt.Fprintf(app.Stderr, "DRY RUN: would %s %d member(s) %s group %s.\n", verb, len(members), map[bool]string{true: "to", false: "from"}[add], args[0])
				return nil
			}
			app.Notef("%s %d member(s) %s group %s.\n", past, len(members), map[bool]string{true: "to", false: "from"}[add], args[0])
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&users, "users", nil, "users to "+verb+", by id or email (comma-separated or repeated)")
	cmd.Flags().StringSliceVar(&groups, "groups", nil, "groups to "+verb+", by id or name (comma-separated or repeated)")
	_ = cmd.RegisterFlagCompletionFunc("users", completeKindFlag("users"))
	_ = cmd.RegisterFlagCompletionFunc("groups", completeKindFlag("groups"))
	return cmd
}

// newUserGroupsCmd builds "users groups <user>": GET /users/{id}/groups,
// the reverse of "groups members". The spec documents UserGroup objects,
// but the live API returns bare group ids (2026-10-02), so ids are joined
// against the groups list to print names.
func newUserGroupsCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "groups <user>",
		Short:             "List the groups a user belongs to",
		Example:           "  bronto users groups alice@example.com",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKindRef("users"),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			id, err := resolveKindRef(cmd.Context(), app, "users", args[0])
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodGet, "/users/"+url.PathEscape(id)+"/groups", nil)
			if err != nil {
				return err
			}
			rows, err := userGroupRows(cmd, app, payload)
			if err != nil {
				return err
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows([]string{"name", "description", "group_id"}, rows)
		},
	}
}

// userGroupRows turns a /users/{id}/groups payload into group rows. Object
// items (the documented shape) pass through; bare id strings (the live
// shape) are filled in from GET /groups, keeping the id when a group isn't
// listed.
func userGroupRows(cmd *cobra.Command, app *App, payload any) ([]map[string]any, error) {
	m, _ := payload.(map[string]any)
	items, _ := m["groups"].([]any)
	var ids []string
	for _, it := range items {
		if s, ok := it.(string); ok {
			ids = append(ids, s)
		}
	}
	if len(ids) == 0 {
		return rowsFromPayload(payload, "groups"), nil
	}
	all, err := doJSONRequest(cmd.Context(), app, http.MethodGet, "/groups", nil)
	if err != nil {
		return nil, err
	}
	byID := map[string]map[string]any{}
	for _, g := range rowsFromPayload(all, "groups") {
		if gid, _ := g["group_id"].(string); gid != "" {
			byID[gid] = g
		}
	}
	rows := make([]map[string]any, 0, len(ids))
	for _, gid := range ids {
		if g, ok := byID[gid]; ok {
			rows = append(rows, g)
		} else {
			rows = append(rows, map[string]any{"group_id": gid})
		}
	}
	return rows, nil
}

// newMonitorNotificationsCmd builds "monitors notifications <monitor>":
// GET /monitors/{id}/notifications, which takes only absolute bounds.
func newMonitorNotificationsCmd() *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:               "notifications <id>",
		Short:             "List notifications a monitor sent",
		Example:           "  bronto monitors notifications <id> --since 7d",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKindRef("monitors"),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := timerange.Absolute(since, nil)
			if err != nil {
				return err
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			id, err := resolveKindRef(cmd.Context(), app, "monitors", args[0])
			if err != nil {
				return err
			}
			params := url.Values{
				"from_ts": {strconv.FormatInt(spec.FromTs, 10)},
				"to_ts":   {strconv.FormatInt(spec.ToTs, 10)},
			}
			return getAndPrintRows(cmd, app, "/monitors/"+url.PathEscape(id)+"/notifications", params,
				[]string{"time", "status", "monitor_status", "type", "destination", "failure_reason"}, "monitor_notifications")
		},
	}
	cmd.Flags().StringVar(&since, "since", "24h", "lookback window (e.g. 1h, 7d, 1h30m)")
	return cmd
}

// newMetricTopKeysCmd builds "metrics top-keys <metric>": the attribute
// keys seen on a metric, like "bronto fields" for a dataset.
func newMetricTopKeysCmd() *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:               "top-keys <metric>",
		Short:             "List the attribute keys seen on a metric",
		Example:           "  bronto metrics top-keys system.cpu.utilization --since 1h",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKindRef("metrics"),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := resolveRelativeSince(since, "Last 1 hour", "metrics top-keys", "/metrics/{metric_id}/top-keys")
			if err != nil {
				return err
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			id, err := resolveKindRef(cmd.Context(), app, "metrics", args[0])
			if err != nil {
				return err
			}
			rows, err := topKeyRowsAt(cmd.Context(), app, "/metrics/"+url.PathEscape(id)+"/top-keys",
				url.Values{"time_range": {spec.TimeRange}})
			if err != nil {
				return err
			}
			return printTopKeyRows(app, rows)
		},
	}
	cmd.Flags().StringVar(&since, "since", "1h", "relative lookback (single unit: 30s, 15m, 1h, 2d)")
	return cmd
}

// newDatasetParserCmd builds "datasets parser get|set|unset": which parser
// a dataset uses (GET/PUT/DELETE /datasets/{id}/parser).
func newDatasetParserCmd() *cobra.Command {
	parent := &cobra.Command{
		Use:   "parser",
		Short: "Show or change the parser assigned to a dataset",
	}
	resolve := func(cmd *cobra.Command, ref string) (*App, string, error) {
		app, err := NewApp(cmd)
		if err != nil {
			return nil, "", err
		}
		id, err := resolveKindRef(cmd.Context(), app, "datasets", ref)
		return app, id, err
	}
	parent.AddCommand(&cobra.Command{
		Use:               "get <dataset>",
		Short:             "Show the parser assigned to a dataset",
		Example:           "  bronto datasets parser get default/nginx",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeDatasets,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, id, err := resolve(cmd, args[0])
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodGet, "/datasets/"+url.PathEscape(id)+"/parser", nil)
			if err != nil {
				return err
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintJSON(payload)
		},
	}, &cobra.Command{
		Use:               "set <dataset> <parser>",
		Short:             "Assign a parser to a dataset",
		Example:           "  bronto datasets parser set default/nginx nginx_combined",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeDatasetThenParser,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, id, err := resolve(cmd, args[0])
			if err != nil {
				return err
			}
			parserID, err := resolveKindRef(cmd.Context(), app, "parsers", args[1])
			if err != nil {
				return err
			}
			body, err := json.Marshal(map[string]string{"parser_id": parserID})
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodPut, "/datasets/"+url.PathEscape(id)+"/parser", body)
			if err != nil {
				return err
			}
			if isDryRunPlan(payload) {
				_, _ = fmt.Fprintf(app.Stderr, "DRY RUN: would assign parser %s to dataset %s.\n", args[1], args[0])
				return nil
			}
			app.Notef("Assigned parser %s to dataset %s.\n", args[1], args[0])
			return nil
		},
	}, &cobra.Command{
		Use:               "unset <dataset>",
		Short:             "Remove the parser assignment from a dataset",
		Example:           "  bronto datasets parser unset default/nginx",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeDatasets,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, id, err := resolve(cmd, args[0])
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodDelete, "/datasets/"+url.PathEscape(id)+"/parser", nil)
			if err != nil {
				return err
			}
			if isDryRunPlan(payload) {
				_, _ = fmt.Fprintf(app.Stderr, "DRY RUN: would remove the parser from dataset %s.\n", args[0])
				return nil
			}
			app.Notef("Removed the parser from dataset %s.\n", args[0])
			return nil
		},
	})
	return parent
}

// completeDatasetThenParser completes "parser set"'s positionals: a
// dataset, then a parser name.
func completeDatasetThenParser(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return completeDatasets(cmd, args, toComplete)
	case 1:
		return completeKindRef("parsers")(cmd, nil, toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// getAndPrintRows GETs path and prints its rows with cols (auto columns
// when the payload carries other fields).
func getAndPrintRows(cmd *cobra.Command, app *App, path string, params url.Values, cols []string, rowKey string) error {
	var payload any
	client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
	if err := client.GetJSON(cmd.Context(), path, params, &payload); err != nil {
		return err
	}
	rows := rowsFromPayload(payload, rowKey)
	p, err := app.Printer(false)
	if err != nil {
		return err
	}
	if len(rows) > 0 && !hasAnyColumn(rows[0], cols) {
		cols = bronto.EventColumns(rows, 8)
	}
	return p.PrintRows(cols, rows)
}

// hasAnyColumn reports whether row carries at least one of cols, so a
// response shape the spec didn't predict still prints its real fields.
func hasAnyColumn(row map[string]any, cols []string) bool {
	for _, c := range cols {
		if _, ok := row[c]; ok {
			return true
		}
	}
	return false
}
