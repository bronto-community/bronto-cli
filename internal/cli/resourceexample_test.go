package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"

	"github.com/bronto-community/bronto-cli/internal/bronto"
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

// bodySchema is what the example checks need from a request schema.
type bodySchema struct {
	required []string            // required in every variant
	variants [][]string          // oneOf members' required sets; a body must satisfy one
	types    map[string][]string // documented properties -> their JSON types (nil: untyped)
}

func (b bodySchema) documents(k string) bool { _, ok := b.types[k]; return ok }

// schemaFields collects a request schema's required fields and documented
// top-level properties with their types, including those contributed by
// allOf members and oneOf variants.
func schemaFields(doc map[string]any, node any) bodySchema {
	b := bodySchema{types: map[string][]string{}}
	collectFields(doc, node, &b)
	slices.Sort(b.required)
	b.required = slices.Compact(b.required)
	return b
}

func collectFields(doc map[string]any, node any, b *bodySchema) {
	s := derefSchema(doc, node)
	for _, r := range asSlice(s["required"]) {
		if k, ok := r.(string); ok {
			b.required = append(b.required, k)
		}
	}
	if m, ok := s["properties"].(map[string]any); ok {
		for k, p := range m {
			b.types[k] = append(b.types[k], schemaTypes(doc, p)...)
		}
	}
	for _, sub := range asSlice(s["allOf"]) {
		collectFields(doc, sub, b)
	}
	for _, sub := range asSlice(s["oneOf"]) {
		v := bodySchema{types: b.types}
		collectFields(doc, sub, &v)
		b.variants = append(b.variants, v.required)
	}
}

// schemaTypes returns the JSON types a property schema allows (several
// for a oneOf), or nil when it doesn't say.
func schemaTypes(doc map[string]any, node any) []string {
	s := derefSchema(doc, node)
	if t, ok := s["type"].(string); ok {
		return []string{t}
	}
	var out []string
	for _, sub := range append(asSlice(s["oneOf"]), asSlice(s["anyOf"])...) {
		ts := schemaTypes(doc, sub)
		if ts == nil {
			return nil // one untyped alternative accepts anything
		}
		out = append(out, ts...)
	}
	return out
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

// specBody returns the request schema of method on path, and whether the
// spec documents that operation.
func specBody(doc map[string]any, path, method string) (bodySchema, bool) {
	paths, _ := doc["paths"].(map[string]any)
	item, _ := paths[path].(map[string]any)
	op, ok := item[strings.ToLower(method)].(map[string]any)
	if !ok {
		return bodySchema{}, false
	}
	rb := derefSchema(doc, op["requestBody"])
	content, _ := rb["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	return schemaFields(doc, media["schema"]), true
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

// specTypeExceptions are example field types that knowingly differ from
// the spec because the spec is wrong about the live API.
var specTypeExceptions = map[string]bool{
	// CreateApiKeyRequest types roles as a bare object; the live API takes
	// (and integration TestResourcesCRUD sends) an array of role names.
	"api-keys create roles": true,
}

// TestResourceExamplesCoverSpecRequiredFields keeps the generated
// create/update Examples valid requests: the -f keys must include every
// field the spec marks required on that request body (plus UpdateRequires)
// and, for a oneOf body, every field of at least one variant; every key
// must be a documented property; and each concrete value must parse (as
// -f parses it) to a JSON type the property allows. Operations the spec
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
				if body, ok := specBody(doc, d.createPath(), "POST"); ok {
					checkExampleFields(t, d.display()+" create", cmd.Example, d.CreateFields, body)
				}
			}
		}
		if !d.NoUpdate {
			cmd := findSub(root, append(path, "update")...)
			if cmd == nil {
				t.Fatalf("%s update not registered", d.display())
			}
			body, _ := specBody(doc, specIDPath(doc, d.idBase()), d.updateMethod())
			body.required = append(slices.Clone(body.required), d.UpdateRequires...)
			checkExampleFields(t, d.display()+" update", cmd.Example, d.UpdateFields, body)
		}
	}
}

func checkExampleFields(t *testing.T, what, example string, fields []string, body bodySchema) {
	t.Helper()
	keys := exampleFieldKeys(example)
	if len(keys) == 0 {
		t.Errorf("%s example %q sets no -f fields", what, example)
	}
	for _, r := range body.required {
		if !slices.Contains(keys, r) {
			t.Errorf("%s example %q omits required field %q (required: %v)", what, example, r, body.required)
		}
	}
	if len(body.variants) > 0 && !slices.ContainsFunc(body.variants, func(req []string) bool {
		return !slices.ContainsFunc(req, func(r string) bool { return !slices.Contains(keys, r) })
	}) {
		t.Errorf("%s example %q satisfies none of the oneOf variants (required: %v)", what, example, body.variants)
	}
	if len(body.types) == 0 {
		return // schema documents no properties (or no operation): nothing to check against
	}
	for _, k := range keys {
		if !body.documents(k) {
			t.Errorf("%s example %q sets %q, which the request schema does not document", what, example, k)
		}
	}
	for _, f := range fields {
		k, v, _ := strings.Cut(f, "=")
		want := body.types[k]
		got := exampleValueType(v)
		if got == "" || len(want) == 0 || specTypeExceptions[what+" "+k] {
			continue
		}
		if !slices.Contains(want, got) && (got != "integer" || !slices.Contains(want, "number")) {
			t.Errorf("%s example sets %s, a JSON %s; the spec types %q as %v", what, f, got, k, want)
		}
	}
}

