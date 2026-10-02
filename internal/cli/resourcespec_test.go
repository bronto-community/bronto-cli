package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// specPaths reads api/openapi.yaml and returns the set of top-level path
// keys (e.g. "/monitors", "/monitors/{monitorId}") declared in the spec.
// This is the CI tripwire for descriptor drift: every resourceDesc's
// Base/CreatePath/IDBase must correspond to a real spec path, so a typo or a
// renamed endpoint fails the build instead of silently 404ing at runtime.
//
// It goes through parseSpec, so a vendored spec that is not well-formed
// fails every conformance test rather than yielding whatever path keys a
// text scan happens to find.
func specPaths(t *testing.T) map[string]bool {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	data, err := os.ReadFile(filepath.Join(root, "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("reading api/openapi.yaml: %v", err)
	}
	paths, err := parseSpec(data)
	if err != nil {
		t.Fatalf("api/openapi.yaml is not a well-formed OpenAPI document: %v\n"+
			"Re-vendor by downloading the upstream spec whole and re-applying the "+
			"'bronto-cli vendor patch' notes; don't hand-splice path blocks.", err)
	}
	return paths
}

// parseSpec parses an OpenAPI document and returns its path keys. It fails
// on anything a hand-spliced re-vendor tends to produce: invalid YAML
// (#116 hand-pasted a path block with an unterminated quote, and the old
// line-based scanner still found every path key), duplicate keys
// (yaml.v3 rejects them), a missing or empty paths map, and internal $refs
// that point at nothing.
func parseSpec(data []byte) (map[string]bool, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if v, _ := doc["openapi"].(string); !strings.HasPrefix(v, "3.") {
		return nil, fmt.Errorf("missing or unsupported top-level openapi version (got %v)", doc["openapi"])
	}
	rawPaths, ok := doc["paths"].(map[string]any)
	if !ok || len(rawPaths) == 0 {
		return nil, errors.New("missing or empty top-level paths map")
	}
	paths := map[string]bool{}
	for p, item := range rawPaths {
		if !strings.HasPrefix(p, "/") {
			return nil, fmt.Errorf("path key %q does not start with /", p)
		}
		if _, ok := item.(map[string]any); !ok {
			return nil, fmt.Errorf("path %q is not a mapping of operations", p)
		}
		paths[p] = true
	}
	var dangling []string
	walkRefs(doc, func(ref string) {
		if !strings.HasPrefix(ref, "#/") {
			return // external refs are out of scope for a single vendored file
		}
		if !resolvesPointer(doc, strings.Split(ref[2:], "/")) {
			dangling = append(dangling, ref)
		}
	})
	if len(dangling) > 0 {
		slices.Sort(dangling)
		return nil, fmt.Errorf("%d $ref(s) resolve to nothing: %s", len(dangling), strings.Join(slices.Compact(dangling), ", "))
	}
	return paths, nil
}

// walkRefs calls fn with the value of every "$ref" key in the tree.
func walkRefs(node any, fn func(string)) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if s, ok := v.(string); ok && k == "$ref" {
				fn(s)
				continue
			}
			walkRefs(v, fn)
		}
	case []any:
		for _, v := range n {
			walkRefs(v, fn)
		}
	}
}

// resolvesPointer reports whether a JSON pointer (already split on "/")
// names an existing node in doc. Only the ~1/~0 escapes OpenAPI refs use
// are decoded; array indices don't appear in component refs.
func resolvesPointer(doc map[string]any, segs []string) bool {
	var cur any = doc
	for _, seg := range segs {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[seg]; !ok {
			return false
		}
	}
	return true
}

// pathPrefixExists reports whether want is a prefix of a declared spec path.
// Only the IDBase+"/{" check uses this, because parameter names vary per
// resource ("/monitors/{monitorId}", "/parsers/{parser_id}", ...). Base and
// CreatePath must EXACT-match a spec path (a plain paths[want] lookup), so a
// near-miss like "/monitor" can't be satisfied by "/monitors" existing.
func pathPrefixExists(paths map[string]bool, want string) bool {
	for p := range paths {
		if strings.HasPrefix(p, want) {
			return true
		}
	}
	return false
}

// specCreatePathExceptions documents descriptor CreatePaths that are real
// Bronto endpoints not captured by this vendored spec snapshot. Anything
// not listed here must have a literal match in api/openapi.yaml.
// (Currently empty; the 2026-07-17 re-vendor added /datasets upstream,
// retiring the previous exception for it.)
var specCreatePathExceptions = map[string]bool{}

// specIDBaseExceptions documents descriptors with no per-ID path in this
// vendored spec. (Currently empty; tags is no longer in the registry.)
var specIDBaseExceptions = map[string]bool{}

