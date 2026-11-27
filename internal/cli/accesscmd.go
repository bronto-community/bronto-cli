package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/spf13/cobra"
)

// accessTarget is the --org/--user/--group selection of the access
// commands.
type accessTarget struct{ org, user, group string }

func (t *accessTarget) orgFlag(cmd *cobra.Command) {
	cmd.Flags().StringVar(&t.org, "org", "", "organization id (default: the API key's active or only organization)")
}

func (t *accessTarget) entityFlags(cmd *cobra.Command) {
	t.orgFlag(cmd)
	cmd.Flags().StringVar(&t.user, "user", "", "the user, by id or email")
	cmd.Flags().StringVar(&t.group, "group", "", "the group, by id or name")
	// One reference each, so the single-value completer: the list one
	// splits on commas, which a group name may contain.
	_ = cmd.RegisterFlagCompletionFunc("user", completeKindRef("users"))
	_ = cmd.RegisterFlagCompletionFunc("group", completeKindRef("groups"))
}

// check refuses anything but exactly one of --user and --group.
func (t accessTarget) check() error {
	if (t.user == "") == (t.group == "") {
		return clierr.New("usage_invalid_flags", "pass exactly one of --user and --group").
			WithHint("e.g. --user ada@example.com, or --group sre")
	}
	return nil
}

// orgID is the organization the command acts on: --org, else the API
// key's active organization, else the only one. Unlike resolveOrgID
// (search's deep link) it ignores the org_id config, which is documented
// as an override for web-app links, and never takes the first of several:
// a grant or revoke must not land on an organization nobody named.
func (t accessTarget) orgID(ctx context.Context, app *App) (string, error) {
	if t.org != "" {
		return t.org, nil
	}
	payload, err := doJSONRequest(ctx, app, http.MethodGet, "/organizations", nil)
	if err != nil {
		return "", err
	}
	var ids, labels []string
	for _, row := range rowsFromPayload(payload, "organisations") { // British spelling on the wire
		id, _ := row["id"].(string)
		if id == "" {
			continue
		}
		if active, _ := row["active"].(bool); active {
			return id, nil
		}
		ids = append(ids, id)
		if name, _ := row["name"].(string); name != "" {
			id += " (" + name + ")"
		}
		labels = append(labels, id)
	}
	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		return "", clierr.New("org_not_found", "could not determine which organization to act on").
			WithHint("Pass --org <id> (the id is in the web-app URL).")
	}
	slices.Sort(labels)
	return "", clierr.New("usage_org_required",
		fmt.Sprintf("the API key can reach %d organizations and none is marked active", len(ids))).
		WithHint("Pass --org <id>: " + capList(labels) + ".")
}

// orgPath is /organizations/{org}/access for the selected organization.
func (t accessTarget) orgPath(ctx context.Context, app *App) (path, org string, err error) {
	if org, err = t.orgID(ctx, app); err != nil {
		return "", "", err
	}
	return "/organizations/" + url.PathEscape(org) + "/access", org, nil
}

// entityPath is orgPath plus /{user|group}/{id}, with the name resolved;
// label names the entity as the user gave it and the organization ("group
// sre to organization <id>").
func (t accessTarget) entityPath(ctx context.Context, app *App) (path, label, org string, err error) {
	base, org, err := t.orgPath(ctx, app)
	if err != nil {
		return "", "", "", err
	}
	typ, kind, ref := "user", "users", t.user
	if t.group != "" {
		typ, kind, ref = "group", "groups", t.group
	}
	id, err := resolveKindRef(ctx, app, kind, ref)
	return base + "/" + typ + "/" + url.PathEscape(id), typ + " " + ref, org, err
}

// accessMembers returns the member rows of an access list: the live API
// (2026-11-27) answers a top-level members array of {type, id, roles,
// status, created_at}; the spec documents items[].members[] with
// createdAt.
func accessMembers(payload any) []map[string]any {
	obj, _ := payload.(map[string]any)
	if _, ok := obj["members"].([]any); ok {
		return rowsFromPayload(payload, "members")
	}
	items, ok := obj["items"].([]any)
	if !ok {
		return rowsFromPayload(payload, "members")
	}
	rows := []map[string]any{}
	for _, item := range items {
		rows = append(rows, rowsFromPayload(item, "members")...)
	}
	// The spec spells the timestamp createdAt; live it is created_at,
	// which is the column the table shows.
	for _, row := range rows {
		if v, ok := row["createdAt"]; ok && row["created_at"] == nil {
			row["created_at"] = v
			delete(row, "createdAt")
		}
	}
	return rows
}

