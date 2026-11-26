package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// readSpec returns the vendored api/openapi.yaml.
func readSpec(t *testing.T) []byte {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("reading api/openapi.yaml: %v", err)
	}
	return data
}

// TestAPICoverage is the staleness guard for the API coverage page
// (docs/cli/reference/api-coverage.mdx): every operation in the vendored
// spec is either covered by a command or explicitly marked as reachable
// only through `bronto api`, and every entry names a real operation and
// real commands. A re-vendored spec with a new operation fails here.
func TestAPICoverage(t *testing.T) {
	rep, err := APICoverage(readSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Operations) == 0 {
		t.Fatal("no operations parsed from the spec")
	}
}

// TestAPICoverageRejectsGaps proves the guard goes red: an operation with
// no entry, and an entry for an operation the spec no longer has.
func TestAPICoverageRejectsGaps(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal(readSpec(t), &doc); err != nil {
		t.Fatal(err)
	}
	paths := mappingValue(doc.Content[0], "paths")
	// Drop /customer (two apiOperationsNotCovered entries) and add an
	// operation nothing maps.
	for i := 0; i+1 < len(paths.Content); i += 2 {
		if paths.Content[i].Value == "/customer" {
			paths.Content = slices.Delete(paths.Content, i, i+2)
			break
		}
	}
	var extra yaml.Node
	if err := yaml.Unmarshal([]byte("/cli-test-new:\n  get:\n    tags: [new]\n    summary: New\n"), &extra); err != nil {
		t.Fatal(err)
	}
	paths.Content = append(paths.Content, extra.Content[0].Content...)
	data, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatal(err)
	}

	_, err = APICoverage(data)
	if err == nil {
		t.Fatal("APICoverage accepted a spec with an unmapped operation and a stale entry")
	}
	for _, want := range []string{"GET /cli-test-new has no command", `"GET /customer" is not an operation in the spec`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

// TestAPICoverageRejectsDroppedRegistryMethod checks that a registry verb
// whose method the spec drops, while the path stays, fails the map
// instead of moving to the outside-spec table.
func TestAPICoverageRejectsDroppedRegistryMethod(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal(readSpec(t), &doc); err != nil {
		t.Fatal(err)
	}
	user := mappingValue(mappingValue(doc.Content[0], "paths"), "/users/{userId}")
	if user == nil || mappingValue(user, "delete") == nil {
		t.Fatal("spec has no DELETE /users/{userId} to drop")
	}
	for i := 0; i+1 < len(user.Content); i += 2 {
		if user.Content[i].Value == "delete" {
			user.Content = slices.Delete(user.Content, i, i+2)
			break
		}
	}
	data, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatal(err)
	}

	_, err = APICoverage(data)
	if err == nil {
		t.Fatal("APICoverage accepted a spec without DELETE /users/{userId}, which bronto users delete calls")
	}
	if want := "DELETE /users/{*} (bronto users delete) is not an operation in the spec"; !strings.Contains(err.Error(), want) {
		t.Errorf("error does not mention %q:\n%v", want, err)
	}
}

