package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/spf13/cobra"
)

// accessTarget is the --org/--user/--group selection of the access
// commands.
type accessTarget struct{ org, user, group string }

func (t *accessTarget) orgFlag(cmd *cobra.Command) {
	cmd.Flags().StringVar(&t.org, "org", "", "organization id (default: org_id from the config, else the active organization)")
}

func (t *accessTarget) entityFlags(cmd *cobra.Command) {
	t.orgFlag(cmd)
	cmd.Flags().StringVar(&t.user, "user", "", "the user, by id or email")
	cmd.Flags().StringVar(&t.group, "group", "", "the group, by id or name")
	_ = cmd.RegisterFlagCompletionFunc("user", completeKindListFlag("users"))
	_ = cmd.RegisterFlagCompletionFunc("group", completeKindListFlag("groups"))
}

// check refuses anything but exactly one of --user and --group.
func (t accessTarget) check() error {
	if (t.user == "") == (t.group == "") {
		return clierr.New("usage_invalid_flags", "pass exactly one of --user and --group").
			WithHint("e.g. --user ada@example.com, or --group sre")
	}
	return nil
}

// orgPath is /organizations/{org}/access for the selected organization.
func (t accessTarget) orgPath(ctx context.Context, app *App) (string, error) {
	org := t.org
	if org == "" {
		var err error
		if org, err = resolveOrgID(ctx, app); err != nil {
			return "", err
		}
	}
	return "/organizations/" + url.PathEscape(org) + "/access", nil
}

// entityPath is orgPath plus /{user|group}/{id}, with the name resolved;
// label names the entity as the user gave it ("group sre").
func (t accessTarget) entityPath(ctx context.Context, app *App) (path, label string, err error) {
	base, err := t.orgPath(ctx, app)
	if err != nil {
		return "", "", err
	}
	typ, kind, ref := "user", "users", t.user
	if t.group != "" {
		typ, kind, ref = "group", "groups", t.group
	}
	id, err := resolveKindRef(ctx, app, kind, ref)
	return base + "/" + typ + "/" + url.PathEscape(id), typ + " " + ref, err
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
			path, err := lt.orgPath(cmd.Context(), app)
			if err != nil {
				return err
			}
			// Live the members are a top-level array, not the spec's
			// items[].members[].
			return printFetchedRows(cmd.Context(), app, path, "members", []string{"type", "id", "roles", "status", "createdAt"})
		},
	}
	lt.orgFlag(list)

	var ct accessTarget
	check := &cobra.Command{
		Use:     "check",
		Short:   "Show whether a user or group has access, and its roles",
		Long:    "Takes exactly one of --user and --group.",
		Example: "  bronto access check --user ada@example.com\n  bronto access check --group sre --jq .is_authorised",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := ct.check(); err != nil {
				return err
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			path, _, err := ct.entityPath(cmd.Context(), app)
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
			path, label, err := gt.entityPath(cmd.Context(), app)
			if err != nil {
				return err
			}
			roleIDs, err := resolveKindRefs(cmd.Context(), app, "roles", roles)
			if err != nil {
				return err
			}
			return sendWrite(cmd.Context(), app, http.MethodPost, path,
				map[string]any{"roles": roleIDs, "status": status},
				fmt.Sprintf("Granted %s access with %s.", label, strings.Join(roles, ", ")))
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
			path, label, err := rt.entityPath(cmd.Context(), app)
			if err != nil {
				return err
			}
			if ok, err := confirmOrAbort(cmd, app, fmt.Sprintf("Revoke the access of %s?", label), yes); !ok {
				return err
			}
			return sendWrite(cmd.Context(), app, http.MethodDelete, path, nil, fmt.Sprintf("Revoked the access of %s.", label))
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
			return printFetchedRows(cmd.Context(), app, "/users/"+url.PathEscape(id)+"/access",
				"organisations", []string{"name", "active", "id"})
		},
	}
}

// printFetchedRows GETs path and prints the array under key as rows.
func printFetchedRows(ctx context.Context, app *App, path, key string, columns []string) error {
	payload, err := doJSONRequest(ctx, app, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	p, rows, err := humanRows(app, rowsFromPayload(payload, key), resourceListPolish)
	if err != nil {
		return err
	}
	return p.PrintRows(columns, rows)
}