// newAccessCmd builds "access list|check|grant|revoke": who may use an
// organization and with which roles (/organizations/{orgId}/access).
func newAccessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "Manage who has access to an organization",
		Long: `Lists, checks, grants and revokes the access users and groups have to an
organization, with the roles it gives them. The organization is the one the
API key belongs to unless --org names another. Users are given by id or
email, groups by id or name, roles by id or display name.`,
	}

	var lt accessTarget
	list := &cobra.Command{
		Use:     "list",
		Short:   "List the users and groups with access to the organization",
		Example: "  bronto access list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			path, _, err := lt.orgPath(cmd.Context(), app)
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodGet, path, nil)
			if err != nil {
				return err
			}
			return printPolishedRows(app, accessMembers(payload), []string{"type", "id", "roles", "status", "created_at"})
		},
	}
	lt.orgFlag(list)

	var ct accessTarget
	check := &cobra.Command{
		Use:     "check",
		Short:   "Show whether a user or group has access, and its roles",
		Long:    "Takes exactly one of --user and --group.",
		Example: "  bronto access check --user ada@example.com\n  bronto access check --group sre -o json --jq .is_authorised",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := ct.check(); err != nil {
				return err
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			path, _, _, err := ct.entityPath(cmd.Context(), app)
			if err != nil {
				return err
			}
			return printFetched(cmd.Context(), app, path+"/check")
		},
	}
	ct.entityFlags(check)

	var gt accessTarget
	var roles []string
	var status string
	grant := &cobra.Command{
		Use:   "grant",
		Short: "Give a user or group access to the organization",
		Long:  "Grants one user (--user) or one group (--group) access with the given roles; 'bronto roles list' shows their ids and names.",
		Example: "  bronto access grant --user ada@example.com --roles ReadOnly\n" +
			"  bronto access grant --group sre --roles Admin,<role> --dry-run",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := gt.check(); err != nil {
				return err
			}
			if len(roles) == 0 {
				return clierr.New("usage_invalid_flags", "pass --roles (the roles the access gives)").
					WithHint("e.g. --roles ReadOnly; 'bronto roles list' shows them.")
			}
			if status != "active" && status != "invited" {
				return clierr.New("usage_invalid_flags", fmt.Sprintf("--status %q is not active or invited", status))
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			path, label, org, err := gt.entityPath(cmd.Context(), app)
			if err != nil {
				return err
			}
			roleIDs, err := resolveKindRefs(cmd.Context(), app, "roles", roles)
			if err != nil {
				return err
			}
			return sendWrite(cmd.Context(), app, http.MethodPost, path,
				map[string]any{"roles": roleIDs, "status": status},
				fmt.Sprintf("Granted %s access to organization %s with %s.", label, org, strings.Join(roles, ", ")))
		},
	}
	gt.entityFlags(grant)
	grant.Flags().StringSliceVar(&roles, "roles", nil, "roles to give, by id or display name (comma-separated or repeated)")
	grant.Flags().StringVar(&status, "status", "active", "status of the grant: active or invited")
	_ = grant.RegisterFlagCompletionFunc("roles", completeKindListFlag("roles"))

	var rt accessTarget
	var yes bool
	revoke := &cobra.Command{
		Use:     "revoke",
		Short:   "Remove a user's or group's access to the organization",
		Long:    "Takes exactly one of --user and --group. Asks for confirmation unless --yes is passed.",
		Example: "  bronto access revoke --user ada@example.com --yes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := rt.check(); err != nil {
				return err
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			path, label, org, err := rt.entityPath(cmd.Context(), app)
			if err != nil {
				return err
			}
			if ok, err := confirmOrAbort(cmd, app, fmt.Sprintf("Revoke the access of %s to organization %s?", label, org), yes); !ok {
				return err
			}
			return sendWrite(cmd.Context(), app, http.MethodDelete, path, nil,
				fmt.Sprintf("Revoked the access of %s to organization %s.", label, org))
		},
	}
	rt.entityFlags(revoke)
	revoke.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")

	cmd.AddCommand(list, check, grant, revoke)
	return cmd
}

// newUserAccessCmd builds "users access <user>": the organizations a user
// can access (GET /users/{id}/access).
func newUserAccessCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "access <user>",
		Short:             "List the organizations a user can access",
		Example:           "  bronto users access ada@example.com",
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
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodGet, "/users/"+url.PathEscape(id)+"/access", nil)
			if err != nil {
				return err
			}
			return printPolishedRows(app, rowsFromPayload(payload, "organisations"), []string{"name", "active", "id"})
		},
	}
}

// printPolishedRows prints rows as a list: readable timestamps in a
// table or csv, the API's values in json.
func printPolishedRows(app *App, rows []map[string]any, columns []string) error {
	p, rows, err := humanRows(app, rows, resourceListPolish)
	if err != nil {
		return err
	}
	return p.PrintRows(columns, rows)
}
