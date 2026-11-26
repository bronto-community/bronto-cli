package cli

import (
	"errors"
	"fmt"
	"net/http"
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
// Every spec operation must be covered by one of them, offered by a
// command through another endpoint (apiOperationsElsewhere), or listed as
// not supported yet (apiOperationsNotCovered), so a re-vendored spec with a new operation fails
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

// apiOperationsElsewhere maps spec operations no command calls, but whose
// capability a command offers through another endpoint, to those commands
// (`bronto search` runs the same query as GET /search through POST
// /search). The coverage page counts them as covered and lists the
// commands under the operation's category.
var apiOperationsElsewhere = map[string][]string{
	"GET /search":                             {"bronto search"},
	"GET /live-tail":                          {"bronto tail"},
	"POST /live-tail":                         {"bronto tail"},
	"POST /logs":                              {"bronto datasets create"},
	"GET /datasets":                           {"bronto datasets list"},
	"GET /integrations/slack/{slack_id}":      {"bronto slack list"},
	"GET /integrations/webhooks/{webhook_id}": {"bronto webhooks list"},
}

// apiOperationsNotCovered lists what the CLI doesn't support yet: spec
// operations no command offers in any form. The value says in a few words
// what a user can't do; the coverage page lists it under "Not supported
// yet", and `bronto api` reaches the endpoint meanwhile.
var apiOperationsNotCovered = map[string]string{
	"GET /users/all":                                                  "List all users",
	"GET /users/{userId}/export":                                      "Export a user",
	"GET /users/{userId}/preferences":                                 "Read a user's preferences",
	"PATCH /users/{userId}/preferences":                               "Change a user's preferences",
	"GET /users/{userId}/access":                                      "List the organizations a user can access",
	"GET /groups/{groupId}/export":                                    "Export a group",
	"POST /datasets/resolve":                                          "Resolve datasets",
	"POST /organizations/{orgId}/access/{entityType}/{entityId}":      "Grant a user or group access to an organization",
	"DELETE /organizations/{orgId}/access/{entityType}/{entityId}":    "Revoke a user's or group's access to an organization",
	"GET /organizations/{orgId}/access":                               "List who has access to an organization",
	"GET /organizations/{orgId}/access/{entityType}/{entityId}/check": "Check whether a user or group has access to an organization",
	"POST /organizations/{orgId}/access/user/{userId}/switch":         "Switch a user's active organization",
	"GET /monitors/{monitorId}/export":                                "Export a monitor",
	"GET /integrations/slack/{slack_id}/export":                       "Export a Slack integration",
	"GET /integrations/slack/workspaces":                              "List Slack workspaces",
	"DELETE /integrations/slack/workspaces/{workspace_id}":            "Remove a Slack workspace",
	"GET /integrations/webhooks/{webhook_id}/export":                  "Export a webhook",
	"GET /integrations/providers":                                     "List provider integrations, such as AWS DevOps Agent",
	"POST /integrations/providers":                                    "Create a provider integration",
	"GET /integrations/providers/{provider_id}":                       "Get a provider integration",
	"PUT /integrations/providers/{provider_id}":                       "Update a provider integration",
	"DELETE /integrations/providers/{provider_id}":                    "Delete a provider integration",
	"GET /integrations/providers/{provider_id}/export":                "Export a provider integration",
	"GET /sso/idps/{idp_name}/export":                                 "Export an SSO identity provider",
	"GET /sso/mappings/export":                                        "Export SSO mappings",
	"GET /customer":                                                   "Read the organization's customer record",
	"PATCH /customer":                                                 "Update the organization's customer record",
	"GET /tags/search":                                                "List tags",
	"POST /tags":                                                      "Create a tag",
	"PUT /tags":                                                       "Update a tag",
	"DELETE /tags":                                                    "Delete a tag",
	"GET /explain-error":                                              "Explain an error with AI",
	"GET /widget-creation-assistant":                                  "Suggest a widget from a prompt with AI",
	"GET /policies/resources/{resource_type}/{resource_id}":           "List the policies on a resource",
	"PATCH /widgets/{id}":                                             "Change a widget's layout",
	"GET /dashboards/{dashboardId}/export":                            "Export a dashboard as Terraform",
	"POST /patterns":                                                  "Get server-side log patterns and their templates",
}

// apiOperationsOutsideSpec lists the endpoints hand-written commands call
// that the vendored spec does not describe (see specLiveButUndocumented),
// as "METHOD /path" with "{*}" for a parameter. The coverage page doesn't
// show them, but TestAPICoverageMatchesRequests checks them, and an entry
// fails once the spec describes it. Registry resources on such endpoints
// are in registryOperationsOutsideSpec.
var apiOperationsOutsideSpec = map[string][]string{
	"GET /usage":                              {"bronto usage"},
	"POST /dashboards/templates/{*}/preview":  {"bronto dashboards templates show"},
	"POST /dashboards/templates/{*}/instance": {"bronto dashboards create"},
}

// registryOperationsOutsideSpec lists the generated resource verbs whose
// endpoint the vendored spec does not describe, as "METHOD /path" with
// "{*}" for a parameter. Every other registry verb must match a spec
// operation, so a verb the spec drops (a method removed while its path
// stays) fails APICoverage instead of moving to the outside-spec table.
var registryOperationsOutsideSpec = map[string]bool{
	"GET /dashboards/templates":       true,
	"GET /dashboards/templates/{*}":   true,
	"GET /logs/{*}":                   true,
	"PUT /logs/{*}":                   true,
	"DELETE /logs/{*}":                true,
	"GET /metrics/definitions":        true,
	"POST /metrics/definitions":       true,
	"GET /metrics/definitions/{*}":    true,
	"DELETE /metrics/definitions/{*}": true,
	"GET /parsers":                    true,
	"POST /parsers":                   true,
	"PATCH /parsers/{*}":              true,
	"DELETE /parsers/{*}":             true,
	"GET /saved-searches":             true,
	"POST /saved-searches":            true,
	"GET /saved-searches/{*}":         true,
	"PATCH /saved-searches/{*}":       true,
	"DELETE /saved-searches/{*}":      true,
	"GET /widgets/templates":          true,
	"GET /widgets/templates/{*}":      true,
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
	// Commands are full command paths ("bronto monitors list") that call
	// the operation.
	Commands []string
	// Elsewhere are commands that offer the operation's capability through
	// another endpoint (apiOperationsElsewhere).
	Elsewhere []string
	// Missing says what the CLI doesn't support yet when no command offers
	// the operation (apiOperationsNotCovered); empty otherwise.
	Missing string
}

// APICoverageReport is the API coverage page's content.
type APICoverageReport struct {
	// Operations are the spec's operations in document order.
	Operations []APICoverageRow
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

// APICoverage maps every operation of the OpenAPI document spec to the
// commands that expose it. It fails when an operation is not covered and
// in neither apiOperationsElsewhere nor apiOperationsNotCovered, when a
// generated resource verb has no spec operation and no exception, when an entry names an operation the
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
	registryKeys := map[string]bool{}
	for _, r := range registryOperations() {
		registryKeys[r.Method+" "+r.Pattern] = true
		excepted := registryOperationsOutsideSpec[r.Method+" "+r.Pattern]
		if key, ok := byNorm[r.Method+" "+normalizeParams(r.Pattern)]; ok {
			if excepted {
				bad("registryOperationsOutsideSpec: %q is the spec operation %q; remove the entry", r.Method+" "+r.Pattern, key)
			}
			commands[key] = append(commands[key], r.Command)
			continue
		}
		if !excepted {
			bad("%s (%s) is not an operation in the spec: fix the resource descriptor, or add it to registryOperationsOutsideSpec if the API serves it undocumented", r.Method+" "+r.Pattern, r.Command)
		}
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
	for key, cmds := range apiOperationsElsewhere {
		if !specKeys[key] {
			bad("apiOperationsElsewhere: %q is not an operation in the spec", key)
		}
		if len(commands[key]) > 0 {
			bad("apiOperationsElsewhere: %q is called by %s; remove the entry", key, strings.Join(commands[key], ", "))
		}
		if _, nc := apiOperationsNotCovered[key]; nc {
			bad("apiOperationsElsewhere: %q is also in apiOperationsNotCovered; keep one", key)
		}
		for _, c := range cmds {
			checkCommand("apiOperationsElsewhere["+key+"]", c)
		}
	}
	for key, missing := range apiOperationsNotCovered {
		if !specKeys[key] {
			bad("apiOperationsNotCovered: %q is not an operation in the spec", key)
		}
		if len(commands[key]) > 0 {
			bad("apiOperationsNotCovered: %q is covered by %s; remove the entry", key, strings.Join(commands[key], ", "))
		}
		if strings.TrimSpace(missing) == "" {
			bad("apiOperationsNotCovered: %q needs a short description of what isn't supported", key)
		}
	}
	for key := range registryOperationsOutsideSpec {
		if !registryKeys[key] {
			bad("registryOperationsOutsideSpec: %q is not a generated resource verb; remove the entry", key)
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
	}

	var rep APICoverageReport
	for _, op := range ops {
		key := op.Key()
		row := APICoverageRow{
			SpecOperation: op, Group: primaryTag(op), Commands: commands[key],
			Elsewhere: apiOperationsElsewhere[key], Missing: apiOperationsNotCovered[key],
		}
		if len(row.Commands) == 0 && len(row.Elsewhere) == 0 && row.Missing == "" {
			bad("%s has no command: add it to apiOperationCommands, to apiOperationsElsewhere if a command offers it through another endpoint, or to apiOperationsNotCovered if the CLI doesn't support it yet", key)
		}
		rep.Operations = append(rep.Operations, row)
	}

	if len(problems) > 0 {
		slices.Sort(problems)
		return rep, fmt.Errorf("API coverage map (internal/cli/apicoverage.go) is out of date:\n  %s", strings.Join(problems, "\n  "))
	}
	return rep, nil
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