// TestAPICoverageMatchesRequests checks the hand-maintained entries
// against what the commands really do, in both directions. Each command
// listed for an operation must request it when driven by its
// endpointProbes (a group such as "bronto traces" by any of its
// subcommands' probes); commands that can't be probed (unprobedCommands)
// are taken on trust. And no probed command may request an operation the
// map says no command calls (see unmappedRequests); TestEveryCommandIsInventoried
// makes sure every hand-written command is probed or explicitly excused.
func TestAPICoverageMatchesRequests(t *testing.T) {
	recorded := map[string][]string{} // probed command -> requests
	for p, runs := range endpointProbes {
		for _, args := range runs {
			recorded[p] = append(recorded[p], recordRequests(t, args)...)
		}
	}
	requests := func(cmd string) ([]string, bool) {
		var out []string
		probed := false
		for p, reqs := range recorded {
			if p == cmd || strings.HasPrefix(p, cmd+" ") {
				probed = true
				out = append(out, reqs...)
			}
		}
		return out, probed
	}
	check := func(op string, cmds []string) {
		method, path, _ := strings.Cut(op, " ")
		pattern := strings.ReplaceAll(normalizeParams(path), "{}", "{*}")
		for _, c := range cmds {
			reqs, probed := requests(c)
			if !probed {
				if _, ok := unprobedCommands[c]; !ok {
					t.Errorf("%s: %s has no endpointProbes entry to verify it against", op, c)
				}
				continue
			}
			if !slices.ContainsFunc(reqs, func(r string) bool {
				m, p, _ := strings.Cut(r, " ")
				return m == method && patternMatches(pattern, p)
			}) {
				t.Errorf("%s is listed under %s, but its probes made only %v", op, c, reqs)
			}
		}
	}
	for op, cmds := range apiOperationCommands {
		check(op, cmds)
	}
	for op, cmds := range apiOperationsOutsideSpec {
		check(op, cmds)
	}

	ops, err := ParseSpecOperations(readSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range unmappedRequests(ops, recorded) {
		t.Error(problem)
	}
}

// unmappedRequests returns a problem for every recorded request (command
// -> "METHOD /path") that reaches a spec operation the coverage map says no
// command calls: one in apiOperationsNotCovered, which the page would keep
// listing as not supported, or in apiOperationsElsewhere. Without it, a new
// command that calls such an operation but isn't added to
// apiOperationCommands leaves the page wrong and every other check green.
//
// Requests to operations mapped to other commands only are not flagged:
// most are the helper calls apicoverage.go deliberately doesn't count
// (GET /logs to resolve a dataset name, GET /top-keys to check a field,
// GET /users and GET /groups to resolve members, the lookups behind
// `dashboards apply`), and the page already shows such an operation as
// covered, so it can't be wrong about what the CLI supports.
func unmappedRequests(ops []SpecOperation, recorded map[string][]string) []string {
	var problems []string
	for cmd, reqs := range recorded {
		for _, req := range reqs {
			key := specOperationFor(ops, req)
			if missing, ok := apiOperationsNotCovered[key]; ok {
				problems = append(problems, fmt.Sprintf(
					"%s calls %s (%s), which apiOperationsNotCovered lists as not supported yet (%q): "+
						"move it to apiOperationCommands under %q", cmd, key, req, missing, cmd))
			}
			if _, ok := apiOperationsElsewhere[key]; ok {
				problems = append(problems, fmt.Sprintf(
					"%s calls %s (%s), which apiOperationsElsewhere says no command calls: "+
						"move it to apiOperationCommands under %q", cmd, key, req, cmd))
			}
		}
	}
	slices.Sort(problems)
	return slices.Compact(problems)
}

// specOperationFor returns the key of the spec operation a recorded
// request ("METHOD /concrete/path") reaches, or "" when it reaches none.
// The most specific match wins, counting literal segments, so GET
// /users/all is "GET /users/all" rather than "GET /users/{userId}"; the
// endpoints outside the spec take part too, so GET /dashboards/templates,
// which the spec doesn't describe, isn't taken for "GET
// /dashboards/{dashboardId}".
func specOperationFor(ops []SpecOperation, req string) string {
	method, path, _ := strings.Cut(req, " ")
	best, bestLiterals := "", -1
	consider := func(key, specKey string) {
		m, p, _ := strings.Cut(key, " ")
		pattern := strings.ReplaceAll(normalizeParams(p), "{}", "{*}")
		if m != method || !patternMatches(pattern, path) {
			return
		}
		if literals := strings.Count(pattern, "/") - strings.Count(pattern, "{*}"); literals > bestLiterals {
			best, bestLiterals = specKey, literals
		}
	}
	for _, op := range ops {
		consider(op.Key(), op.Key())
	}
	for key := range apiOperationsOutsideSpec {
		consider(key, "")
	}
	for key := range registryOperationsOutsideSpec {
		consider(key, "")
	}
	return best
}

// TestAPICoverageRejectsUnmappedRequests proves the reverse check goes
// red: a command requesting operations the map lists as not supported or
// offered elsewhere. `bronto api` really makes its request against the
// stub; the other requests are injected.
func TestAPICoverageRejectsUnmappedRequests(t *testing.T) {
	ops, err := ParseSpecOperations(readSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string][]string{
		"bronto api": recordRequests(t, []string{"api", "GET", "/explain-error"}),
		"bronto cli-test-new": {
			"POST /patterns",            // not supported yet
			"GET /users/all",            // not supported yet; not GET /users/{userId}
			"GET /search",               // offered elsewhere
			"GET /logs",                 // a helper call: mapped to datasets list
			"GET /dashboards/templates", // outside the spec; not GET /dashboards/{dashboardId}
			"GET /usage",                // outside the spec
		},
	}
	problems := unmappedRequests(ops, recorded)
	want := []string{
		`bronto api calls GET /explain-error (GET /explain-error), which apiOperationsNotCovered lists as not supported yet ("Explain an error with AI"): move it to apiOperationCommands under "bronto api"`,
		`bronto cli-test-new calls GET /search (GET /search), which apiOperationsElsewhere says no command calls: move it to apiOperationCommands under "bronto cli-test-new"`,
		`bronto cli-test-new calls GET /users/all (GET /users/all), which apiOperationsNotCovered`,
		`bronto cli-test-new calls POST /patterns (POST /patterns), which apiOperationsNotCovered`,
	}
	if len(problems) != len(want) {
		t.Fatalf("got %d problems, want %d:\n  %s", len(problems), len(want), strings.Join(problems, "\n  "))
	}
	for i, w := range want {
		if !strings.HasPrefix(problems[i], w) {
			t.Errorf("problem %d = %q, want it to start with %q", i, problems[i], w)
		}
	}
}
