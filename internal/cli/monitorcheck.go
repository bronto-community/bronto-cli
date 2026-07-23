package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/query"
)

var monitorComparisonOps = map[string]bool{
	"BELOW": true, "BELOW_OR_EQUAL": true, "ABOVE": true,
	"ABOVE_OR_EQUAL": true, "EQUAL": true, "NOT_EQUAL": true,
}

var monitorWindowRe = regexp.MustCompile(`^Last (\d+) (minutes?|hours?|days?)$`)

// newMonitorCheckCmd lints monitor definitions before they ship —
// promtool-style validation as a CI gate: required fields, query syntax
// (via the client-side parser), window bounds, action shapes, and
// best-effort dataset existence.
func newMonitorCheckCmd() *cobra.Command {
	var inputs []string
	cmd := &cobra.Command{
		Use:   "check --input <file.json> [--input more.json]",
		Short: "Validate monitor definitions (CI-friendly: non-zero exit on problems)",
		Example: "  bronto monitors check --input monitor.json\n" +
			"  bronto monitors check --input monitors/a.json --input monitors/b.json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(inputs) == 0 {
				return clierr.New("usage_missing_body", "provide at least one --input <file.json>")
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			knownDatasets, datasetsChecked := monitorCheckDatasets(cmd.Context(), app)

			total, problems := 0, 0
			out := cmd.ErrOrStderr()
			for _, path := range inputs {
				body, err := readMonitorInput(cmd, path)
				if err != nil {
					return err
				}
				total++
				probs := lintMonitor(body, knownDatasets, datasetsChecked)
				name, _ := body["name"].(string)
				if name == "" {
					name = path
				}
				if len(probs) == 0 {
					_, _ = fmt.Fprintf(out, "✓ monitor %q\n", name)
					continue
				}
				problems += len(probs)
				_, _ = fmt.Fprintf(out, "✗ monitor %q\n", name)
				for _, p := range probs {
					_, _ = fmt.Fprintf(out, "  - %s\n", p)
				}
			}
			_, _ = fmt.Fprintf(out, "%d monitor(s) checked, %d problem(s).\n", total, problems)
			if !datasetsChecked {
				_, _ = fmt.Fprintln(out, "note: dataset-existence checks skipped (account not reachable).")
			}
			if problems > 0 {
				return clierr.New("monitor_check_failed", fmt.Sprintf("%d problem(s) in %d monitor(s)", problems, total))
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "monitor definition JSON file, or - for stdin (repeatable)")
	return cmd
}

func readMonitorInput(cmd *cobra.Command, path string) (map[string]any, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		data, err = os.ReadFile(path) // #nosec G304 -- user-provided lint target
	}
	if err != nil {
		return nil, clierr.New("usage_input_file", err.Error())
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, clierr.New("usage_input_file", fmt.Sprintf("%s: not valid JSON: %v", path, err))
	}
	return body, nil
}

// monitorCheckDatasets fetches the account's dataset ids; second return
// is false when the lookup failed (offline lint keeps working).
func monitorCheckDatasets(ctx context.Context, app *App) (map[string]bool, bool) {
	ds, err := listDatasets(ctx, app)
	if err != nil {
		return nil, false
	}
	ids := make(map[string]bool, len(ds))
	for _, d := range ds {
		ids[d.id] = true
	}
	return ids, true
}

// lintMonitor returns human-readable problems for one CreateMonitorRequest.
func lintMonitor(body map[string]any, knownDatasets map[string]bool, datasetsChecked bool) []string {
	var probs []string
	need := func(field string) bool {
		if _, ok := body[field]; !ok {
			probs = append(probs, fmt.Sprintf("missing required field %q", field))
			return false
		}
		return true
	}
	need("name")
	if need("comparison_operator") {
		if op, _ := body["comparison_operator"].(string); !monitorComparisonOps[op] {
			probs = append(probs, fmt.Sprintf("comparison_operator %q not one of BELOW/BELOW_OR_EQUAL/ABOVE/ABOVE_OR_EQUAL/EQUAL/NOT_EQUAL", op))
		}
	}
	need("threshold")
	if need("window") {
		probs = append(probs, lintMonitorWindow(body["window"])...)
	}
	if need("actions") {
		probs = append(probs, lintMonitorActions(body["actions"])...)
	}
	if need("queries") {
		probs = append(probs, lintMonitorQueries(body["queries"], knownDatasets, datasetsChecked)...)
	}
	return probs
}

