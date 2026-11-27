package cli

import (
	"strings"
	"testing"
)

func TestAccessCommands(t *testing.T) {
	access := "/organizations/" + tOrg + "/access"
	checkCmdRequests(t, []cmdRequest{
		{[]string{"access", "list"}, "GET " + access}, // the active org
		{[]string{"access", "list", "--org", "o2"}, "GET /organizations/o2/access"},
		{[]string{"access", "check", "--user", "ada@example.com"}, "GET " + access + "/user/" + tUser + "/check"},
		{[]string{"access", "check", "--group", "sre"}, "GET " + access + "/group/" + tGroup + "/check"},
		{[]string{"access", "grant", "--user", "ada@example.com", "--roles", "Admin,Auditor"},
			"POST " + access + "/user/" + tUser + ` {"roles":["Admin","aaaaaaaa-aaaa-aaaa-aaaa-00000000a009"],"status":"active"}`},
		{[]string{"access", "grant", "--group", "sre", "--roles", "Admin", "--status", "invited"},
			"POST " + access + "/group/" + tGroup + ` {"roles":["Admin"],"status":"invited"}`},
		{[]string{"access", "revoke", "--user", tUser, "--yes"}, "DELETE " + access + "/user/" + tUser},
		{[]string{"users", "access", "ada@example.com"}, "GET /users/" + tUser + "/access"},
	})
	checkCmdUsageErrors(t, [][]string{
		{"access", "check"}, // neither --user nor --group
		{"access", "check", "--user", tUser, "--group", tGroup},
		{"access", "grant", "--user", tUser}, // no roles
		{"access", "grant", "--user", tUser, "--roles", "Admin", "--status", "gone"},
		{"access", "revoke", "--user", tUser}, // non-TTY without --yes
	})

	var got []string
	out, errOut, err := runResource(t, cmdStub(&got), "", "access", "grant", "--group", "sre", "--roles", "Admin")
	if err != nil || out != "" || !strings.Contains(errOut, "Granted group sre access") {
		t.Fatalf("access grant: %v, stdout %q, stderr %q", err, out, errOut)
	}
	got = nil
	out, _, err = runResource(t, cmdStub(&got), "", "access", "revoke", "--group", "sre", "--dry-run")
	if err != nil || len(got) != 0 || !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, tGroup) {
		t.Fatalf("access revoke --dry-run: %v, requests %v, stdout %q", err, got, out)
	}
	out, _, err = runResource(t, cmdStub(&got), "", "access", "list", "-o", "csv")
	if err != nil || !strings.HasPrefix(out, "type,id,roles,status,created_at\nuser,u1,") {
		t.Fatalf("access list csv: %v, %q", err, out)
	}
	out, _, err = runResource(t, cmdStub(&got), "", "users", "access", tUser, "-o", "csv")
	if err != nil || out != "name,active,id\nAcme,true,o1\n" {
		t.Fatalf("users access csv: %v, %q", err, out)
	}
}
