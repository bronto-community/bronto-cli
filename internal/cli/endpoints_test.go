package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestEndpointInventoryShape(t *testing.T) {
	inv := EndpointInventory()
	byPattern := map[string]string{}
	for _, e := range inv {
		if e.Pattern == "" || e.Command == "" {
			t.Fatalf("inventory entry with empty field: %+v", e)
		}
		if _, dup := byPattern[e.Pattern]; dup {
			t.Fatalf("duplicate pattern %q — commands must merge into one entry", e.Pattern)
		}
		byPattern[e.Pattern] = e.Command
	}

	// Registry expansion: every resource contributes its Base, and a per-ID
	// pattern unless it is list-only.
	for _, d := range resourceRegistry {
		if byPattern[d.Base] == "" {
			t.Errorf("registry Base %q missing from inventory", d.Base)
		}
		if d.NoGet && d.NoUpdate && d.NoDelete {
			continue
		}
		if byPattern[d.idBase()+"/{*}"] == "" {
			t.Errorf("registry per-ID pattern %q missing from inventory", d.idBase()+"/{*}")
		}
	}

	// A pattern shared by several commands merges (e.g. /logs is datasets'
	// Base and ping's health-check endpoint).
	if c := byPattern["/logs"]; !strings.Contains(c, "datasets") || !strings.Contains(c, "ping") {
		t.Errorf("/logs command = %q, want both datasets and ping", c)
	}
}

// endpointProbes drives every hand-written command through its
// API-calling branches against a recording server. Several argument lists
// per command reach different branches (a dataset name triggers GET /logs
// resolution, --eq triggers /top-keys field lookup, and so on). A new
// hand-written command fails TestEveryCommandIsInventoried until it has an
// entry here, in unprobedCommands, or in commandsWithoutSpecEndpoints.
var endpointProbes = map[string][][]string{
	"bronto ask":                             {{"ask", "q", "--yes"}, {"ask", "q", "-d", "ds", "--yes"}},
	"bronto auth status":                     {{"auth", "status"}},
	"bronto context":                         {{"context", "-d", "ds", "--sequence", "1", "--timestamp", "1"}},
	"bronto fields":                          {{"fields"}, {"fields", "-d", "ds"}},
	"bronto ping":                            {{"ping"}},
	"bronto query check":                     {{"query", "check", "a = 1", "-d", "ds"}},
	"bronto search":                          {{"search", "-d", "ds", "--eq", "a=1"}, {"search", "--saved", "s1"}, {"search", "--url", "-d", "ds"}},
	"bronto tail":                            {{"tail", "-d", "ds"}},
	"bronto traces aggregate":                {{"traces", "aggregate", "--by", "service"}},
	"bronto traces list":                     {{"traces", "list"}},
	"bronto traces operations":               {{"traces", "operations"}},
	"bronto traces services":                 {{"traces", "services"}},
	"bronto traces shape":                    {{"traces", "shape"}},
	"bronto traces show":                     {{"traces", "show", "abc"}},
	"bronto usage":                           {{"usage"}},
	"bronto monitors check":                  {{"monitors", "check", "--input", "@MONITOR"}},
	"bronto monitors events":                 {{"monitors", "events", probeID}},
	"bronto monitors mute":                   {{"monitors", "mute", probeID}},
	"bronto exports create":                  {{"exports", "create", "-d", "ds", "--since", "1h"}},
	"bronto groups members":                  {{"groups", "members", probeID}},
	"bronto users deactivate":                {{"users", "deactivate", probeID}},
	"bronto users reactivate":                {{"users", "reactivate", probeID}},
	"bronto users resend-invite":             {{"users", "resend-invite", probeID}},
	"bronto dashboards attach-widgets":       {{"dashboards", "attach-widgets", probeID, "--widget-ids", probeID}},
	"bronto dashboards remove-widget":        {{"dashboards", "remove-widget", probeID, probeID}},
	"bronto dashboards detach-from-template": {{"dashboards", "detach-from-template", probeID}},
	"bronto widgets attach-widgets":          {{"widgets", "attach-widgets", probeID, "--widget-ids", probeID}},
	"bronto widgets remove-widget":           {{"widgets", "remove-widget", probeID, probeID}},
}

// unprobedCommands can't be driven in-process; their inventory entries are
// maintained by hand.
var unprobedCommands = map[string]string{
	"bronto repl":       "requires a terminal (GET /logs, /top-keys and POST /search are inventoried by hand)",
	"bronto auth login": "stores the key in the OS keychain (its region probe, GET /logs, is inventoried by hand)",
	"bronto login":      "alias for auth login",
	"bronto api":        "generic passthrough to any path",
	"bronto send":       "ingestion host, a separate API not described by the spec",
}

// commandsWithoutSpecEndpoints are probed too, and must make no request.
var commandsWithoutSpecEndpoints = map[string][]string{
	"bronto auth logout":  {"auth", "logout"},
	"bronto auth switch":  {"auth", "switch", "nope"},
	"bronto auth token":   {"auth", "token"},
	"bronto config get":   {"config", "get", "region"},
	"bronto config list":  {"config", "list"},
	"bronto config set":   {"config", "set", "region", "eu"},
	"bronto version":      {"version"},
	"bronto plugins list": {"plugins", "list"},
}

const (
	probeDataset = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000d1"
	probeID      = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000d2"
)