// specLiveButUndocumented lists base paths the published upstream spec
// stopped documenting but that the live API still serves: dashboards,
// saved-searches and parsers since the 2026-07-17 re-vendor (which removed
// 35 paths), usage since the 2026-10-01 one. dashboards and saved-searches
// are live-verified on every PR by integration TestResourcesCRUD, usage by
// TestUsageLive, and parsers by TestParsersListTolerant. Re-check at every
// re-vendor: if an entry here starts 404ing live, drop the CLI command
// instead of keeping the exception.
//
// organizations was never in any vendored spec: `search --url/--open` reads
// the active org from it to build the web-UI link (live-verified
// 2026-10-02).
var specLiveButUndocumented = map[string]bool{
	"/dashboards":     true,
	"/saved-searches": true,
	"/parsers":        true,
	"/usage":          true,
	"/organizations":  true,
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

// TestEndpointInventoryMatchesSpec keeps EndpointInventory (the CLI-impact
// input for spec-sync's digest) honest: every pattern must match a path in
// the vendored spec — exactly, as a prefix (e.g. "/logs/{*}" is satisfied
// by "/logs/{logId}/dashboards" even though the bare per-ID path is no
// longer documented), or via the specLiveButUndocumented set.
func TestEndpointInventoryMatchesSpec(t *testing.T) {
	normSpec := map[string]bool{}
	for p := range specPaths(t) {
		normSpec[normalizeParams(p)] = true
	}
	undocumented := func(pattern string) bool {
		for base := range specLiveButUndocumented {
			if pattern == base || strings.HasPrefix(pattern, base+"/") {
				return true
			}
		}
		return false
	}
	for _, e := range EndpointInventory() {
		norm := normalizeParams(e.Pattern)
		ok := normSpec[norm] || undocumented(e.Pattern)
		if !ok {
			for p := range normSpec {
				if strings.HasPrefix(p, norm+"/") {
					ok = true
					break
				}
			}
		}
		if !ok {
			t.Errorf("endpoint inventory pattern %q (%s) matches nothing in api/openapi.yaml and is not a documented live-but-undocumented exception", e.Pattern, e.Command)
		}
	}
}

func TestResourceRegistryMatchesSpec(t *testing.T) {
	paths := specPaths(t)
	for _, d := range resourceRegistry {
		if !paths[d.Base] && !specLiveButUndocumented[d.Base] {
			t.Errorf("%s: Base %q not found in api/openapi.yaml", d.Name, d.Base)
		}
		if cp := d.createPath(); !specCreatePathExceptions[cp] && !specLiveButUndocumented[cp] && !paths[cp] {
			t.Errorf("%s: CreatePath %q not found in api/openapi.yaml", d.Name, cp)
		}
		if d.NoGet && d.NoUpdate && d.NoDelete {
			continue // list-only resource: no per-ID path to conform to
		}
		if idb := d.idBase(); !specIDBaseExceptions[idb] && !specLiveButUndocumented[idb] && !pathPrefixExists(paths, idb+"/{") {
			t.Errorf("%s: IDBase %q has no matching '.../{...}' path in api/openapi.yaml", d.Name, idb)
		}
	}
}

// TestParseSpecRejectsMalformed proves the spec gate can go red: each case
// is a shape a hand-edited re-vendor has produced or could produce, and the
// line-based scanner parseSpec replaced accepted all of them.
func TestParseSpecRejectsMalformed(t *testing.T) {
	const head = "openapi: 3.0.3\ninfo:\n  title: t\n  version: '1'\n"
	cases := map[string]string{
		// The #116 shape: a hand-pasted path block ending in an
		// unterminated quoted $ref, which swallows the next path's opening
		// lines. The line scanner still found every path key.
		"unterminated quote": head + `paths:
  /a:
    get:
      responses:
        default:
          description: err
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/E
  /b:
    get:
      parameters:
        - name: from
          in: query
          required: false
      responses:
        '200':
          description: ok
`,
		"duplicate path key": head + `paths:
  /a:
    get:
      responses: {}
  /a:
    get:
      responses: {}
`,
		"dangling ref": head + `paths:
  /a:
    get:
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Missing'
components:
  schemas: {}
`,
		"no paths":         head + "components: {}\n",
		"not openapi 3":    "swagger: '2.0'\npaths:\n  /a: {}\n",
		"non-mapping path": head + "paths:\n  /a: oops\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSpec([]byte(doc)); err == nil {
				t.Fatal("parseSpec accepted a malformed spec")
			}
		})
	}

	ok := head + `paths:
  /a~b/{id}:
    get:
      responses:
        '200':
          $ref: '#/components/responses/R'
components:
  responses:
    R:
      description: ok
`
	paths, err := parseSpec([]byte(ok))
	if err != nil {
		t.Fatalf("parseSpec rejected a well-formed spec: %v", err)
	}
	if !paths["/a~b/{id}"] {
		t.Fatalf("parseSpec paths = %v, want /a~b/{id}", paths)
	}
}
