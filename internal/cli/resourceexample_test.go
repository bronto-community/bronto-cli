package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

// specDoc loads api/openapi.yaml as a generic tree. parseSpec (resourcespec_test.go)
// already fails the build on a malformed spec; this just needs the schemas.
func specDoc(t *testing.T) map[string]any {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("reading api/openapi.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing api/openapi.yaml: %v", err)
	}
	return doc
}

// derefSchema follows internal $refs until it reaches a concrete node.
func derefSchema(doc map[string]any, node any) map[string]any {
	for {
		m, _ := node.(map[string]any)
		ref, ok := m["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/") {
			return m
		}
		var cur any = doc
		for _, seg := range strings.Split(ref[2:], "/") {
			cur = cur.(map[string]any)[seg]
		}
		node = cur
	}
}

// schemaFields returns a request schema's required and documented
// top-level properties, including those contributed by allOf members.
func schemaFields(doc map[string]any, node any) (required, props []string) {
	s := derefSchema(doc, node)
	for _, r := range asSlice(s["required"]) {
		if k, ok := r.(string); ok {
			required = append(required, k)
		}
	}
	if m, ok := s["properties"].(map[string]any); ok {
		for k := range m {
			props = append(props, k)
		}
	}
	for _, sub := range asSlice(s["allOf"]) {
		r, p := schemaFields(doc, sub)
		required, props = append(required, r...), append(props, p...)
	}
	slices.Sort(required)
	return slices.Compact(required), props
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

// specBody returns the required and documented fields of the JSON request
// body of method on path, and whether the spec documents that operation.
func specBody(doc map[string]any, path, method string) (required, props []string, ok bool) {
	paths, _ := doc["paths"].(map[string]any)
	item, _ := paths[path].(map[string]any)
	op, ok := item[strings.ToLower(method)].(map[string]any)
	if !ok {
		return nil, nil, false
	}
	rb := derefSchema(doc, op["requestBody"])
	content, _ := rb["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	required, props = schemaFields(doc, media["schema"])
	return required, props, true
}

// specIDPath finds the single-resource path under base ("/monitors" ->
// "/monitors/{monitorId}"): exactly one parameter segment after base.
func specIDPath(doc map[string]any, base string) string {
	paths, _ := doc["paths"].(map[string]any)
	re := regexp.MustCompile("^" + regexp.QuoteMeta(base) + `/\{[^/}]+\}$`)
	for p := range paths {
		if re.MatchString(p) {
			return p
		}
	}
	return ""
}

var exampleFieldRE = regexp.MustCompile(`-f '?([A-Za-z_]+)=`)

// exampleFieldKeys returns the -f keys of the first example command (the
// one that builds the body from flags; the next uses --input), following
// backslash line continuations.
func exampleFieldKeys(example string) []string {
	var first strings.Builder
	for _, line := range strings.Split(example, "\n") {
		first.WriteString(line)
		if !strings.HasSuffix(line, "\\") {
			break
		}
	}
	var keys []string
	for _, m := range exampleFieldRE.FindAllStringSubmatch(first.String(), -1) {
		keys = append(keys, m[1])
	}
	return keys
}

// findSub returns the subcommand of root at path, or nil.
func findSub(root *cobra.Command, path ...string) *cobra.Command {
	cmd, rest, err := root.Find(path)
	if err != nil || len(rest) > 0 || cmd.Name() != path[len(path)-1] {
		return nil
	}
	return cmd
}

// TestResourceExamplesCoverSpecRequiredFields keeps the generated
// create/update Examples valid requests: the -f keys must include every
// field the spec marks required on that request body (plus UpdateRequires),
// and every key must be a documented property of it. Operations the spec
// does not document (specLiveButUndocumented, datasets' PUT /logs/{id}) are
// skipped, as are hand-written commands that replace the generic one
// (exports create).
func TestResourceExamplesCoverSpecRequiredFields(t *testing.T) {
	doc := specDoc(t)
	root := NewRootCmd()
	for _, d := range resourceRegistry {
		path := strings.Fields(d.display())
		if !d.NoCreate {
			cmd := findSub(root, append(path, "create")...)
			if cmd == nil {
				t.Fatalf("%s create not registered", d.display())
			}
			if cmd.Example == newResourceCreateCmd(d).Example {
				req, props, ok := specBody(doc, d.createPath(), "POST")
				if ok {
					checkExampleFields(t, d.display()+" create", cmd.Example, req, props)
				}
			}
		}
		if !d.NoUpdate {
			cmd := findSub(root, append(path, "update")...)
			if cmd == nil {
				t.Fatalf("%s update not registered", d.display())
			}
			req, props, ok := specBody(doc, specIDPath(doc, d.idBase()), d.updateMethod())
			if !ok {
				req, props = nil, nil
			}
			req = append(slices.Clone(req), d.UpdateRequires...)
			checkExampleFields(t, d.display()+" update", cmd.Example, req, props)
		}
	}
}

func checkExampleFields(t *testing.T, what, example string, required, props []string) {
	t.Helper()
	keys := exampleFieldKeys(example)
	if len(keys) == 0 {
		t.Errorf("%s example %q sets no -f fields", what, example)
	}
	for _, r := range required {
		if !slices.Contains(keys, r) {
			t.Errorf("%s example %q omits required field %q (required: %v)", what, example, r, required)
		}
	}
	if len(props) == 0 {
		return // schema documents no properties (or no operation): nothing to check against
	}
	for _, k := range keys {
		if !slices.Contains(props, k) {
			t.Errorf("%s example %q sets %q, which the request schema does not document", what, example, k)
		}
	}
}
