package cli

import (
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
// against what the commands really do: each command listed for an
// operation must request it when driven by its endpointProbes (a group
// such as "bronto traces" by any of its subcommands' probes). Commands
// that can't be probed (unprobedCommands) are taken on trust.
func TestAPICoverageMatchesRequests(t *testing.T) {
	recorded := map[string][]string{} // command -> requests, cached
	requests := func(cmd string) ([]string, bool) {
		if r, ok := recorded[cmd]; ok {
			return r, true
		}
		var out []string
		probed := false
		for p, runs := range endpointProbes {
			if p != cmd && !strings.HasPrefix(p, cmd+" ") {
				continue
			}
			probed = true
			for _, args := range runs {
				out = append(out, recordRequests(t, args)...)
			}
		}
		if probed {
			recorded[cmd] = out
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
}
