package cli

import (
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
