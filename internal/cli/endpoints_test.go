package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bronto-community/bronto-cli/internal/api"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
// hand-written command, including one that replaces or wraps a generated
// resource verb, fails TestEveryCommandIsInventoried until it has an entry
// here, in unprobedCommands, or in commandsWithoutSpecEndpoints.
var endpointProbes = map[string][][]string{
	"bronto ask":                             {{"ask", "q", "--yes"}, {"ask", "q", "-d", "ds", "--yes"}},
	"bronto auth status":                     {{"auth", "status"}},
	"bronto context":                         {{"context", "-d", "ds", "--sequence", "1", "--timestamp", "1"}},
	"bronto fields":                          {{"fields"}, {"fields", "-d", "ds"}},
	"bronto ping":                            {{"ping"}},
	"bronto org show":                        {{"org", "show"}},
	"bronto org update":                      {{"org", "update", "-f", "customer_name=x"}},
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
	"bronto sso idps export":                 {{"sso", "idps", "export", "Google"}},
	"bronto sso mappings export":             {{"sso", "mappings", "export"}},
	"bronto monitors check":                  {{"monitors", "check", "--input", "@MONITOR"}},
	"bronto monitors events":                 {{"monitors", "events", probeID}},
	"bronto monitors mute":                   {{"monitors", "mute", probeID}},
	"bronto exports create":                  {{"exports", "create", "-d", "ds", "--since", "1h"}},
	"bronto groups members":                  {{"groups", "members", probeID}},
	"bronto groups add-members":              {{"groups", "add-members", "g1", "--users", "alice@example.com", "--groups", "g2"}},
	"bronto groups remove-members":           {{"groups", "remove-members", probeID, "--users", "alice@example.com"}},
	"bronto users groups":                    {{"users", "groups", "alice@example.com"}},
	"bronto users access":                    {{"users", "access", "alice@example.com"}},
	"bronto access list":                     {{"access", "list"}, {"access", "list", "--org", probeID}},
	"bronto access check":                    {{"access", "check", "--user", "alice@example.com", "--org", probeID}},
	"bronto access grant":                    {{"access", "grant", "--group", "g1", "--roles", "Admin", "--org", probeID}, {"access", "grant", "--group", "g1", "--roles", probeID, "--org", probeID}},
	"bronto access revoke":                   {{"access", "revoke", "--user", probeID, "--yes", "--org", probeID}},
	"bronto monitors notifications":          {{"monitors", "notifications", "m1"}},
	"bronto metrics top-keys":                {{"metrics", "top-keys", "cpu"}},
	"bronto datasets parser get":             {{"datasets", "parser", "get", "ds"}},
	"bronto datasets parser set":             {{"datasets", "parser", "set", "ds", "p1"}},
	"bronto datasets parser unset":           {{"datasets", "parser", "unset", "ds"}},
	"bronto datasets monitors":               {{"datasets", "monitors", "ds"}},
	"bronto datasets dashboards":             {{"datasets", "dashboards", "ds"}},
	"bronto forward-configs test":            {{"forward-configs", "test", "f1"}, {"forward-configs", "test", "--bucket", "b"}},
	"bronto users deactivate":                {{"users", "deactivate", probeID}},
	"bronto users reactivate":                {{"users", "reactivate", probeID}},
	"bronto users resend-invite":             {{"users", "resend-invite", probeID}},
	"bronto dashboards attach-widgets":       {{"dashboards", "attach-widgets", probeID, "--widget-ids", probeID}},
	"bronto dashboards remove-widget":        {{"dashboards", "remove-widget", probeID, probeID}},
	"bronto dashboards detach-from-template": {{"dashboards", "detach-from-template", probeID}},
	"bronto widgets attach-widgets":          {{"widgets", "attach-widgets", probeID, "--widget-ids", probeID}},
	"bronto widgets remove-widget":           {{"widgets", "remove-widget", probeID, probeID}},
	"bronto metrics definitions query":       {{"metrics", "definitions", "query", probeID}, {"metrics", "definitions", "query", "d1"}},
	"bronto widgets query":                   {{"widgets", "query", probeID}},
	"bronto metrics query":                   {{"metrics", "query", "--select", "count(*)", "-d", "ds"}},
	"bronto dashboards templates show":       {{"dashboards", "templates", "show", probeID}, {"dashboards", "templates", "show", "t1"}},
	"bronto dashboards create": {{"dashboards", "create", "--template", probeID},
		{"dashboards", "create", "--template", "t1"}},
	// The first apply creates dashboard d and its widget; the second
	// changes d1's tags (add, update, remove), so between them every write
	// apply makes is recorded.
	"bronto dashboards apply":        {{"dashboards", "apply", "-f", "@MANIFEST"}, {"dashboards", "apply", "-f", "@UPDATE_MANIFEST"}},
	"bronto dashboards export":       {{"dashboards", "export", probeID}, {"dashboards", "export", probeID, "--format", "terraform"}},
	"bronto dashboards validate":     {{"dashboards", "validate", "-f", "@MANIFEST", "--live"}},
	"bronto metrics definitions get": {{"metrics", "definitions", "get", probeID}, {"metrics", "definitions", "get", probeMissingID}},
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
	// probeDashboardID is dashboard d1, which the update manifest changes.
	probeDashboardID = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000d4"
	// probeMissingID answers 500, as live a GET of a deleted metric
	// definition does (see newDefinitionGetCmd).
	probeMissingID = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000d5"
)

