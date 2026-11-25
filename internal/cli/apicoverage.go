package cli

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// API coverage: which bronto command calls each operation (method + path)
// in api/openapi.yaml. internal/tools/docgen renders it as
// docs/cli/reference/api-coverage.mdx.
//
// Commands come from two places:
//   - the resourceRegistry, expanded mechanically: list is GET Base,
//     create POST CreatePath, get/update/delete GET/UpdateMethod/DELETE
//     IDBase/{id} (registryOperations);
//   - apiOperationCommands below, for the hand-written commands.
//
// Every spec operation must be covered by one of them or listed in
// apiOperationsNotCovered, so a re-vendored spec with a new operation fails
// APICoverage (and with it TestAPICoverage and `make docs-reference`) until
// someone decides which command covers it. An endpoint a command calls only
// as a helper (resolving a dataset or user name, reading ownership tags)
// does not count: the operation is covered only when a command exposes
// what it does. TestAPICoverageMatchesRequests checks each hand-written
// entry against the requests the command really makes.

// apiOperationCommands maps a spec operation ("METHOD /path" with the
// spec's own parameter names) to the hand-written commands that expose it.
// A command group ("bronto traces") stands for all of its subcommands.
var apiOperationCommands = map[string][]string{
	"POST /search":                                        {"bronto search", "bronto tail", "bronto traces", "bronto ask", "bronto repl"},
	"GET /context":                                        {"bronto context"},
	"GET /top-keys":                                       {"bronto fields"},
	"GET /users/{userId}/groups":                          {"bronto users groups"},
	"POST /users/{userId}/deactivate":                     {"bronto users deactivate"},
	"POST /users/{userId}/reactivate":                     {"bronto users reactivate"},
	"POST /users/{userId}/resend-invite":                  {"bronto users resend-invite"},
	"GET /groups/{groupId}/members":                       {"bronto groups members"},
	"POST /groups/{groupId}/members":                      {"bronto groups add-members"},
	"DELETE /groups/{groupId}/members":                    {"bronto groups remove-members"},
	"GET /datasets/{datasetId}/parser":                    {"bronto datasets parser get"},
	"PUT /datasets/{datasetId}/parser":                    {"bronto datasets parser set"},
	"DELETE /datasets/{datasetId}/parser":                 {"bronto datasets parser unset"},
	"GET /logs/{logId}/monitors":                          {"bronto datasets monitors"},
	"GET /logs/{logId}/dashboards":                        {"bronto datasets dashboards"},
	"GET /monitors/{monitorId}/events":                    {"bronto monitors events"},
	"GET /monitors/{monitorId}/notifications":             {"bronto monitors notifications"},
	"POST /monitors/{monitorId}/status":                   {"bronto monitors mute"},
	"POST /forward-configs/test-destination":              {"bronto forward-configs test"},
	"POST /widgets/{id}/widgets":                          {"bronto widgets attach-widgets"},
	"DELETE /widgets/{id}/widgets/{widgetId}":             {"bronto widgets remove-widget"},
	"POST /dashboards/{dashboardId}/detach-from-template": {"bronto dashboards detach-from-template"},
	"POST /dashboards/{dashboardId}/widgets":              {"bronto dashboards attach-widgets"},
	"DELETE /dashboards/{dashboardId}/widgets/{widgetId}": {"bronto dashboards remove-widget"},
	"GET /metrics/{metric_id}/top-keys":                   {"bronto metrics top-keys"},
	"GET /timeseries/{metric_definition_id}":              {"bronto metrics definitions query", "bronto widgets query"},
	"POST /timeseries/search":                             {"bronto metrics query", "bronto dashboards validate"},
}

