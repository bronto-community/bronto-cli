package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

const (
	tmplNginx = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1"
	tmplEC2   = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e2"
)

// templatesList is the live GET /dashboards/templates shape (2026-10-21):
// full templates under "templates", parameters as a name -> spec map.
const templatesList = `{"templates":[` +
	`{"id":"` + tmplNginx + `","name":"Nginx Overview","parameters":{"fromExpr":{"type":"string","description":"metric fromExpr"}},` +
	`"widgets":[{"id":"w1","arguments":{"from":{"input":"fromExpr"}}},{"id":"w2","arguments":{}}]},` +
	`{"id":"` + tmplEC2 + `","name":"AWS EC2","parameters":{},"widgets":[{"id":"w3","arguments":{}}]}]}`

// templatePreview is a trimmed live preview response: widgets with their
// metric definitions inline, and a throwaway dashboard id.
const templatePreview = `{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000ff","name":"Nginx Overview","description":"Nginx access logs.",` +
	`"template":{"id":"` + tmplNginx + `","arguments":{"fromExpr":{"literal":"{{fromExpr}}"}}},` +
	`"widgets":[{"id":"x1","name":"Total Requests","type":"score","metrics":[{"id":"d1","name":"Total Requests",` +
	`"queries":[{"name":"total","select":["count(*)"],"where":"","groups":[],"from_expr":"{{fromExpr}}"}],` +
	`"formulas":[{"name":"rate","expression":"total / 60"}]}]},` +
	`{"id":"x2","name":"Status Codes","type":"bar","metrics":[{"id":"d2","queries":[{"name":"codes","select":["count(*)"],` +
	`"where":"$status >= 500","groups":["$status"],"from":["log-1"]}]}]}]}`

// templateServer answers the template endpoints and records each request
// as "METHOD path body".
type templateServer struct {
	mu   sync.Mutex
	reqs []string
}

func (s *templateServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(b)))
		s.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/dashboards/templates":
			_, _ = w.Write([]byte(templatesList))
		case r.Method == http.MethodGet && r.URL.Path == "/dashboards/templates/"+tmplNginx:
			_, _ = w.Write([]byte(`{"id":"` + tmplNginx + `","name":"Nginx Overview","parameters":{"fromExpr":{"type":"string"}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/preview"):
			_, _ = w.Write([]byte(templatePreview))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/instance"):
			_, _ = w.Write([]byte(`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000d9","name":"Nginx Overview","widget_ids":["n1","n2"],` +
				`"template":{"id":"` + tmplNginx + `"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/dashboards":
			_, _ = w.Write([]byte(`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000d8","name":"plain"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *templateServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reqs...)
}

func wantCode(t *testing.T, err error, code string) *clierr.Error {
	t.Helper()
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
	if clierr.ExitCode(err) != 2 {
		t.Fatalf("exit code = %d, want 2", clierr.ExitCode(err))
	}
	return ce
}

func TestDashboardTemplatesListTable(t *testing.T) {
	s := &templateServer{}
	out, _, err := runResource(t, s.handler(t), "", "dashboards", "templates", "list", "-o", "table")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("table = %q", out)
	}
	// parameters render as their names, widgets as a count.
	if f := strings.Fields(lines[1]); len(f) != 5 || f[2] != "fromExpr" || f[3] != "2" || f[4] != tmplNginx {
		t.Fatalf("nginx row = %q", lines[1])
	}
	if f := strings.Fields(lines[2]); len(f) != 4 || f[2] != "1" {
		t.Fatalf("ec2 row = %q", lines[2])
	}
}