// recordRequests runs one command against a stub API and returns the
// "METHOD /path" of every request it made. The stub answers just enough
// for name resolution to succeed (dataset "ds", saved search "s1"), and
// --ask_url points at a fake LLM on the same server, excluded from the
// result. Errors are ignored: a command that fails after its first calls
// has still shown which endpoints it uses.
func recordRequests(t *testing.T, args []string) []string {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__llm" {
			_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"query\":\"x\",\"dataset\":\"ds\",\"since\":\"1h\"}"}}]}`)
			return
		}
		mu.Lock()
		seen[r.Method+" "+r.URL.Path] = true
		mu.Unlock()
		switch r.URL.Path {
		case "/logs":
			_, _ = fmt.Fprintf(w, `{"logs":[{"log_id":%q,"log":"ds","collection":"c"}]}`, probeDataset)
		case "/saved-searches":
			_, _ = fmt.Fprintf(w, `{"saved_searches":[{"id":%q,"name":"s1"}]}`, probeID)
		default:
			_, _ = fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("BRONTO_CONFIG_DIR", dir)
	t.Setenv("BRONTO_ASK_URL", srv.URL+"/__llm")
	monitor := filepath.Join(dir, "monitor.json")
	if err := os.WriteFile(monitor, []byte(`{"name":"m","log_ids":["`+probeDataset+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	full := make([]string, 0, len(args)+4)
	for _, a := range args {
		full = append(full, strings.ReplaceAll(a, "@MONITOR", monitor))
	}
	full = append(full, "--base-url", srv.URL, "--api-key", "k")

	root := NewRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(full)
	// tail polls until cancelled; the timeout bounds it.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = root.ExecuteContext(ctx)

	mu.Lock()
	defer mu.Unlock()
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// patternMatches reports whether a concrete path fits an inventory
// pattern, where each "{*}" segment matches one path segment.
func patternMatches(pattern, path string) bool {
	ps, xs := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(ps) != len(xs) {
		return false
	}
	for i := range ps {
		if ps[i] != "{*}" && ps[i] != xs[i] {
			return false
		}
	}
	return true
}

// commandNames expands an inventory Command string into full command
// paths: "bronto search / tail" → "bronto search", "bronto tail".
func commandNames(command string) []string {
	var out []string
	for _, c := range strings.Split(command, " / ") {
		if !strings.HasPrefix(c, "bronto ") {
			c = "bronto " + c
		}
		out = append(out, c)
	}
	return out
}

// coveredBy reports whether cmdPath is one of names, or a subcommand of one
// ("bronto traces show" is covered by "bronto traces").
func coveredBy(cmdPath string, names []string) bool {
	for _, n := range names {
		if cmdPath == n || strings.HasPrefix(cmdPath, n+" ") {
			return true
		}
	}
	return false
}

// TestEveryCommandIsInventoried is the guard for #121. groups members and
// the users verb-actions shipped without inventory entries, so spec-sync
// reported their endpoints as having "no CLI coverage". A hand-maintained
// list of which command calls what kept missing shared helpers (every
// command taking a dataset name calls GET /logs), so this records what
// each command actually requests and checks the inventory against it.
func TestEveryCommandIsInventoried(t *testing.T) {
	inv := EndpointInventory()

	// 1. Every leaf command is accounted for.
	generated := map[string]bool{}
	for _, d := range resourceRegistry {
		for _, verb := range []string{"list", "get", "create", "update", "delete"} {
			generated["bronto "+d.display()+" "+verb] = true
		}
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if !sub.Hidden && sub.Name() != "help" {
				walk(sub)
			}
		}
		if c.HasSubCommands() || !c.Runnable() {
			return
		}
		p := c.CommandPath()
		_, probed := endpointProbes[p]
		_, unprobed := unprobedCommands[p]
		_, none := commandsWithoutSpecEndpoints[p]
		if !probed && !unprobed && !none && !generated[p] {
			t.Errorf("%s has no entry in endpointProbes, unprobedCommands or commandsWithoutSpecEndpoints", p)
		}
	}
	walk(NewRootCmd())

	// 2. Every request a probed command makes is inventoried under it.
	for cmdPath, runs := range endpointProbes {
		for _, args := range runs {
			for _, req := range recordRequests(t, args) {
				path := strings.SplitN(req, " ", 2)[1]
				ok := false
				for _, e := range inv {
					if patternMatches(e.Pattern, path) && coveredBy(cmdPath, commandNames(e.Command)) {
						ok = true
						break
					}
				}
				if !ok {
					t.Errorf("%s (%s) calls %s, which is not inventoried under %q in handWrittenEndpoints",
						cmdPath, strings.Join(args, " "), req, cmdPath)
				}
			}
		}
	}

	// 3. Commands declared endpoint-free really make no request.
	for cmdPath, args := range commandsWithoutSpecEndpoints {
		if args == nil {
			continue
		}
		if reqs := recordRequests(t, args); len(reqs) > 0 {
			t.Errorf("%s is declared endpoint-free but called %v", cmdPath, reqs)
		}
	}

	// 4. Every command label spec-sync will print names a real command,
	// registry-generated ones included.
	for _, e := range inv {
		for _, c := range commandNames(e.Command) {
			cmd, _, err := NewRootCmd().Find(strings.Fields(c)[1:])
			if err != nil || cmd.CommandPath() != c {
				t.Errorf("inventory pattern %s names %q, which is not a command", e.Pattern, c)
			}
		}
	}
}
