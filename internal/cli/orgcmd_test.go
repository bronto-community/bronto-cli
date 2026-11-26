package cli

import (
	"net/http"
	"strings"
	"testing"
)

func TestOrgCommands(t *testing.T) {
	checkCmdRequests(t, []cmdRequest{
		{[]string{"org", "show"}, "GET /customer"},
		{[]string{"org", "update", "-f", "customer_name=Acme", "-f", `billing_emails=["b@example.com"]`},
			`PATCH /customer {"billing_emails":["b@example.com"],"customer_name":"Acme"}`},
	})
	checkCmdUsageErrors(t, [][]string{{"org", "update"}}) // no body

	var got []string
	out, _, err := runResource(t, cmdStub(&got), "", "org", "show")
	if err != nil || !strings.Contains(out, `"ok"`) {
		t.Fatalf("org show: %v, stdout %q", err, out)
	}
	got = nil
	out, _, err = runResource(t, cmdStub(&got), "", "org", "update", "-f", "customer_name=Acme", "--dry-run")
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
	out, errOut, err := runResource(t, handler, "", "org", "update", "-f", "customer_name=Acme")
	if err != nil || out != "" || !strings.Contains(errOut, "Updated the organization record.") {
		t.Fatalf("plain: %v, stdout %q, stderr %q", err, out, errOut)
	}
	for _, flags := range [][]string{{"-o", "json"}, {"-o", "json", "--jq", ".customer_name"}} {
		out, _, err = runResource(t, handler, "", append([]string{"org", "update", "-f", "customer_name=Acme"}, flags...)...)
		if err != nil || !strings.Contains(out, "Acme") {
			t.Fatalf("%v: %v, stdout %q", flags, err, out)
		}
	}
}
