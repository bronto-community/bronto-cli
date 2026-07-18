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
	// json/jsonl keep the API payload untouched.
	ListTransform func(rows []map[string]any) []map[string]any

	NoCreate bool
	NoUpdate bool
	NoDelete bool
	NoGet    bool
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
	{Name: "monitors", Base: "/monitors", UpdateMethod: http.MethodPut},
	{Name: "dashboards", Base: "/dashboards"},
	{Name: "saved-searches", Base: "/saved-searches", Singular: "saved search"},
	// The vendored spec has no GET /parsers/{parser_id}: only patch and
	// delete are documented for a single parser.
	{Name: "parsers", Base: "/parsers", NoGet: true},
	{Name: "api-keys", Base: "/api-keys", Singular: "API key", NoGet: true},
	{Name: "datasets", Base: "/logs", CreatePath: "/datasets", UpdateMethod: http.MethodPut,
		// The raw /logs rows are unreadable as a table (duplicate name
		// fields, a metadata blob with epoch floats); curate the human
		// view and derive LAST_ACTIVITY from metadata.last_heartbeat_at.
		Columns:       []string{"collection", "dataset", "last_activity", "log_id"},
		ListTransform: datasetListRows},
	// exports has no update verb; its create is hand-written (exports.go) to
	// support the convenience flags / --wait / --download workflow and
	// replaces the generic factory create (see newResourceCmd's extras
	// override rule).
	{Name: "exports", Singular: "export", Base: "/exports", NoUpdate: true},
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
		return nil, clierr.New("network_error", err.Error()).WithRetryable().
			WithHint("Check your network and the API base URL / region.")
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
	if err := json.Unmarshal(respBody, &doc); err != nil {
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
		if err := json.Unmarshal(body, &decoded); err == nil {
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
// subcommands (e.g. monitors' events/mute/test) to attach alongside the
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
	return &cobra.Command{
		Use:     "list",
		Short:   fmt.Sprintf("List %s", desc.Name),
		Example: fmt.Sprintf("  bronto %s list", desc.Name),
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
			if desc.ListTransform != nil && (format == output.FormatTable || format == output.FormatCSV) {
				rows = desc.ListTransform(rows)
			}
			p, err := app.PrinterFor(format)
			if err != nil {
				return err
			}
			return p.PrintRows(columnsFor(desc, rows), rows)
		},
	}
}

func newResourceGetCmd(desc resourceDesc) *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   fmt.Sprintf("Get a %s by ID", desc.singular()),
		Example: fmt.Sprintf("  bronto %s get <id>", desc.Name),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodGet, desc.idBase()+"/"+url.PathEscape(args[0]), nil)
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
		Example: "  bronto " + desc.Name + " create -f name=x -f limit=10\n" +
			"  bronto " + desc.Name + " create --input body.json",
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
			payload, err := doJSONRequest(cmd.Context(), app, desc.updateMethod(), desc.idBase()+"/"+url.PathEscape(args[0]), body)
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
		Example: fmt.Sprintf("  bronto %s delete <id> --yes", desc.Name),
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
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodDelete, desc.idBase()+"/"+url.PathEscape(args[0]), nil)
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
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodGet, "/monitors/"+url.PathEscape(args[0])+"/events", nil)
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
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodPost, "/monitors/"+url.PathEscape(args[0])+"/status", body)
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
