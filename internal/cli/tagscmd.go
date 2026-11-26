package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/spf13/cobra"
)

// tagEntityKinds maps the API's tag entity types to the resource whose
// names resolve to that entity's id ("" where the CLI has no such
// resource, so only an id works).
var tagEntityKinds = map[string]string{
	"log": "datasets", "dashboard": "dashboards", "user": "users", "api_key": "api-keys",
	"widget": "widgets", "saved_search": "saved-searches", "monitor": "monitors",
	"slo": "", "integration": "",
}

func tagEntityTypes() string {
	types := make([]string, 0, len(tagEntityKinds))
	for t := range tagEntityKinds {
		types = append(types, t)
	}
	slices.Sort(types)
	return strings.Join(types, ", ")
}

func checkTagEntityType(typ string) error {
	if _, ok := tagEntityKinds[typ]; !ok {
		return clierr.New("usage_invalid_args", fmt.Sprintf("%q is not a taggable entity type", typ)).
			WithHint("Entity types: " + tagEntityTypes() + ".")
	}
	return nil
}

// tagEntity is the {"type","id"} entity of a tag request, with a name
// resolved to the id.
func tagEntity(ctx context.Context, app *App, typ, ref string) (map[string]string, error) {
	id := ref
	if kind := tagEntityKinds[typ]; kind != "" {
		var err error
		if id, err = resolveKindRef(ctx, app, kind, ref); err != nil {
			return nil, err
		}
	}
	return map[string]string{"type": typ, "id": id}, nil
}

// newTagsCmd builds "tags list|create|update|delete" (/tags and
// /tags/search). A tag has no id: it is a name on one entity, so the
// write verbs take the entity type, the entity, and name=value.
func newTagsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tags",
		Short: "Manage the tags on datasets, dashboards, monitors and other entities",
		Long: `A tag is a name=value pair on one entity; a name is unique per entity.
The write verbs take the entity's type (` + tagEntityTypes() + `),
then the entity by id, or by name where the CLI has a command for that
type (log is a dataset), then the tag.`,
	}

	var name, value, entityType string
	list := &cobra.Command{
		Use:     "list",
		Short:   "List tag names, their values, and how many entities of each type carry them",
		Example: "  bronto tags list\n  bronto tags list --name team --entity-type dashboard",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			for k, v := range map[string]string{"name": name, "value": value, "entity_type": entityType} {
				if v != "" {
					q.Set(k, v)
				}
			}
			path := "/tags/search"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodGet, path, nil)
			if err != nil {
				return err
			}
			p, rows, err := humanRows(app, rowsFromPayload(payload, "tags"), tagListRows)
			if err != nil {
				return err
			}
			return p.PrintRows([]string{"name", "value", "type", "entities"}, rows)
		},
	}
	list.Flags().StringVar(&name, "name", "", "only tags with this name")
	list.Flags().StringVar(&value, "value", "", "only tags with this value")
	list.Flags().StringVar(&entityType, "entity-type", "", "only tags on this entity type")
	cmd.AddCommand(list)

	for _, v := range []struct{ verb, method, short, done string }{
		{"create", http.MethodPost, "Add a tag to an entity", "Created"},
		{"update", http.MethodPut, "Change the value of a tag on an entity", "Updated"},
	} {
		cmd.AddCommand(&cobra.Command{
			Use:     v.verb + " <entity-type> <entity> <tag>",
			Long:    "The tag is given as name=value.",
			Short:   v.short,
			Example: "  bronto tags " + v.verb + " dashboard \"Checkout overview\" team=checkout",
			Args:    cobra.ExactArgs(3),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := checkTagEntityType(args[0]); err != nil {
					return err
				}
				name, value, ok := strings.Cut(args[2], "=")
				if !ok || name == "" || value == "" {
					return clierr.New("usage_invalid_args", fmt.Sprintf("%q is not a tag: want <name>=<value>", args[2]))
				}
				app, err := NewApp(cmd)
				if err != nil {
					return err
				}
				entity, err := tagEntity(cmd.Context(), app, args[0], args[1])
				if err != nil {
					return err
				}
				return sendWrite(cmd.Context(), app, v.method, "/tags",
					map[string]any{"name": name, "value": value, "entity": entity},
					fmt.Sprintf("%s tag %s on %s %s.", v.done, args[2], args[0], args[1]))
			},
		})
	}

	var yes bool
	del := &cobra.Command{
		Use:     "delete <entity-type> <entity> <tag-name>",
		Short:   "Remove a tag from an entity",
		Example: "  bronto tags delete dashboard \"Checkout overview\" team --yes",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkTagEntityType(args[0]); err != nil {
				return err
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			entity, err := tagEntity(cmd.Context(), app, args[0], args[1])
			if err != nil {
				return err
			}
			if ok, err := confirmOrAbort(cmd, app, fmt.Sprintf("Delete tag %s from %s %s?", args[2], args[0], args[1]), yes); !ok {
				return err
			}
			return sendWrite(cmd.Context(), app, http.MethodDelete, "/tags",
				map[string]any{"name": args[2], "entity": entity},
				fmt.Sprintf("Deleted tag %s from %s %s.", args[2], args[0], args[1]))
		},
	}
	del.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.AddCommand(del)
	return cmd
}

// tagListRows flattens /tags/search rows for table and csv to one row
// per name, value and entity type, with the number of entities carrying
// it. Live a row is {name, value: [{value, types: [{type, entities:
// [{id}]}]}]}; the spec documents the array as "values" and the types as
// plain strings, so both are read.
func tagListRows(rows []map[string]any, _ output.Format) []map[string]any {
	var out []map[string]any
	for _, row := range rows {
		values, _ := row["value"].([]any)
		if values == nil {
			values, _ = row["values"].([]any)
		}
		for _, v := range values {
			m, _ := v.(map[string]any)
			types, _ := m["types"].([]any)
			for _, t := range types {
				r := map[string]any{"name": row["name"], "value": m["value"], "type": t}
				if tm, ok := t.(map[string]any); ok {
					entities, _ := tm["entities"].([]any)
					r["type"], r["entities"] = tm["type"], len(entities)
				}
				out = append(out, r)
			}
		}
	}
	return out
}
