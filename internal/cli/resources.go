package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/api"
	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
)

// resourceDesc describes one uniform Bronto management resource: the
// paths its list/get/create/update/delete verbs hit, and how to shape its
// list output. One table entry replaces what would otherwise be five
// hand-written, near-identical commands per resource (see the "Architecture
// note" in the plan: a descriptor registry + generic factory instead of
// codegen, with resourcespec_test.go as the spec-conformance tripwire).
type resourceDesc struct {
	Name     string // subcommand name, e.g. "monitors"
	Singular string // human name for messages, e.g. "monitor"; defaults from Name
	Base     string // list/collection path, e.g. "/monitors"

	IDBase       string // get/update/delete path prefix; defaults to Base
	CreatePath   string // create path; defaults to Base
	UpdateMethod string // "PATCH" (default) or "PUT"

	ListRowKeys []string // payload keys to try for the list rows array; nil = auto
	Columns     []string // table/csv columns; nil = auto via bronto.EventColumns

	// ListTransform, if set, enriches list rows for the human formats
	// (table/csv) only — e.g. deriving a readable last_activity column.
	// json/jsonl keep the API payload untouched. The format lets a
	// transform render human-relative values for table but absolute
	// machine-friendly ones for csv.
	ListTransform func(rows []map[string]any, format output.Format) []map[string]any

	NoCreate bool
	NoUpdate bool
	NoDelete bool
	NoGet    bool

	// AttachTo nests this resource's command under a parent command name
	// instead of the root (e.g. templates/downtimes under monitors).
	AttachTo string

	// NameKeys are the list-row fields resolveResourceRef matches a
	// non-UUID reference against (default: ["name"]; users use email).
	NameKeys []string
	// IDKey is the list-row field carrying the resource id when it is not
	// plain "id" (e.g. groups' group_id, datasets' log_id).
	IDKey string

	// SecretKeys are list-row fields holding key material that must be
	// masked in EVERY output format by default (json/jsonl are the piped/CI
	// default, so verbatim keys land in build logs). When set, `list` gains
	// a --reveal flag to opt back into full values.
	SecretKeys []string
}

func (d resourceDesc) nameKeys() []string {
	if len(d.NameKeys) > 0 {
		return d.NameKeys
	}
	return []string{"name"}
}

func (d resourceDesc) rowID(row map[string]any) string {
	if d.IDKey != "" {
		if v, _ := row[d.IDKey].(string); v != "" {
			return v
		}
	}
	v, _ := row["id"].(string)
	return v
}

// display is the human command path used in examples: "monitors templates"
// for attached resources, plain Name otherwise.
func (d resourceDesc) display() string {
	if d.AttachTo != "" {
		return d.AttachTo + " " + d.Name
	}
	return d.Name
}

func (d resourceDesc) idBase() string {
	if d.IDBase != "" {
		return d.IDBase
	}
	return d.Base
}

func (d resourceDesc) createPath() string {
	if d.CreatePath != "" {
		return d.CreatePath
	}
	return d.Base
}

func (d resourceDesc) updateMethod() string {
	if d.UpdateMethod != "" {
		return d.UpdateMethod
	}
	return http.MethodPatch
}

func (d resourceDesc) singular() string {
	if d.Singular != "" {
		return d.Singular
	}
	return strings.TrimSuffix(d.Name, "s")
}