// apiOperationsNotCovered marks the spec operations no command exposes;
// `bronto api` reaches them. The value is an optional short reason or
// pointer, shown in the Notes column.
var apiOperationsNotCovered = map[string]string{
	"GET /users/all":                    "",
	"GET /users/{userId}/export":        "",
	"GET /users/{userId}/preferences":   "",
	"PATCH /users/{userId}/preferences": "",
	"GET /users/{userId}/access":        "",
	"GET /groups/{groupId}/export":      "",
	"GET /search":                       "Same query as `POST /search`, which `bronto search` uses.",
	"GET /live-tail":                    "`bronto tail` polls `POST /search` instead.",
	"POST /live-tail":                   "`bronto tail` polls `POST /search` instead.",
	"POST /logs":                        "`bronto datasets create` uses `POST /datasets`.",
	"GET /datasets":                     "`bronto datasets list` reads the same datasets from `GET /logs`.",
	"POST /datasets/resolve":            "",
	"POST /organizations/{orgId}/access/{entityType}/{entityId}":      "",
	"DELETE /organizations/{orgId}/access/{entityType}/{entityId}":    "",
	"GET /organizations/{orgId}/access":                               "",
	"GET /organizations/{orgId}/access/{entityType}/{entityId}/check": "",
	"POST /organizations/{orgId}/access/user/{userId}/switch":         "",
	"GET /monitors/{monitorId}/export":                                "",
	"PATCH /widgets/{id}":                                             "`bronto widgets update` replaces the widget with `PUT`.",
	"GET /dashboards/{dashboardId}/export":                            "`bronto dashboards export` writes a manifest for `bronto dashboards apply` instead.",
	"GET /integrations/slack/{slack_id}":                              "`bronto slack list` shows every Slack integration.",
	"GET /integrations/slack/{slack_id}/export":                       "",
	"GET /integrations/slack/workspaces":                              "",
	"DELETE /integrations/slack/workspaces/{workspace_id}":            "",
	"GET /integrations/webhooks/{webhook_id}":                         "`bronto webhooks list` shows every webhook.",
	"GET /integrations/webhooks/{webhook_id}/export":                  "",
	"GET /integrations/providers":                                     "",
	"POST /integrations/providers":                                    "",
	"GET /integrations/providers/{provider_id}":                       "",
	"PUT /integrations/providers/{provider_id}":                       "",
	"DELETE /integrations/providers/{provider_id}":                    "",
	"GET /integrations/providers/{provider_id}/export":                "",
	"GET /sso/idps/{idp_name}/export":                                 "",
	"GET /sso/mappings/export":                                        "",
	"GET /customer":                                                   "",
	"PATCH /customer":                                                 "",
	"PUT /tags":                                                       "`bronto dashboards apply` tags the dashboards it manages, but no command manages tags.",
	"POST /tags":                                                      "`bronto dashboards apply` tags the dashboards it manages, but no command manages tags.",
	"DELETE /tags":                                                    "`bronto dashboards apply` tags the dashboards it manages, but no command manages tags.",
	"GET /tags/search":                                                "",
	"GET /explain-error":                                              "",
	"GET /widget-creation-assistant":                                  "",
	"GET /policies/resources/{resource_type}/{resource_id}":           "",
	"POST /patterns":                                                  "`bronto search --patterns` clusters events into patterns locally.",
}

// apiOperationNotes adds a note to covered operations.
var apiOperationNotes = map[string]string{
	"POST /monitors/{monitorId}/status": "Mutes until a time, or unmutes with `--unmute`.",
	"POST /timeseries/search":           "`bronto dashboards validate` calls it with `--live`.",
}

// apiOperationsOutsideSpec lists the endpoints hand-written commands call
// that the vendored spec does not describe (see specLiveButUndocumented),
// as "METHOD /path" with "{*}" for a parameter. Registry resources on such
// endpoints are found mechanically.
var apiOperationsOutsideSpec = map[string][]string{
	"GET /usage":                              {"bronto usage"},
	"POST /dashboards/templates/{*}/preview":  {"bronto dashboards templates show"},
	"POST /dashboards/templates/{*}/instance": {"bronto dashboards create"},
}

// SpecOperation is one operation of the OpenAPI document.
type SpecOperation struct {
	Method     string // upper case, e.g. "GET"
	Path       string // as in the spec, e.g. "/monitors/{monitorId}"
	Tags       []string
	Summary    string
	Deprecated bool
}

// Key is the operation as "METHOD /path".
func (o SpecOperation) Key() string { return o.Method + " " + o.Path }

// APICoverageRow is one operation and the commands that expose it.
type APICoverageRow struct {
	SpecOperation
	// Group is the spec tag the operation is listed under.
	Group string
	// Commands are full command paths ("bronto monitors list"); empty when
	// only `bronto api` reaches the operation.
	Commands []string
	Note     string
}

// APICoverageReport is the API coverage page's content.
type APICoverageReport struct {
	// Operations are the spec's operations in document order.
	Operations []APICoverageRow
	// OutsideSpec are endpoints commands call that the spec does not
	// describe, sorted by path; Path uses "{id}" for a parameter.
	OutsideSpec []APICoverageRow
}

var specMethods = []string{"get", "put", "post", "delete", "patch", "head", "options", "trace"}

