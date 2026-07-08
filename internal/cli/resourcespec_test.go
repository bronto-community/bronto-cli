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

// specCreatePathExceptions documents descriptor CreatePaths that are real,
// documented Bronto endpoints not captured by this vendored spec snapshot.
// Anything not listed here must have a literal match in api/openapi.yaml.
var specCreatePathExceptions = map[string]bool{
	// POST /datasets creates a dataset from {"collection","dataset"} (see
	// the bronto skill's api-overview.md); this vendored spec only
	// documents the equivalent via POST /logs (logset/log fields).
	"/datasets": true,
}

// specIDBaseExceptions documents descriptors with no per-ID path in this
// vendored spec. (Currently empty; tags is no longer in the registry.)
var specIDBaseExceptions = map[string]bool{}

func TestResourceRegistryMatchesSpec(t *testing.T) {
	paths := specPaths(t)
	for _, d := range resourceRegistry {
		if !paths[d.Base] {
			t.Errorf("%s: Base %q not found in api/openapi.yaml", d.Name, d.Base)
		}
		if cp := d.createPath(); !specCreatePathExceptions[cp] && !paths[cp] {
			t.Errorf("%s: CreatePath %q not found in api/openapi.yaml", d.Name, cp)
		}
		if idb := d.idBase(); !specIDBaseExceptions[idb] && !pathPrefixExists(paths, idb+"/{") {
			t.Errorf("%s: IDBase %q has no matching '.../{...}' path in api/openapi.yaml", d.Name, idb)
		}
	}
}