// resourceRegistry is the single source of truth for every uniform Bronto
// management resource. resourcespec_test.go asserts each entry's Base,
// CreatePath, and IDBase correspond to real api/openapi.yaml paths (modulo
// the documented exceptions there). Tags is intentionally absent: the API
// models it as base-path PUT/DELETE + /tags/search; use `bronto api`.
var resourceRegistry = []resourceDesc{
	{Name: "monitors", Base: "/monitors", UpdateMethod: http.MethodPut,
		Columns: []string{"name", "window", "threshold", "created", "id"}},
	{Name: "dashboards", Base: "/dashboards",
		Columns:       []string{"name", "description", "widgets_count", "created", "id"},
		ListTransform: dashboardListRows},
	{Name: "saved-searches", Base: "/saved-searches", Singular: "saved search",
		Columns: []string{"name", "description", "created", "id"}},
	// The vendored spec has no GET /parsers/{parser_id}: only patch and
	// delete are documented for a single parser.
	{Name: "parsers", Base: "/parsers", NoGet: true,
		Columns: []string{"name", "description", "type", "enabled", "created", "id"}},
	{Name: "api-keys", Base: "/api-keys", Singular: "API key", NoGet: true,
		// Key material is masked in every format by default (SecretKeys);
		// --reveal opts back in. Full keys were rendering into terminals,
		// scrollback, and — via the json/jsonl piped default — build logs.
		SecretKeys: []string{"api_key", "key"},
		Columns:    []string{"name", "api_key", "created", "id"}},
	{Name: "datasets", Base: "/logs", CreatePath: "/datasets", UpdateMethod: http.MethodPut,
		// The raw /logs rows are unreadable as a table (duplicate name
		// fields, a metadata blob with epoch floats); curate the human
		// view and derive LAST_ACTIVITY from metadata.last_heartbeat_at.
		Columns:       []string{"collection", "dataset", "last_activity", "log_id"},
		ListTransform: datasetListRows},
	// Read-only catalogs: list is the only verb the API documents.
	{Name: "collections", Base: "/collections", Singular: "collection",
		NoCreate: true, NoUpdate: true, NoDelete: true, NoGet: true,
		// The API returns {collection: [datasets…]} maps; untransformed,
		// the table renderer turned collection names into column headers.
		Columns:       []string{"collection", "datasets", "names"},
		ListTransform: collectionListRows},
	{Name: "log-views", Base: "/logs/views", Singular: "log view",
		NoCreate: true, NoUpdate: true, NoDelete: true, NoGet: true,
		// Rows carry only components + this_template_tags; derive the
		// human columns from them.
		Columns:       []string{"log_type", "components_count"},
		ListTransform: logViewListRows},
	{Name: "limits", Base: "/limits", Singular: "limit",
		Columns: []string{"category", "description", "value", "unit", "created", "id"}},
	{Name: "encryption-keys", Base: "/encryption-keys", Singular: "encryption key"},
	// No per-ID GET documented for these three; update is full-body PUT.
	{Name: "forward-configs", Base: "/forward-configs", Singular: "forward config",
		UpdateMethod: http.MethodPut, NoGet: true},
	{Name: "webhooks", Base: "/integrations/webhooks", Singular: "webhook",
		UpdateMethod: http.MethodPut, NoGet: true},
	{Name: "slack", Base: "/integrations/slack", Singular: "Slack integration",
		UpdateMethod: http.MethodPut, NoGet: true},
	{Name: "templates", AttachTo: "monitors", Base: "/monitors/templates",
		Singular: "monitor template", UpdateMethod: http.MethodPut,
		Columns: []string{"name", "description", "monitor_type", "window", "threshold", "id"}},
	{Name: "downtimes", AttachTo: "monitors", Base: "/monitors/downtimes",
		Singular: "downtime", UpdateMethod: http.MethodPut, NoGet: true},
	{Name: "users", Base: "/users", Singular: "user",
		Columns:       []string{"email", "first_name", "last_name", "last_login", "id"},
		NameKeys:      []string{"email"},
		ListTransform: userListRows},
	{Name: "groups", Base: "/groups", Singular: "group",
		Columns: []string{"name", "description", "created_at", "group_id"}, IDKey: "group_id"},
	// exports has no update verb; its create is hand-written (exports.go) to
	// support the convenience flags / --wait / --download workflow and
	// replaces the generic factory create (see newResourceCmd's extras
	// override rule).
	{Name: "exports", Singular: "export", Base: "/exports", NoUpdate: true, IDKey: "export_id"},
}

