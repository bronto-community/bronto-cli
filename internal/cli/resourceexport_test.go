package cli

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

const exportHCL = "resource \"bronto_x\" \"x\" {\n  name = \"x\"\n}\n"

// TestResourceExport: the generated export verb GETs <id>/export with the
// format (and, for monitors, include_dependencies) and writes the HCL the
// API answers, byte for byte, whatever -o says.
func TestResourceExport(t *testing.T) {
	const id = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f1"
	cases := []struct {
		args      []string
		wantPath  string
		wantQuery string
	}{
		{[]string{"users", "export", id}, "/users/" + id + "/export", "format=terraform"},
		{[]string{"groups", "export", id, "--format", "opentofu"}, "/groups/" + id + "/export", "format=opentofu"},
		{[]string{"monitors", "export", id, "--include-dependencies"}, "/monitors/" + id + "/export", "format=terraform&include_dependencies=true"},
		{[]string{"slack", "export", id, "-o", "json"}, "/integrations/slack/" + id + "/export", "format=terraform"},
		{[]string{"webhooks", "export", id}, "/integrations/webhooks/" + id + "/export", "format=terraform"},
		{[]string{"providers", "export", id}, "/integrations/providers/" + id + "/export", "format=terraform"},
		{[]string{"dashboards", "export", id, "--format", "terraform", "--include-dependencies"}, "/dashboards/" + id + "/export", "format=terraform&include_dependencies=true"},
		{[]string{"sso", "idps", "export", "Google"}, "/sso/idps/Google/export", "format=terraform"},
		{[]string{"sso", "mappings", "export", "--format", "opentofu"}, "/sso/mappings/export", "format=opentofu"},
	}
	for _, c := range cases {
		var got []string
		out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
			got = append(got, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(exportHCL))
		}, "", c.args...)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if want := []string{"GET " + c.wantPath + "?" + c.wantQuery}; !slices.Equal(got, want) {
			t.Errorf("%v requested %v, want %v", c.args, got, want)
		}
		if out != exportHCL {
			t.Errorf("%v stdout = %q, want the HCL verbatim", c.args, out)
		}
	}
}

// TestResourceExportByName: export takes a name wherever get does.
func TestResourceExportByName(t *testing.T) {
	const id = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f2"
	var exported string
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/users" {
			_, _ = w.Write([]byte(`{"users":[{"id":"` + id + `","email":"ada@example.com"}]}`))
			return
		}
		exported = r.URL.Path
		_, _ = w.Write([]byte(exportHCL))
	}, "", "users", "export", "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if exported != "/users/"+id+"/export" || out != exportHCL {
		t.Fatalf("exported %q, stdout %q", exported, out)
	}
}

// TestResourceExportRejectsBadFlags: an unknown format, and --jq and
// --fields (the output is HCL, not JSON), fail before any request.
func TestResourceExportRejectsBadFlags(t *testing.T) {
	const id = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f3"
	for _, args := range [][]string{
		{"monitors", "export", id, "--format", "pulumi"},
		{"monitors", "export", id, "--jq", ".x"},
		{"monitors", "export", id, "--fields", "name"},
		{"monitors", "export", id, "--fields", "?"},
		{"dashboards", "export", id, "--format", "terraform", "--fields", "name"},
		{"sso", "idps", "export", "Google", "--fields", "name"},
		{"sso", "mappings", "export", "--fields", "name"},
		{"dashboards", "export", id, "--format", "pulumi"},
		// --include-dependencies belongs to the Terraform export.
		{"dashboards", "export", id, "--include-dependencies"},
	} {
		_, _, err := runResource(t, func(http.ResponseWriter, *http.Request) {
			t.Fatalf("%v made a request", args)
		}, "", args...)
		var ce *clierr.Error
		if !errors.As(err, &ce) || !strings.HasPrefix(ce.Code, "usage_") {
			t.Errorf("%v: err = %v, want a usage error", args, err)
		}
	}
}

// TestExportVerbOnlyWhereTheAPIHasIt pins which resources get the verb.
func TestExportVerbOnlyWhereTheAPIHasIt(t *testing.T) {
	var got []string
	for _, d := range resourceRegistry {
		if d.Export {
			got = append(got, d.display())
		}
	}
	want := []string{"monitors", "dashboards", "webhooks", "slack", "providers", "users", "groups"}
	if !slices.Equal(got, want) {
		t.Fatalf("resources with export = %v, want %v", got, want)
	}
	if !resourceRegistry[0].ExportDeps {
		t.Fatal("monitors export must offer --include-dependencies")
	}
	c, _, err := NewRootCmd().Find([]string{"users", "export"})
	if err != nil || c.Flags().Lookup("include-dependencies") != nil {
		t.Fatalf("users export must not have --include-dependencies (err %v)", err)
	}
}

// TestProvidersRegistration: provider integrations have all five verbs,
// update is a PUT, and the list reads the "integrations" array.
func TestProvidersRegistration(t *testing.T) {
	const id = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000f4"
	var gotMethod, gotPath string
	record := func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{"integrations":[{"id":"` + id + `","name":"AWS DevOps Agent","provider":"aws-devops-agent"}]}`))
	}
	for _, c := range []struct {
		args                 []string
		wantMethod, wantPath string
	}{
		{[]string{"providers", "list"}, http.MethodGet, "/integrations/providers"},
		{[]string{"providers", "get", id}, http.MethodGet, "/integrations/providers/" + id},
		{[]string{"providers", "create", "-f", "provider=pagerduty", "-f", "url=https://example.com/h", "-f", "secret=s", "-f", "auth_type=api_key"}, http.MethodPost, "/integrations/providers"},
		{[]string{"providers", "update", id, "-f", "url=https://example.com/h", "-f", "auth_type=hmac"}, http.MethodPut, "/integrations/providers/" + id},
		{[]string{"providers", "delete", id, "--yes"}, http.MethodDelete, "/integrations/providers/" + id},
	} {
		if _, _, err := runResource(t, record, "", c.args...); err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if gotMethod != c.wantMethod || gotPath != c.wantPath {
			t.Fatalf("%v -> %s %s, want %s %s", c.args, gotMethod, gotPath, c.wantMethod, c.wantPath)
		}
	}
	out, _, err := runResource(t, record, "", "providers", "list", "-o", "csv")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "name,provider,url,auth_type,has_secret,id\n") || !strings.Contains(out, "AWS DevOps Agent,aws-devops-agent") {
		t.Fatalf("providers list csv = %q", out)
	}
}