func lintMonitorWindow(v any) []string {
	w, _ := v.(string)
	m := monitorWindowRe.FindStringSubmatch(w)
	if m == nil {
		return []string{fmt.Sprintf("window %q must look like \"Last 10 minutes\" / \"Last 2 hours\" / \"Last 1 days\"", w)}
	}
	n, _ := strconv.Atoi(m[1])
	minutes := n
	switch {
	case strings.HasPrefix(m[2], "hour"):
		minutes = n * 60
	case strings.HasPrefix(m[2], "day"):
		minutes = n * 60 * 24
	}
	if minutes < 5 {
		return []string{fmt.Sprintf("window %q below the 5-minute minimum", w)}
	}
	if minutes > 60*24 {
		return []string{fmt.Sprintf("window %q above the one-day maximum", w)}
	}
	return nil
}

func lintMonitorActions(v any) []string {
	actions, ok := v.([]any)
	if !ok || len(actions) == 0 {
		return []string{"actions must be a non-empty array"}
	}
	var probs []string
	for i, a := range actions {
		m, ok := a.(map[string]any)
		if !ok {
			probs = append(probs, fmt.Sprintf("actions[%d]: not an object", i))
			continue
		}
		typ, _ := m["type"].(string)
		switch typ {
		case "EMAIL":
			if e, _ := m["email"].(string); e == "" {
				probs = append(probs, fmt.Sprintf("actions[%d]: EMAIL action needs an email", i))
			}
		case "INTEGRATION":
		default:
			probs = append(probs, fmt.Sprintf("actions[%d]: type %q not EMAIL or INTEGRATION", i, typ))
		}
	}
	return probs
}

func lintMonitorQueries(v any, knownDatasets map[string]bool, datasetsChecked bool) []string {
	queries, ok := v.([]any)
	if !ok || len(queries) == 0 {
		return []string{"queries must be a non-empty array"}
	}
	var probs []string
	for i, q := range queries {
		m, ok := q.(map[string]any)
		if !ok {
			probs = append(probs, fmt.Sprintf("queries[%d]: not an object", i))
			continue
		}
		if n, _ := m["name"].(string); n == "" {
			probs = append(probs, fmt.Sprintf("queries[%d]: missing name", i))
		}
		if sel, ok := m["select"].([]any); !ok || len(sel) == 0 {
			probs = append(probs, fmt.Sprintf("queries[%d]: select must be a non-empty array", i))
		}
		where, hasWhere := m["where"].(string)
		if !hasWhere {
			probs = append(probs, fmt.Sprintf("queries[%d]: missing where", i))
		} else if where != "" {
			if _, err := query.Parse(where); err != nil {
				var pe *query.ParseError
				if errors.As(err, &pe) {
					probs = append(probs, fmt.Sprintf("queries[%d].where: %s\n      %s",
						i, pe.Msg, strings.ReplaceAll(pe.Caret(where), "\n", "\n      ")))
				} else {
					probs = append(probs, fmt.Sprintf("queries[%d].where: %v", i, err))
				}
			}
		}
		from, hasFrom := m["from"].([]any)
		_, hasFromExpr := m["from_expr"].(string)
		if !hasFrom && !hasFromExpr {
			probs = append(probs, fmt.Sprintf("queries[%d]: needs from (log ids) or from_expr", i))
		}
		if hasFrom && datasetsChecked {
			for _, f := range from {
				id, _ := f.(string)
				if id != "" && !knownDatasets[id] {
					probs = append(probs, fmt.Sprintf("queries[%d].from: dataset %s not found in this account", i, id))
				}
			}
		}
	}
	return probs
}