// doJSONRequest issues an authenticated request against the resolved base
// URL and decodes a JSON response body (nil for empty bodies, e.g. 204).
// Non-2xx responses become a typed *clierr.Error via api.ErrorFromStatus.
// Shared by every generic resource verb and by the monitors extras
// (events/mute).
func doJSONRequest(ctx context.Context, app *App, method, path string, body []byte) (any, error) {
	if app.DryRun && method != http.MethodGet && method != http.MethodHead {
		return dryRunPlan(method, path, body), nil
	}
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, app.Config.BaseURL()+path, reqBody)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.HTTPClient.Do(req)
	if err != nil {
		return nil, bronto.ClassifyTransportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if apiErr := api.ErrorFromStatus(resp.StatusCode, respBody); apiErr != nil {
		return nil, apiErr
	}
	if len(respBody) == 0 {
		return nil, nil
	}
	var doc any
	if err := bronto.DecodeJSON(respBody, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// dryRunPlan is what doJSONRequest returns for a mutating call under
// --dry-run: the exact request that WOULD have been sent. Commands print
// it through the normal output engine, so -o json yields a stable,
// machine-checkable plan document.
func dryRunPlan(method, path string, body []byte) map[string]any {
	plan := map[string]any{"dry_run": true, "method": method, "path": path}
	if len(body) > 0 {
		var decoded any
		if err := bronto.DecodeJSON(body, &decoded); err == nil {
			plan["body"] = decoded
		} else {
			plan["body"] = string(body)
		}
	}
	return plan
}

// isDryRunPlan reports whether payload came from dryRunPlan — for commands
// whose success path prints a side-effect message ("Deleted X.") that
// would be a lie under --dry-run.
func isDryRunPlan(payload any) bool {
	m, ok := payload.(map[string]any)
	if !ok {
		return false
	}
	v, _ := m["dry_run"].(bool)
	return v
}

// resourceRequestBody resolves the create/update request body from exactly
// one of --input (file or "-" for stdin) or -f k=v fields.
func resourceRequestBody(cmd *cobra.Command, input string, fields []string) ([]byte, error) {
	switch {
	case input != "" && len(fields) > 0:
		return nil, clierr.New("usage_conflicting_flags", "--input and --field are mutually exclusive")
	case input == "" && len(fields) == 0:
		return nil, clierr.New("usage_missing_body", "provide --input <file|-> or at least one -f key=value").
			WithHint("Example: --input body.json, or -f name=x -f limit=10")
	case input != "":
		return readBodyInput(cmd, input)
	default:
		obj, err := parseFieldArgs(fields)
		if err != nil {
			return nil, err
		}
		return json.Marshal(obj)
	}
}

// toRows converts a JSON array (as decoded into []any) into row maps,
// wrapping non-object elements so callers always get map[string]any rows.
func toRows(arr []any) []map[string]any {
	rows := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			rows = append(rows, m)
		} else {
			rows = append(rows, map[string]any{"value": item})
		}
	}
	return rows
}

// rowsFromPayload extracts a table-friendly []map[string]any view from an
// arbitrary decoded JSON payload, trying in order: each of keys as a
// top-level array-valued field; the payload itself if it's already a JSON
// array; the object's sole array-valued field if it has exactly one; and
// finally wrapping the payload (or a scalar) as a single row.
func rowsFromPayload(payload any, keys ...string) []map[string]any {
	if obj, ok := payload.(map[string]any); ok {
		for _, k := range keys {
			if arr, ok := obj[k].([]any); ok {
				return toRows(arr)
			}
		}
	}
	if arr, ok := payload.([]any); ok {
		return toRows(arr)
	}
	if obj, ok := payload.(map[string]any); ok {
		var onlyKey string
		count := 0
		for k, v := range obj {
			if _, ok := v.([]any); ok {
				onlyKey = k
				count++
			}
		}
		if count == 1 {
			return toRows(obj[onlyKey].([]any))
		}
		return []map[string]any{obj}
	}
	return []map[string]any{{"value": payload}}
}

// columnsFor resolves the table/csv columns for a resource's list output:
// the descriptor's explicit Columns if set, else an auto-derived set capped
// at 8 (bronto.EventColumns already prioritizes @time/@status/@raw and then
// sorts the rest, which reads fine for arbitrary resource rows too).
func columnsFor(desc resourceDesc, rows []map[string]any) []string {
	if desc.Columns != nil {
		return desc.Columns
	}
	return bronto.EventColumns(rows, 8)
}

// errAborted signals a user declined a destructive-action confirmation
// prompt; RunE handlers translate it into a clean exit-0 "Aborted." message.
var errAborted = errors.New("aborted")

// confirmDestructive implements the delete-confirmation contract shared by
// every resource's delete command:
//   - --yes: always proceeds, no prompt.
//   - stdout and stdin are both TTYs: prompt on stderr, proceed only on a
//     bare y/Y response, otherwise errAborted.
//   - non-TTY without --yes: refuses with usage_confirmation_required
//     (exit 2) — prompting on a non-interactive stream would hang forever.
func confirmDestructive(cmd *cobra.Command, app *App, prompt string, yes bool) error {
	if yes {
		return nil
	}
	if !stdoutIsTTY() || !stdinIsTTY() {
		return clierr.New("usage_confirmation_required",
			"refusing a destructive action without confirmation on a non-interactive session").
			WithHint("Pass --yes to proceed non-interactively.")
	}
	_, _ = fmt.Fprintf(app.Stderr, "%s [y/N]: ", prompt)
	line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	line = strings.TrimSpace(line)
	if line != "y" && line != "Y" {
		return errAborted
	}
	return nil
}