// ParseSpecOperations returns the operations of an OpenAPI document in
// document order.
func ParseSpecOperations(data []byte) ([]SpecOperation, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("empty document")
	}
	paths := mappingValue(doc.Content[0], "paths")
	if paths == nil || paths.Kind != yaml.MappingNode || len(paths.Content) == 0 {
		return nil, errors.New("missing or empty top-level paths map")
	}
	var ops []SpecOperation
	for i := 0; i+1 < len(paths.Content); i += 2 {
		path, item := paths.Content[i].Value, paths.Content[i+1]
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("path %q is not a mapping of operations", path)
		}
		for j := 0; j+1 < len(item.Content); j += 2 {
			method := item.Content[j].Value
			if !slices.Contains(specMethods, method) {
				continue // parameters, summary, servers, ...
			}
			var op struct {
				Tags       []string `yaml:"tags"`
				Summary    string   `yaml:"summary"`
				Deprecated bool     `yaml:"deprecated"`
			}
			if err := item.Content[j+1].Decode(&op); err != nil {
				return nil, fmt.Errorf("%s %s: %w", strings.ToUpper(method), path, err)
			}
			ops = append(ops, SpecOperation{
				Method: strings.ToUpper(method), Path: path,
				Tags: op.Tags, Summary: op.Summary, Deprecated: op.Deprecated,
			})
		}
	}
	return ops, nil
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// registryOperation is one generated resource verb's request.
type registryOperation struct {
	Method, Pattern, Command string
}

// registryOperations expands the resourceRegistry into the request each
// generated verb makes. A verb a resource replaces with a hand-written
// command (exports create, dashboards create, metrics definitions get)
// still calls the same endpoint.
func registryOperations() []registryOperation {
	var out []registryOperation
	for _, d := range resourceRegistry {
		cmd := "bronto " + d.display() + " "
		id := d.idBase() + "/{*}"
		out = append(out, registryOperation{http.MethodGet, d.Base, cmd + "list"})
		if !d.NoGet {
			out = append(out, registryOperation{http.MethodGet, id, cmd + "get"})
		}
		if !d.NoCreate {
			out = append(out, registryOperation{http.MethodPost, d.createPath(), cmd + "create"})
		}
		if !d.NoUpdate {
			out = append(out, registryOperation{d.updateMethod(), id, cmd + "update"})
		}
		if !d.NoDelete {
			out = append(out, registryOperation{http.MethodDelete, id, cmd + "delete"})
		}
	}
	return out
}

// normalizeParams rewrites every {param} segment to a bare {} so patterns
// and spec paths compare regardless of parameter naming ("/monitors/{*}"
// vs "/monitors/{monitorId}"). Mirrors the `norm` helper in
// scripts/spec-digest.sh — keep the two in sync.
func normalizeParams(p string) string {
	var b strings.Builder
	for {
		open := strings.IndexByte(p, '{')
		if open < 0 {
			b.WriteString(p)
			return b.String()
		}
		closing := strings.IndexByte(p[open:], '}')
		if closing < 0 {
			b.WriteString(p)
			return b.String()
		}
		b.WriteString(p[:open])
		b.WriteString("{}")
		p = p[open+closing+1:]
	}
}

// codeCommand matches a `bronto ...` code span in a note.
var codeCommand = regexp.MustCompile("`(bronto(?: [a-z][a-z-]*)+)")

