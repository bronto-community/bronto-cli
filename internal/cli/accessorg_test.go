package cli

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// accessOrgStub serves GET /organizations with orgs, 204 for writes, and
// list for every other read, recording the non-lookup requests.
func accessOrgStub(orgs, list string, got *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/organizations" {
			_, _ = w.Write([]byte(orgs))
			return
		}
		*got = append(*got, r.Method+" "+r.URL.Path)
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(list))
	}
}

// TestAccessNeverGuessesTheOrg: without --org the access
// commands act on the active organization, or on the only one; with
// several and none active they refuse (exit 2) and list them, instead of
// taking the first as search's deep link does.
func TestAccessNeverGuessesTheOrg(t *testing.T) {
	const twoInactive = `{"organisations":[{"id":"o1","name":"Acme EU"},{"id":"o2","name":"Acme US"}]}`
	for _, args := range [][]string{
		{"access", "grant", "--user", tUser, "--roles", tUser},
		{"access", "revoke", "--user", tUser, "--yes"},
		{"access", "list"},
		{"access", "check", "--user", tUser},
	} {
		var got []string
		_, _, err := runResource(t, accessOrgStub(twoInactive, `{}`, &got), "", args...)
		var ce *clierr.Error
		if !errors.As(err, &ce) || clierr.ExitCode(err) != 2 {
			t.Fatalf("%v: err = %v, want a usage error", args, err)
		}
		if len(got) != 0 {
			t.Errorf("%v made requests %v", args, got)
		}
		if strings.Contains(ce.Message+ce.Hint, "deep link") || !strings.Contains(ce.Hint, "o1 (Acme EU)") || !strings.Contains(ce.Hint, "--org") {
			t.Errorf("%v: message %q, hint %q", args, ce.Message, ce.Hint)
		}
	}

	// No organization at all: its own message, not the deep link's.
	var got []string
	_, _, err := runResource(t, accessOrgStub(`{"organisations":[]}`, `{}`, &got), "", "access", "list")
	var ce *clierr.Error
	if !errors.As(err, &ce) || strings.Contains(ce.Message+ce.Hint, "deep link") || !strings.Contains(ce.Hint, "--org") {
		t.Fatalf("no orgs: err = %v", err)
	}

	// The only organization is unambiguous even when not flagged active,
	// and the confirmation names it.
	got = nil
	_, errOut, err := runResource(t, accessOrgStub(`{"organisations":[{"id":"o1","name":"Acme EU"}]}`, `{}`, &got), "",
		"access", "revoke", "--user", tUser, "--yes")
	if err != nil || len(got) != 1 || got[0] != "DELETE /organizations/o1/access/user/"+tUser {
		t.Fatalf("one org: %v, requests %v", err, got)
	}
	if want := "Revoked the access of user " + tUser + " to organization o1."; !strings.Contains(errOut, want) {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}
	_, errOut, err = runResource(t, accessOrgStub(`{}`, `{}`, &got), "",
		"access", "grant", "--user", tUser, "--roles", tUser, "--org", "o9")
	if want := "Granted user " + tUser + " access to organization o9 with "; err != nil || !strings.Contains(errOut, want) {
		t.Errorf("grant: %v, stderr = %q, want %q", err, errOut, want)
	}
}

// TestAccessIgnoresTheLinkOrgOverride: org_id/BRONTO_ORG_ID is the
// override for web-app links; it must not redirect a grant or revoke away
// from the key's active organization.
func TestAccessIgnoresTheLinkOrgOverride(t *testing.T) {
	t.Setenv("BRONTO_ORG_ID", "o-link")
	const orgs = `{"organisations":[{"id":"o-link","name":"Other"},{"id":"o1","name":"Acme EU","active":true}]}`
	var got []string
	_, _, err := runResource(t, accessOrgStub(orgs, `{}`, &got), "", "access", "revoke", "--user", tUser, "--yes")
	if err != nil || len(got) != 1 || got[0] != "DELETE /organizations/o1/access/user/"+tUser {
		t.Fatalf("err %v, requests %v, want one DELETE on o1", err, got)
	}
}

// TestAccessRevokePromptNamesTheOrg: at a terminal the prompt says which
// organization the access is revoked from.
func TestAccessRevokePromptNamesTheOrg(t *testing.T) {
	origOut, origIn := stdoutIsTTY, stdinIsTTY
	stdoutIsTTY, stdinIsTTY = func() bool { return true }, func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY, stdinIsTTY = origOut, origIn })
	var got []string
	_, errOut, err := runResource(t, accessOrgStub(`{}`, `{}`, &got), "n\n", "access", "revoke", "--user", tUser, "--org", "o9")
	if err != nil || len(got) != 0 {
		t.Fatalf("declined revoke: %v, requests %v", err, got)
	}
	if want := "Revoke the access of user " + tUser + " to organization o9?"; !strings.Contains(errOut, want) {
		t.Errorf("prompt = %q, want %q", errOut, want)
	}
}

// TestAccessListShapes: the live API answers a top-level members array,
// the spec documents items[].members[]; both print one row per member,
// with createdAt readable rather than raw epoch milliseconds.
func TestAccessListShapes(t *testing.T) {
	const member = `{"type":"user","id":"u1","status":"active","createdAt":1784280308383}`
	for name, payload := range map[string]string{
		"live": `{"members":[` + member + `]}`,
		"spec": `{"items":[{"members":[` + member + `]},{"members":[{"type":"group","id":"g1","status":"invited"}]}]}`,
	} {
		var got []string
		out, _, err := runResource(t, accessOrgStub(`{}`, payload, &got), "", "access", "list", "--org", "o1", "-o", "csv")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if lines[0] != "type,id,roles,status,createdAt" || len(lines) < 2 || lines[1] != "user,u1,,active,2026-07-17T09:25:08Z" {
			t.Errorf("%s: csv = %q", name, out)
		}
		if name == "spec" && (len(lines) != 3 || lines[2] != "group,g1,,invited,") {
			t.Errorf("spec: csv = %q, want the second item's member as a row", out)
		}
	}
}