// newResourceCmd builds the "<name>" command with generic list/get/create/
// update/delete subcommands driven by desc. extras are hand-written
// subcommands (e.g. monitors' events/mute) to attach alongside the
// generated ones; an extra whose first Use word matches a generated verb
// (e.g. a hand-written "create" for exports, Task 4) REPLACES it instead of
// being added twice.
func newResourceCmd(desc resourceDesc, extras ...*cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   desc.Name,
		Short: fmt.Sprintf("Manage %s", desc.Name),
	}

	replaced := map[string]bool{}
	for _, e := range extras {
		replaced[firstWord(e.Use)] = true
	}

	type verb struct {
		name     string
		disabled bool
		build    func() *cobra.Command
	}
	verbs := []verb{
		{"list", false, func() *cobra.Command { return newResourceListCmd(desc) }},
		{"get", desc.NoGet, func() *cobra.Command { return newResourceGetCmd(desc) }},
		{"create", desc.NoCreate, func() *cobra.Command { return newResourceCreateCmd(desc) }},
		{"update", desc.NoUpdate, func() *cobra.Command { return newResourceUpdateCmd(desc) }},
		{"delete", desc.NoDelete, func() *cobra.Command { return newResourceDeleteCmd(desc) }},
	}
	for _, v := range verbs {
		if v.disabled || replaced[v.name] {
			continue
		}
		cmd.AddCommand(v.build())
	}
	for _, e := range extras {
		cmd.AddCommand(e)
	}
	return cmd
}

// firstWord returns the leading whitespace-delimited token of a cobra Use
// string, e.g. "get <id>" -> "get".
func firstWord(use string) string {
	if i := strings.IndexAny(use, " \t"); i >= 0 {
		return use[:i]
	}
	return use
}

func newResourceListCmd(desc resourceDesc) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   fmt.Sprintf("List %s", desc.Name),
		Example: fmt.Sprintf("  bronto %s list", desc.display()),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodGet, desc.Base, nil)
			if err != nil {
				return err
			}
			rows := rowsFromPayload(payload, desc.ListRowKeys...)
			format, err := app.DetectFormat(false)
			if err != nil {
				return err
			}
			// Mask secret material in ALL formats (json/jsonl are the piped
			// default) unless the user explicitly asked to reveal it.
			if len(desc.SecretKeys) > 0 {
				if reveal, _ := cmd.Flags().GetBool("reveal"); !reveal {
					maskSecretRows(rows, desc.SecretKeys)
				}
			}
			if format == output.FormatTable || format == output.FormatCSV {
				rows = resourceListPolish(rows, format)
				if desc.ListTransform != nil {
					rows = desc.ListTransform(rows, format)
				}
			}
			p, err := app.PrinterFor(format)
			if err != nil {
				return err
			}
			return p.PrintRows(columnsFor(desc, rows), rows)
		},
	}
	if len(desc.SecretKeys) > 0 {
		cmd.Flags().Bool("reveal", false, "show full secret values instead of masking them")
	}
	return cmd
}

// maskSecretRows masks the given secret-bearing keys in every row using the
// rune-safe maskSecret (short values reveal nothing). Applied before
// rendering in any format so key material never lands verbatim in json/
// jsonl output — the piped/CI default, i.e. build logs.
func maskSecretRows(rows []map[string]any, keys []string) {
	for _, row := range rows {
		for _, k := range keys {
			if s, ok := row[k].(string); ok && s != "" {
				row[k] = maskSecret(s)
			}
		}
	}
}

func newResourceGetCmd(desc resourceDesc) *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   fmt.Sprintf("Get a %s by ID", desc.singular()),
		Example: fmt.Sprintf("  bronto %s get <id>", desc.display()),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			id, err := resolveResourceRef(cmd.Context(), app, desc, args[0])
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodGet, desc.idBase()+"/"+url.PathEscape(id), nil)
			if err != nil {
				return err
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintJSON(payload)
		},
	}
}