// exampleValueType is the JSON type -f gives an example value (see
// parseFieldArgs), or "" for a bare placeholder such as <name> or <json>
// whose replacement decides. A structured value with placeholders inside
// ({"from":<start-ms>}) is typed by its first character.
func exampleValueType(v string) string {
	if strings.Contains(v, "<") {
		switch {
		case strings.HasPrefix(v, "{"):
			return "object"
		case strings.HasPrefix(v, "["):
			return "array"
		case strings.HasPrefix(v, `"`):
			return "string"
		}
		return ""
	}
	var parsed any
	if err := bronto.DecodeJSON([]byte(v), &parsed); err != nil {
		return "string"
	}
	switch p := parsed.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case bool:
		return "boolean"
	case string:
		return "string"
	case nil:
		return "null"
	default:
		if s, ok := p.(interface{ String() string }); ok && !strings.ContainsAny(s.String(), ".eE") {
			return "integer"
		}
		return "number"
	}
}

// TestExampleValueType pins how example values are typed: the bug this
// guards against is threshold=1000 reaching a string-typed field as a
// number.
func TestExampleValueType(t *testing.T) {
	for v, want := range map[string]string{
		"1000":                       "integer",
		"1.5":                        "number",
		`"1000"`:                     "string",
		"Last 20 minutes":            "string",
		"true":                       "boolean",
		`["IngestionApi"]`:           "array",
		`{"a":1}`:                    "object",
		"<name>":                     "",
		"<json>":                     "",
		`{"from":<start-ms>,"to":1}`: "object",
		`["<monitor-id>"]`:           "array",
	} {
		if got := exampleValueType(v); got != want {
			t.Errorf("exampleValueType(%q) = %q, want %q", v, got, want)
		}
	}
}

// TestRegistryIDKeysMatchSpec checks each entry's IDKey against the id
// field of its spec list rows: a row without plain "id" (downtimes'
// downtime_id) needs IDKey, or name resolution matches a row with no usable
// id and reports not-found. The id field is the property named after the
// per-ID path parameter ({downtimeId} -> downtime_id) or the base's last
// segment (/logs -> log_id); entries whose list schema is undocumented or
// names neither are skipped.
func TestRegistryIDKeysMatchSpec(t *testing.T) {
	doc := specDoc(t)
	checked := 0
	for _, d := range resourceRegistry {
		props := specListRowProps(doc, d.Base, d.ListRowKeys)
		if props == nil {
			continue
		}
		if live, ok := specIDKeyExceptions[d.display()]; ok {
			if d.IDKey != live {
				t.Errorf("%s: IDKey %q, want the live id field %q", d.display(), d.IDKey, live)
			}
			continue
		}
		if d.IDKey != "" && !slices.Contains(props, d.IDKey) {
			t.Errorf("%s: IDKey %q is not a property of the spec's list rows %v", d.display(), d.IDKey, props)
			continue
		}
		if slices.Contains(props, "id") {
			checked++
			continue // rowID's fallback; an IDKey, if set, was checked above
		}
		var want string
		for _, c := range idFieldCandidates(doc, d) {
			if slices.Contains(props, c) {
				want = c
				break
			}
		}
		if want == "" {
			continue
		}
		checked++
		if d.IDKey != want {
			t.Errorf("%s: list rows carry their id in %q, not \"id\"; set IDKey: %q", d.display(), want, want)
		}
	}
	if checked < 5 {
		t.Fatalf("only %d registry entries had a determinable list id field: is the spec lookup broken?", checked)
	}
}

// specIDKeyExceptions are list id fields the live API names differently
// from the spec.
var specIDKeyExceptions = map[string]string{
	// The Downtime schema documents id; live rows carry downtime_id
	// (2026-10-15).
	"monitors downtimes": "downtime_id",
}

// idFieldCandidates are the snake_case names a resource's id field goes by.
func idFieldCandidates(doc map[string]any, d resourceDesc) []string {
	var out []string
	if p := specIDPath(doc, d.idBase()); p != "" {
		param := p[strings.LastIndex(p, "{")+1 : len(p)-1]
		out = append(out, snakeCase(param))
	}
	seg := d.Base[strings.LastIndex(d.Base, "/")+1:]
	return append(out, strings.TrimSuffix(strings.ReplaceAll(seg, "-", "_"), "s")+"_id")
}

func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// specListRowProps returns the properties of the rows GET base answers
// with: a top-level array's items, or the items of the object's array
// property (one of keys, else the only one). nil when undeterminable.
func specListRowProps(doc map[string]any, base string, keys []string) []string {
	paths, _ := doc["paths"].(map[string]any)
	item, _ := paths[base].(map[string]any)
	op, _ := item["get"].(map[string]any)
	responses, _ := op["responses"].(map[string]any)
	resp := derefSchema(doc, responses["200"])
	content, _ := resp["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	s := derefSchema(doc, media["schema"])
	if s == nil {
		return nil
	}
	var rows any
	if s["type"] == "array" {
		rows = s["items"]
	} else {
		var arrays []string
		props, _ := s["properties"].(map[string]any)
		for k, p := range props {
			if derefSchema(doc, p)["type"] == "array" {
				arrays = append(arrays, k)
			}
		}
		for _, k := range keys {
			if slices.Contains(arrays, k) {
				arrays = []string{k}
			}
		}
		if len(arrays) != 1 {
			return nil
		}
		rows = derefSchema(doc, props[arrays[0]])["items"]
	}
	b := schemaFields(doc, rows)
	if len(b.types) == 0 {
		return nil
	}
	out := make([]string, 0, len(b.types))
	for k := range b.types {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