func TestDashboardTemplatesListJSONIsRaw(t *testing.T) {
	s := &templateServer{}
	out, _, err := runResource(t, s.handler(t), "", "dashboards", "templates", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if _, ok := rows[0]["parameters"].(map[string]any)["fromExpr"]; !ok {
		t.Fatalf("json rows must keep the raw parameters map: %v", rows[0])
	}
}

func TestDashboardTemplatesShowSummary(t *testing.T) {
	s := &templateServer{}
	out, _, err := runResource(t, s.handler(t), "", "dashboards", "templates", "show", "Nginx Overview", "-o", "table")
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.requests()
	// A missing parameter is sent as a {{name}} placeholder: the API
	// refuses a preview without every argument.
	want := []string{
		"GET /dashboards/templates",
		`POST /dashboards/templates/` + tmplNginx + `/preview {"arguments":{"fromExpr":{"literal":"{{fromExpr}}"}}}`,
	}
	if strings.Join(reqs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests =\n%s\nwant\n%s", strings.Join(reqs, "\n"), strings.Join(want, "\n"))
	}
	wantOut := `Nginx Overview
id: ` + tmplNginx + `
Nginx access logs.

Parameters (pass with --param key=value):
  fromExpr  string  metric fromExpr

Widgets (2):

  Total Requests (score)
    query total
      select:    count(*)
      where:     (none)
      from_expr: {{fromExpr}}
      groups:    (none)
    formula rate: total / 60

  Status Codes (bar)
    query codes
      select:    count(*)
      where:     $status >= 500
      from:      log-1
      groups:    $status
`
	if out != wantOut {
		t.Fatalf("summary =\n%s\nwant\n%s", out, wantOut)
	}
}

func TestDashboardTemplatesShowJSONAndParam(t *testing.T) {
	s := &templateServer{}
	out, _, err := runResource(t, s.handler(t), "", "dashboards", "templates", "show", tmplNginx,
		"--param", `fromExpr="collection" = 'web'`, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.requests()
	if len(reqs) != 2 || reqs[0] != "GET /dashboards/templates/"+tmplNginx ||
		!strings.HasSuffix(reqs[1], `{"arguments":{"fromExpr":{"literal":"\"collection\" = 'web'"}}}`) {
		t.Fatalf("requests = %q", reqs)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	// The preview's throwaway id is replaced by the template's, and the
	// template's parameters are added.
	if doc["id"] != tmplNginx {
		t.Fatalf("id = %v, want the template id", doc["id"])
	}
	if _, ok := doc["parameters"].(map[string]any)["fromExpr"]; !ok {
		t.Fatalf("parameters missing: %v", doc["parameters"])
	}
	if ws, _ := doc["widgets"].([]any); len(ws) != 2 {
		t.Fatalf("widgets = %v", doc["widgets"])
	}
}

func TestDashboardTemplatesShowRunsUnderDryRun(t *testing.T) {
	s := &templateServer{}
	out, _, err := runResource(t, s.handler(t), "", "dashboards", "templates", "show", "AWS EC2", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.requests()
	if len(reqs) != 2 || reqs[1] != "POST /dashboards/templates/"+tmplEC2+`/preview {"arguments":{}}` {
		t.Fatalf("preview saves nothing, so --dry-run must still send it: %q", reqs)
	}
	if strings.Contains(out, "dry_run") {
		t.Fatalf("show printed a plan instead of the preview: %s", out)
	}
}

func TestDashboardTemplatesShowUnknownParam(t *testing.T) {
	s := &templateServer{}
	_, _, err := runResource(t, s.handler(t), "", "dashboards", "templates", "show", "Nginx Overview", "--param", "from=x")
	ce := wantCode(t, err, "usage_unknown_param")
	if !strings.Contains(ce.Hint, "Valid keys: fromExpr.") {
		t.Fatalf("hint = %q", ce.Hint)
	}
	for _, r := range s.requests() {
		if strings.HasPrefix(r, "POST") {
			t.Fatalf("no preview may be sent for an unknown key: %q", r)
		}
	}
}

func TestDashboardsCreateFromTemplate(t *testing.T) {
	s := &templateServer{}
	out, stderr, err := runResource(t, s.handler(t), "", "dashboards", "create", "--template", "Nginx Overview",
		"--param", "fromExpr=x = 1", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.requests()
	want := `POST /dashboards/templates/` + tmplNginx + `/instance {"arguments":{"fromExpr":{"literal":"x = 1"}}}`
	if len(reqs) != 2 || reqs[0] != "GET /dashboards/templates" || reqs[1] != want {
		t.Fatalf("requests = %q", reqs)
	}
	if !strings.Contains(out, `"aaaaaaaa-aaaa-aaaa-aaaa-0000000000d9"`) {
		t.Fatalf("stdout = %s", out)
	}
	if !strings.Contains(stderr, `from template "Nginx Overview" with 2 widgets`) {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestDashboardsCreateFromTemplateDryRun(t *testing.T) {
	s := &templateServer{}
	out, stderr, err := runResource(t, s.handler(t), "", "dashboards", "create", "--template", "AWS EC2", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if reqs := s.requests(); len(reqs) != 1 || reqs[0] != "GET /dashboards/templates" {
		t.Fatalf("dry-run may only read: %q", reqs)
	}
	var plan map[string]any
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if plan["path"] != "/dashboards/templates/"+tmplEC2+"/instance" || plan["method"] != "POST" {
		t.Fatalf("plan = %v", plan)
	}
	if strings.Contains(stderr, "Created") {
		t.Fatalf("dry-run claimed a create: %q", stderr)
	}
}

func TestDashboardsCreateFromTemplateRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code string
		hint string
	}{
		{"missing param", []string{"--template", "Nginx Overview"}, "usage_missing_param", "--param fromExpr=<value>"},
		{"unknown param", []string{"--template", "Nginx Overview", "--param", "fromExpr=a", "--param", "zone=b"},
			"usage_unknown_param", "Valid keys: fromExpr."},
		{"param on a template without parameters", []string{"--template", "AWS EC2", "--param", "fromExpr=a"},
			"usage_unknown_param", "It takes no parameters."},
		{"name field", []string{"--template", "AWS EC2", "-f", "name=x"}, "usage_conflicting_flags", "detach-from-template"},
		{"param without template", []string{"--param", "fromExpr=a"}, "usage_invalid_flags", "--template"},
		{"malformed param", []string{"--template", "AWS EC2", "--param", "novalue"}, "usage_invalid_param", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &templateServer{}
			_, _, err := runResource(t, s.handler(t), "", append([]string{"dashboards", "create"}, tc.args...)...)
			ce := wantCode(t, err, tc.code)
			if !strings.Contains(ce.Hint, tc.hint) {
				t.Fatalf("hint = %q, want it to mention %q", ce.Hint, tc.hint)
			}
			for _, r := range s.requests() {
				if strings.HasPrefix(r, "POST") {
					t.Fatalf("refused create still sent %q", r)
				}
			}
		})
	}
}

// TestDashboardsCreateWithoutTemplateUnchanged pins that the generic
// create path is untouched by --template.
func TestDashboardsCreateWithoutTemplateUnchanged(t *testing.T) {
	s := &templateServer{}
	out, _, err := runResource(t, s.handler(t), "", "dashboards", "create", "-f", "name=plain", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if reqs := s.requests(); len(reqs) != 1 || reqs[0] != `POST /dashboards {"name":"plain"}` {
		t.Fatalf("requests = %q", reqs)
	}
	if !strings.Contains(out, `"plain"`) {
		t.Fatalf("stdout = %s", out)
	}
	_, _, err = runResource(t, s.handler(t), "", "dashboards", "create")
	_ = wantCode(t, err, "usage_missing_body")
}

func TestWidgetTemplatesList(t *testing.T) {
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/widgets/templates" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"widgets":[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000c1","name":"Total Requests","type":"score",` +
			`"parameters":{"from":{"type":"string"}},"widgets":[]}]}`))
	}, "", "widgets", "templates", "list", "-o", "table")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Total Requests  score  from") {
		t.Fatalf("table = %q", out)
	}
}

func TestDashboardDetachFromTemplateNoBody(t *testing.T) {
	out, stderr, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}, "", "dashboards", "detach-from-template", wDash)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" || !strings.Contains(stderr, "Detached dashboard "+wDash) {
		t.Fatalf("a 204 must confirm on stderr, not print null: stdout %q stderr %q", out, stderr)
	}
}
