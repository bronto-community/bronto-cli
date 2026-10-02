package cli

import "strings"

// EndpointPattern maps one management-API path pattern to the CLI command
// that calls it. A "{*}" segment matches any single path parameter, so
// "/monitors/{*}" covers the spec's "/monitors/{monitorId}". Consumed as
// JSON (via internal/tools/endpointmap) by spec-sync's digest
// (scripts/spec-digest.sh) to classify upstream spec changes into
// "covered by a CLI command" vs "no CLI coverage".
type EndpointPattern struct {
	Pattern string `json:"pattern"`
	Command string `json:"command"`
}

// handWrittenEndpoints lists the management-API paths used by hand-written
// commands — everything outside the resourceRegistry factory (which
// EndpointInventory expands mechanically). The ingestion host (bronto
// send) is deliberately absent: it is a separate API surface not described
// by api/openapi.yaml. TestEndpointInventoryMatchesSpec asserts every
// pattern here still resolves against the vendored spec (or the
// documented specLiveButUndocumented set), so this table can't silently
// rot when endpoints move. TestEveryCommandIsInventoried guards the other
// direction: it runs each hand-written command against a recording server
// and fails if a path it calls isn't listed here under that command, so
// spec-sync can't under-report coverage. Shell-completion lookups (GET
// /logs, /top-keys) are not commands and aren't listed.
//
// A Command names one or more commands separated by " / ". Each is a
// command path with the "bronto " prefix dropped after the first, and it
// covers that command and all of its subcommands ("traces" covers
// "bronto traces show").
var handWrittenEndpoints = []EndpointPattern{
	{Pattern: "/search", Command: "bronto search / tail / traces / ask / repl"},
	{Pattern: "/context", Command: "bronto context"},
	{Pattern: "/top-keys", Command: "bronto fields / ask / query check / search / repl"},
	{Pattern: "/usage", Command: "bronto usage"},
	// GET /logs is the health probe (ping, auth) and also how every
	// command that takes a dataset name resolves it to an id.
	{Pattern: "/logs", Command: "bronto ping / auth login / auth status / login / ask / context / fields / " +
		"query check / search / tail / repl / monitors check / exports create"},
	{Pattern: "/saved-searches", Command: "bronto search"},
	{Pattern: "/saved-searches/{*}", Command: "bronto search"},
	{Pattern: "/organizations", Command: "bronto search"},
	{Pattern: "/monitors/{*}/events", Command: "bronto monitors events"},
	{Pattern: "/monitors/{*}/status", Command: "bronto monitors mute"},
	{Pattern: "/groups/{*}/members", Command: "bronto groups members / groups add-members / groups remove-members"},
	// add-members/remove-members resolve --users emails through the users list.
	{Pattern: "/users", Command: "bronto groups add-members / groups remove-members"},
	{Pattern: "/users/{*}/groups", Command: "bronto users groups"},
	// users groups names the bare group ids it gets back via the groups list.
	{Pattern: "/groups", Command: "bronto users groups"},
	{Pattern: "/monitors/{*}/notifications", Command: "bronto monitors notifications"},
	{Pattern: "/metrics/{*}/top-keys", Command: "bronto metrics top-keys"},
	{Pattern: "/datasets/{*}/parser", Command: "bronto datasets parser"},
	// parser set resolves the parser name through the parsers list.
	{Pattern: "/parsers", Command: "bronto datasets parser set"},
	{Pattern: "/users/{*}/deactivate", Command: "bronto users deactivate"},
	{Pattern: "/users/{*}/reactivate", Command: "bronto users reactivate"},
	{Pattern: "/users/{*}/resend-invite", Command: "bronto users resend-invite"},
	{Pattern: "/dashboards/{*}/detach-from-template", Command: "bronto dashboards detach-from-template"},
	{Pattern: "/dashboards/{*}/widgets", Command: "bronto dashboards attach-widgets"},
	{Pattern: "/dashboards/{*}/widgets/{*}", Command: "bronto dashboards remove-widget"},
	{Pattern: "/widgets/{*}/widgets", Command: "bronto widgets attach-widgets"},
	{Pattern: "/widgets/{*}/widgets/{*}", Command: "bronto widgets remove-widget"},
}

// EndpointInventory returns every management-API path pattern the CLI
// calls: the resourceRegistry's generated verbs plus handWrittenEndpoints.
// A pattern shared by several commands (e.g. "/logs" serves both bronto
// datasets and bronto ping) gets its commands joined into one entry.
func EndpointInventory() []EndpointPattern {
	var out []EndpointPattern
	seen := map[string]int{}
	add := func(pattern, command string) {
		if pattern == "" {
			return
		}
		if i, dup := seen[pattern]; dup {
			if !strings.Contains(out[i].Command, command) {
				out[i].Command += " / " + command
			}
			return
		}
		seen[pattern] = len(out)
		out = append(out, EndpointPattern{Pattern: pattern, Command: command})
	}
	for _, d := range resourceRegistry {
		cmd := "bronto " + d.display()
		add(d.Base, cmd)
		add(d.createPath(), cmd)
		if !d.NoGet || !d.NoUpdate || !d.NoDelete {
			add(d.idBase()+"/{*}", cmd)
		}
	}
	for _, e := range handWrittenEndpoints {
		add(e.Pattern, e.Command)
	}
	return out
}
