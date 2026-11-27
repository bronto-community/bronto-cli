package cli

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func TestOrgCommands(t *testing.T) {
	checkCmdRequests(t, []cmdRequest{
		{[]string{"org", "show"}, "GET /customer"},
		{[]string{"org", "update", "-f", "customer_name=Acme", "-f", `billing_emails=["b@example.com"]`},
			`PATCH /customer {"billing_emails":["b@example.com"],"customer_name":"Acme"}`},
	})
	checkCmdUsageErrors(t, [][]string{{"org", "update"}}) // no body

	// The API answers 400 unless the body has both fields, so a partial
	// body is refused before the request, --dry-run included.
	var got []string
	for _, args := range [][]string{
		{"-f", "customer_name=Acme"},
		{"-f", `billing_emails=["b@example.com"]`, "--dry-run"},
	} {
		_, _, err := runResource(t, cmdStub(&got), "", append([]string{"org", "update"}, args...)...)
		var ce *clierr.Error
		if !errors.As(err, &ce) || clierr.ExitCode(err) != 2 || ce.Code != "usage_missing_fields" || len(got) != 0 ||
			!strings.Contains(ce.Hint, "bronto org show") || !strings.Contains(ce.Hint, "-f customer_name=") || !strings.Contains(ce.Hint, "billing_emails=") {
			t.Errorf("%v: err = %v, requests %v, want usage_missing_fields with the full form and 'bronto org show'", args, err, got)
		}
	}
	// A body that isn't a JSON object goes through to the API.
	if _, _, err := runResource(t, cmdStub(&got), `["x"]`, "org", "update", "--input", "-"); err != nil || len(got) == 0 {
		t.Errorf("non-object body: err = %v, requests %v, want it sent", err, got)
	}

	out, _, err := runResource(t, cmdStub(&got), "", "org", "show")
	if err != nil || !strings.Contains(out, `"ok"`) {
		t.Fatalf("org show: %v, stdout %q", err, out)
	}
	got = nil
	out, _, err = runResource(t, cmdStub(&got), "", "org", "update", "-f", "customer_name=Acme", "-f", `billing_emails=["b@example.com"]`, "--dry-run")
	if err != nil || len(got) != 0 || !strings.Contains(out, `"dry_run": true`) {
		t.Fatalf("org update --dry-run: %v, requests %v, stdout %q", err, got, out)
	}
}

// TestWriteWithoutBodyPrintsTheResultAsJSON: a write the API answers with
// no body confirms on stderr, but with -o json or --jq it prints the
// object fetched afterwards instead of nothing (sendWrite's fetch path).
func TestWriteWithoutBodyPrintsTheResultAsJSON(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"customer_name":"Acme"}`))
	}
	out, errOut, err := runResource(t, handler, "", "org", "update", "-f", "customer_name=Acme", "-f", `billing_emails=[]`)
	if err != nil || out != "" || !strings.Contains(errOut, "Updated the organization record.") {
		t.Fatalf("plain: %v, stdout %q, stderr %q", err, out, errOut)
	}
	for _, flags := range [][]string{{"-o", "json"}, {"-o", "json", "--jq", ".customer_name"}} {
		out, _, err = runResource(t, handler, "", append([]string{"org", "update", "-f", "customer_name=Acme", "-f", `billing_emails=[]`}, flags...)...)
		if err != nil || !strings.Contains(out, "Acme") {
			t.Fatalf("%v: %v, stdout %q", flags, err, out)
		}
	}
}