func newResourceCreateCmd(desc resourceDesc) *cobra.Command {
	var fields []string
	var input string
	cmd := &cobra.Command{
		Use:   "create",
		Short: fmt.Sprintf("Create a %s", desc.singular()),
		Example: "  bronto " + desc.display() + " create -f name=x -f limit=10\n" +
			"  bronto " + desc.display() + " create --input body.json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := resourceRequestBody(cmd, input, fields)
			if err != nil {
				return err
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodPost, desc.createPath(), body)
			if err != nil {
				return err
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintJSON(payload)
		},
	}
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "key=value pair for the request body (repeatable)")
	cmd.Flags().StringVar(&input, "input", "", "request body from file, or - for stdin")
	return cmd
}

func newResourceUpdateCmd(desc resourceDesc) *cobra.Command {
	var fields []string
	var input string
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: fmt.Sprintf("Update a %s", desc.singular()),
		Example: "  bronto " + desc.Name + " update <id> -f name=x\n" +
			"  bronto " + desc.Name + " update <id> --input body.json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := resourceRequestBody(cmd, input, fields)
			if err != nil {
				return err
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			id, err := resolveResourceRef(cmd.Context(), app, desc, args[0])
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, desc.updateMethod(), desc.idBase()+"/"+url.PathEscape(id), body)
			if err != nil {
				return err
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintJSON(payload)
		},
	}
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "key=value pair for the request body (repeatable)")
	cmd.Flags().StringVar(&input, "input", "", "request body from file, or - for stdin")
	return cmd
}

func newResourceDeleteCmd(desc resourceDesc) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <id>",
		Short:   fmt.Sprintf("Delete a %s", desc.singular()),
		Example: fmt.Sprintf("  bronto %s delete <id> --yes", desc.display()),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			if !app.DryRun {
				prompt := fmt.Sprintf("Delete %s %s?", desc.singular(), args[0])
				if err := confirmDestructive(cmd, app, prompt, yes); err != nil {
					if errors.Is(err, errAborted) {
						_, _ = fmt.Fprintln(app.Stderr, "Aborted.")
						return nil
					}
					return err
				}
			}
			id, err := resolveResourceRef(cmd.Context(), app, desc, args[0])
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodDelete, desc.idBase()+"/"+url.PathEscape(id), nil)
			if err != nil {
				return err
			}
			if isDryRunPlan(payload) {
				_, _ = fmt.Fprintf(app.Stderr, "DRY RUN: would delete %s %s.\n", desc.singular(), args[0])
				return nil
			}
			_, _ = fmt.Fprintf(app.Stderr, "Deleted %s %s.\n", desc.singular(), args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// --- Monitors extras: events/mute have no place in the uniform
// list/get/create/update/delete shape, so they're hand-written and attached
// alongside the generated monitors subcommands (see root.go).

func newMonitorEventsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "events <id>",
		Short:   "List recent events for a monitor",
		Example: "  bronto monitors events <id>",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			id, err := resolveKindRef(cmd.Context(), app, "monitors", args[0])
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodGet, "/monitors/"+url.PathEscape(id)+"/events", nil)
			if err != nil {
				return err
			}
			rows := rowsFromPayload(payload)
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows(bronto.EventColumns(rows, 8), rows)
		},
	}
}

func newMonitorMuteCmd() *cobra.Command {
	var until int64
	var unmute bool
	cmd := &cobra.Command{
		Use:   "mute <id>",
		Short: "Mute (or unmute) a monitor",
		Example: "  bronto monitors mute <id>\n" +
			"  bronto monitors mute <id> --until 1710958395538\n" +
			"  bronto monitors mute <id> --unmute",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			// The live status endpoint takes mute_until: -1 mutes forever,
			// 0 unmutes, a future epoch-millis timestamp mutes until then.
			muteUntil := until
			if unmute {
				muteUntil = 0
			}
			body, err := json.Marshal(map[string]int64{"mute_until": muteUntil})
			if err != nil {
				return err
			}
			id, err := resolveKindRef(cmd.Context(), app, "monitors", args[0])
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodPost, "/monitors/"+url.PathEscape(id)+"/status", body)
			if err != nil {
				return err
			}
			if isDryRunPlan(payload) {
				_, _ = fmt.Fprintf(app.Stderr, "DRY RUN: would set mute_until=%d on monitor %s.\n", muteUntil, args[0])
				return nil
			}
			if unmute {
				_, _ = fmt.Fprintf(app.Stderr, "Unmuted monitor %s.\n", args[0])
			} else {
				_, _ = fmt.Fprintf(app.Stderr, "Muted monitor %s.\n", args[0])
			}
			return nil
		},
	}
	cmd.Flags().Int64Var(&until, "until", -1, "mute until this epoch-millis timestamp (-1 = forever)")
	cmd.Flags().BoolVar(&unmute, "unmute", false, "unmute the monitor instead")
	return cmd
}