// APICoverage maps every operation of the OpenAPI document spec to the
// commands that expose it. It fails when an operation is neither covered
// nor in apiOperationsNotCovered, when an entry names an operation the
// spec doesn't have, or when an entry names a command that doesn't exist.
func APICoverage(spec []byte) (APICoverageReport, error) {
	ops, err := ParseSpecOperations(spec)
	if err != nil {
		return APICoverageReport{}, fmt.Errorf("parsing the spec: %w", err)
	}
	var problems []string
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	root := NewRootCmd()
	checkCommand := func(where, c string) {
		found, _, err := root.Find(strings.Fields(c)[1:])
		if err != nil || found == root || found.CommandPath() != c {
			bad("%s names %q, which is not a command", where, c)
		}
	}

	specKeys := map[string]bool{}
	byNorm := map[string]string{} // "METHOD /path/{}" -> spec key
	for _, op := range ops {
		specKeys[op.Key()] = true
		byNorm[op.Method+" "+normalizeParams(op.Path)] = op.Key()
	}

	commands := map[string][]string{}
	var outside []APICoverageRow
	for _, r := range registryOperations() {
		if key, ok := byNorm[r.Method+" "+normalizeParams(r.Pattern)]; ok {
			commands[key] = append(commands[key], r.Command)
			continue
		}
		outside = append(outside, APICoverageRow{
			SpecOperation: SpecOperation{Method: r.Method, Path: r.Pattern},
			Commands:      []string{r.Command},
		})
	}
	for key, cmds := range apiOperationCommands {
		if !specKeys[key] {
			bad("apiOperationCommands: %q is not an operation in the spec", key)
		}
		for _, c := range cmds {
			checkCommand("apiOperationCommands["+key+"]", c)
			if !slices.Contains(commands[key], c) {
				commands[key] = append(commands[key], c)
			}
		}
	}
	for key, note := range apiOperationsNotCovered {
		if !specKeys[key] {
			bad("apiOperationsNotCovered: %q is not an operation in the spec", key)
		}
		if len(commands[key]) > 0 {
			bad("apiOperationsNotCovered: %q is covered by %s; remove the entry", key, strings.Join(commands[key], ", "))
		}
		for _, m := range codeCommand.FindAllStringSubmatch(note, -1) {
			checkCommand("apiOperationsNotCovered["+key+"]", m[1])
		}
	}
	for key, note := range apiOperationNotes {
		if !specKeys[key] {
			bad("apiOperationNotes: %q is not an operation in the spec", key)
		}
		if _, nc := apiOperationsNotCovered[key]; nc {
			bad("apiOperationNotes: %q is not covered; put the note in apiOperationsNotCovered", key)
		}
		for _, m := range codeCommand.FindAllStringSubmatch(note, -1) {
			checkCommand("apiOperationNotes["+key+"]", m[1])
		}
	}
	for key, cmds := range apiOperationsOutsideSpec {
		method, pattern, _ := strings.Cut(key, " ")
		if spec, ok := byNorm[method+" "+normalizeParams(pattern)]; ok {
			bad("apiOperationsOutsideSpec: %q is the spec operation %q; move it to apiOperationCommands", key, spec)
		}
		for _, c := range cmds {
			checkCommand("apiOperationsOutsideSpec["+key+"]", c)
		}
		outside = append(outside, APICoverageRow{
			SpecOperation: SpecOperation{Method: method, Path: pattern},
			Commands:      cmds,
		})
	}

	var rep APICoverageReport
	for _, op := range ops {
		key := op.Key()
		cmds := commands[key]
		note, notCovered := apiOperationsNotCovered[key]
		if len(cmds) == 0 && !notCovered {
			bad("%s has no command: add it to apiOperationCommands, or to apiOperationsNotCovered if only `bronto api` reaches it", key)
		}
		if n, ok := apiOperationNotes[key]; ok {
			note = n
		}
		rep.Operations = append(rep.Operations, APICoverageRow{
			SpecOperation: op, Group: primaryTag(op), Commands: cmds, Note: note,
		})
	}

	// One row per endpoint outside the spec, its commands merged.
	merged := map[string]int{}
	for _, r := range outside {
		r.Path = strings.ReplaceAll(r.Path, "{*}", "{id}")
		if i, ok := merged[r.Key()]; ok {
			rep.OutsideSpec[i].Commands = append(rep.OutsideSpec[i].Commands, r.Commands...)
			continue
		}
		merged[r.Key()] = len(rep.OutsideSpec)
		rep.OutsideSpec = append(rep.OutsideSpec, r)
	}
	slices.SortFunc(rep.OutsideSpec, func(a, b APICoverageRow) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return methodRank(a.Method) - methodRank(b.Method)
	})

	if len(problems) > 0 {
		slices.Sort(problems)
		return rep, fmt.Errorf("API coverage map (internal/cli/apicoverage.go) is out of date:\n  %s", strings.Join(problems, "\n  "))
	}
	return rep, nil
}

// methodRank orders methods as the spec usually does.
func methodRank(m string) int {
	return slices.Index([]string{"GET", "POST", "PUT", "PATCH", "DELETE"}, m)
}

// primaryTag picks the tag an operation is grouped under: the one named
// like the path's first segment when there is one ("monitors" for
// /monitors/downtimes, tagged downtimes and monitors), else the first.
func primaryTag(op SpecOperation) string {
	if len(op.Tags) == 0 {
		return "other"
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(op.Path, "/"), "/")
	if slices.Contains(op.Tags, first) {
		return first
	}
	return op.Tags[0]
}