// recordRequests runs one command against a stub API and returns the
// "METHOD /path" of every request it made. The stub answers just enough
// for name resolution to succeed (dataset "ds", saved search "s1", user
// alice@example.com, groups g1/g2, monitor m1, metric cpu, parser p1,
// widget w1, dashboard d1) and for dashboards apply to plan and write (a
// create answers an id), and --ask_url points at a fake LLM on the same
// server, excluded from the result. Errors are ignored: a command that
// fails after its first calls has still shown which endpoints it uses.
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
		if strings.HasPrefix(r.URL.Path, "/users/") && strings.HasSuffix(r.URL.Path, "/groups") {
			_, _ = fmt.Fprintf(w, `{"groups":[%q]}`, probeID) // live shape: bare ids
			return
		}
		if strings.HasSuffix(r.URL.Path, "/"+probeMissingID) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"message":"internal error"}`)
			return
		}
		if r.Method == http.MethodPost {
			switch r.URL.Path {
			case "/dashboards", "/widgets", "/metrics/definitions":
				_, _ = fmt.Fprintf(w, `{"id":%q}`, probeID)
				return
			}
		}
		switch r.URL.Path {
		case "/logs":
			_, _ = fmt.Fprintf(w, `{"logs":[{"log_id":%q,"log":"ds","collection":"c"}]}`, probeDataset)
		case "/saved-searches":
			_, _ = fmt.Fprintf(w, `{"saved_searches":[{"id":%q,"name":"s1"}]}`, probeID)
		case "/users":
			_, _ = fmt.Fprintf(w, `{"users":[{"id":%q,"email":"alice@example.com"}]}`, probeID)
		case "/groups":
			_, _ = fmt.Fprintf(w, `{"groups":[{"group_id":%q,"name":"g1"},{"group_id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000d3","name":"g2"}]}`, probeID)
		case "/monitors":
			_, _ = fmt.Fprintf(w, `{"monitors":[{"id":%q,"name":"m1"}]}`, probeID)
		case "/metrics":
			_, _ = fmt.Fprintf(w, `{"metrics":[{"metric_id":%q,"metric_name":"cpu"}]}`, probeID)
		case "/parsers":
			_, _ = fmt.Fprintf(w, `{"parsers":[{"id":%q,"name":"p1"}]}`, probeID)
		case "/metrics/definitions":
			_, _ = fmt.Fprintf(w, `{"definitions":[{"id":%q,"name":"d1"}]}`, probeID)
		case "/widgets":
			_, _ = fmt.Fprintf(w, `{"widgets":[{"id":%q,"name":"w1","metric_ids":[%q]}]}`, probeID, probeID)
		case "/widgets/" + probeID:
			_, _ = fmt.Fprintf(w, `{"id":%q,"name":"w1","metric_ids":[%q]}`, probeID, probeID)
		case "/dashboards":
			_, _ = fmt.Fprintf(w, `{"dashboards":[{"id":%q,"name":"d1"}]}`, probeDashboardID)
		case "/dashboards/" + probeDashboardID:
			// The update manifest keeps env, changes managed-by and drops
			// stale: one tag add, update and delete.
			_, _ = fmt.Fprintf(w, `{"id":%q,"name":"d1","tags":{"managed-by":"cli-test-other","stale":"x"},"widget_ids":[%q]}`,
				probeDashboardID, probeID)
		case "/dashboards/templates":
			_, _ = fmt.Fprintf(w, `{"templates":[{"id":%q,"name":"t1","parameters":{}}]}`, probeID)
		case "/dashboards/templates/" + probeID:
			_, _ = fmt.Fprintf(w, `{"id":%q,"name":"t1","parameters":{}}`, probeID)
		case "/forward-configs":
			_, _ = fmt.Fprintf(w, `{"forward_configs":[{"id":%q,"name":"f1","destination":{"bucket":"b"}}]}`, probeID)
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
	manifest := filepath.Join(dir, "dashboard.yaml")
	if err := os.WriteFile(manifest, []byte("name: d\nwidgets:\n  - {name: w, queries: [{dataset: ds, select: count(*)}]}\n  - {ref: w1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	update := filepath.Join(dir, "update.yaml")
	if err := os.WriteFile(update, []byte("name: d1\ntags: {env: prod}\nwidgets:\n  - {ref: w1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	placeholders := strings.NewReplacer("@MONITOR", monitor, "@UPDATE_MANIFEST", update, "@MANIFEST", manifest)
	full := make([]string, 0, len(args)+4)
	for _, a := range args {
		full = append(full, placeholders.Replace(a))
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

	// 1. Every leaf command is accounted for. A generated resource verb
	// needs no entry (registryOperations maps it), but a hand-written
	// command that replaces one (exports create, dashboards create) does.
	generated := generatedVerbs()
	isGenerated := func(c *cobra.Command) bool {
		g := generated[c.CommandPath()]
		return g != nil && sameAsGenerated(c, g)
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
		if !probed && !unprobed && !none && !isGenerated(c) {
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

// generatedVerbs returns every generated resource verb by command path.
func generatedVerbs() map[string]*cobra.Command {
	out := map[string]*cobra.Command{}
	for _, d := range resourceRegistry {
		for _, v := range newResourceCmd(d).Commands() {
			out["bronto "+d.display()+" "+v.Name()] = v
		}
	}
	return out
}

// sameAsGenerated reports whether c is the generated resource verb g
// rather than a hand-written replacement. Help text and flags aren't
// enough: a wrapper such as metrics definitions get keeps them and swaps
// RunE for one that makes extra requests. Closures built from the same
// func literal share a code pointer, so a generated verb's RunE matches
// g's, and a wrapping closure's doesn't.
func sameAsGenerated(c, g *cobra.Command) bool {
	return c.Use == g.Use && c.Short == g.Short && c.Long == g.Long && c.Example == g.Example &&
		slices.Equal(flagNames(c), flagNames(g)) &&
		funcPointer(c.RunE) == funcPointer(g.RunE) &&
		funcPointer(c.PreRunE) == funcPointer(g.PreRunE)
}

// Args isn't compared: NewRootCmd wraps every command's validator.
//
// funcPointer returns a func's code pointer, 0 for nil.
func funcPointer(f any) uintptr {
	return reflect.ValueOf(f).Pointer()
}

// flagNames returns the sorted names of a command's local flags.
func flagNames(c *cobra.Command) []string {
	var out []string
	c.LocalFlags().VisitAll(func(f *pflag.Flag) { out = append(out, f.Name) })
	slices.Sort(out)
	return out
}

// TestEveryInventoriedEndpointHasURLTemplate guards HTTP span naming: every
// path a command calls must resolve to a URL template, so its client span
// is named "GET /monitors/{monitorId}" rather than a bare "GET". A failure
// means a new endpoint is missing from api/openapi.yaml; add it to
// undocumentedURLTemplates in internal/api.
func TestEveryInventoriedEndpointHasURLTemplate(t *testing.T) {
	for _, e := range EndpointInventory() {
		path := strings.ReplaceAll(e.Pattern, "{*}", "0f0e0d0c")
		if _, ok := api.URLTemplate(path); !ok {
			t.Errorf("%s (%s): no URL template matches %q", e.Pattern, e.Command, path)
		}
	}
}

// TestSameAsGeneratedSpotsWrappers: metrics definitions get keeps the
// generated help and flags but wraps RunE, so it must count as
// hand-written and need a probe.
func TestSameAsGeneratedSpotsWrappers(t *testing.T) {
	generated := generatedVerbs()
	for path, want := range map[string]bool{
		"bronto metrics definitions get":  false,
		"bronto metrics definitions list": true,
	} {
		c, _, err := NewRootCmd().Find(strings.Fields(path)[1:])
		if err != nil || generated[path] == nil {
			t.Fatalf("%s: command %v or generated verb missing", path, err)
		}
		if got := sameAsGenerated(c, generated[path]); got != want {
			t.Errorf("sameAsGenerated(%s) = %v, want %v", path, got, want)
		}
	}
}

// TestEndpointProbesReachTheirBranches guards probes that only prove
// something if they get past their first requests: an apply that fails
// to plan makes no writes, and TestAPICoverageMatchesRequests would no
// longer see the tag writes.
func TestEndpointProbesReachTheirBranches(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"dashboards", "apply", "-f", "@MANIFEST"}, []string{"POST /dashboards", "POST /widgets", "POST /tags"}},
		{[]string{"dashboards", "apply", "-f", "@UPDATE_MANIFEST"}, []string{"POST /tags", "PUT /tags", "DELETE /tags"}},
		// the 500 fallback lists definitions to tell missing from broken
		{[]string{"metrics", "definitions", "get", probeMissingID}, []string{"GET /metrics/definitions"}},
	} {
		got := recordRequests(t, tc.args)
		for _, w := range tc.want {
			if !slices.Contains(got, w) {
				t.Errorf("%s: no %s among %v", strings.Join(tc.args, " "), w, got)
			}
		}
	}
}