// newUserActionCmd builds one of the users verb-actions (deactivate,
// reactivate, resend-invite): POST /users/{id}/<action> with no body.
func newUserActionCmd(action, short string) *cobra.Command {
	return &cobra.Command{
		Use:     action + " <id>",
		Short:   short,
		Example: "  bronto users " + action + " <id>",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			id, err := resolveKindRef(cmd.Context(), app, "users", args[0])
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodPost,
				"/users/"+url.PathEscape(id)+"/"+action, nil)
			if err != nil {
				return err
			}
			if isDryRunPlan(payload) {
				_, _ = fmt.Fprintf(app.Stderr, "DRY RUN: would %s user %s.\n", action, args[0])
				return nil
			}
			_, _ = fmt.Fprintf(app.Stderr, "%s: user %s.\n", short, args[0])
			return nil
		},
	}
}

// newGroupMembersCmd lists a group's members (GET /groups/{id}/members).
func newGroupMembersCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "members <id>",
		Short:   "List the members of a group",
		Example: "  bronto groups members <id>",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			id, err := resolveKindRef(cmd.Context(), app, "groups", args[0])
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodGet,
				"/groups/"+url.PathEscape(id)+"/members", nil)
			if err != nil {
				return err
			}
			rows := rowsFromPayload(payload)
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows(bronto.EventColumns(rows, 8), rows)
		},
	}
}

// resolveResourceRef turns a get/update/delete argument into a resource
// id, mirroring the dataset UX everywhere: UUIDs pass through untouched;
// datasets delegate to resolveDatasetRef (collection/name aware); every
// other resource matches the reference against its list's name keys — a
// unique match resolves, ambiguity and misses error with what IS there.
func resolveResourceRef(ctx context.Context, app *App, desc resourceDesc, ref string) (string, error) {
	if uuidRe.MatchString(ref) {
		return ref, nil
	}
	if desc.Name == "datasets" {
		return resolveDatasetRef(ctx, app, ref)
	}
	payload, err := doJSONRequest(ctx, app, http.MethodGet, desc.Base, nil)
	if err != nil {
		return "", err
	}
	rows := rowsFromPayload(payload, desc.ListRowKeys...)
	var matchIDs, available []string
	for _, row := range rows {
		for _, key := range desc.nameKeys() {
			if v, _ := row[key].(string); v != "" {
				if v == ref {
					matchIDs = append(matchIDs, desc.rowID(row))
				}
				available = append(available, v)
				break
			}
		}
	}
	switch len(matchIDs) {
	case 1:
		if matchIDs[0] == "" {
			break // matched a row without a usable id: fall through to not-found
		}
		return matchIDs[0], nil
	case 0:
	default:
		return "", clierr.New("usage_ambiguous_"+strings.ReplaceAll(desc.singular(), " ", "_"),
			fmt.Sprintf("%d %s are named %q", len(matchIDs), desc.Name, ref)).
			WithHint("Pass the id instead: " + capList(matchIDs))
	}
	hint := fmt.Sprintf("Run 'bronto %s list' to see ids and names.", desc.display())
	if len(available) > 0 {
		sort.Strings(available)
		hint = fmt.Sprintf("This account has: %s.", capList(available))
	}
	return "", clierr.New("resource_not_found",
		fmt.Sprintf("no %s named %q", desc.singular(), ref)).WithHint(hint)
}

// resolveKindRef is resolveResourceRef for hand-written extras that know
// their registry entry only by name (monitors events/mute, groups
// members, users verb-actions).
func resolveKindRef(ctx context.Context, app *App, kind, ref string) (string, error) {
	for _, d := range resourceRegistry {
		if d.Name == kind {
			return resolveResourceRef(ctx, app, d, ref)
		}
	}
	return ref, nil
}
