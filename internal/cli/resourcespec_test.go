package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// specPaths reads api/openapi.yaml once and returns the set of top-level
// path keys (e.g. "/monitors", "/monitors/{monitorId}") declared in the
// spec. This is the CI tripwire for descriptor drift: every resourceDesc's
// Base/CreatePath/IDBase must correspond to a real spec path, so a typo or a
// renamed endpoint fails the build instead of silently 404ing at runtime.
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
	paths := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimRight(line, " \r")
		if !strings.HasPrefix(trimmed, "  /") || !strings.HasSuffix(trimmed, ":") {
			continue
		}
		paths[strings.TrimSuffix(strings.TrimSpace(trimmed), ":")] = true
	}
	if len(paths) == 0 {
		t.Fatal("parsed zero paths from api/openapi.yaml — parser or spec location is broken")
	}
	return paths
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
// stopped documenting (2026-07-17 re-vendor removed 35 paths) but that the
// live API still serves. dashboards and saved-searches are live-verified on
// every PR by integration TestResourcesCRUD; parsers is untested live but
// was working when last documented. Re-check at every re-vendor: if an
// entry here starts 404ing live, drop the CLI command instead of keeping
// the exception.
var specLiveButUndocumented = map[string]bool{
	"/dashboards":     true,
	"/saved-searches": true,
	"/parsers":        true,
	// monitors test's endpoint also vanished from the published spec in
	// the same 2026-07-17 reorg; untested live (it notifies every monitor
	// in the account), kept until a live 404 says otherwise.
	"/monitors/send-test-notifications": true,
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
		if idb := d.idBase(); !specIDBaseExceptions[idb] && !specLiveButUndocumented[idb] && !pathPrefixExists(paths, idb+"/{") {
			t.Errorf("%s: IDBase %q has no matching '.../{...}' path in api/openapi.yaml", d.Name, idb)
		}
	}
}
