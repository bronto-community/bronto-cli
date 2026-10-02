package cli

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

const (
	aGroup  = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a7"
	aGroup2 = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a8"
	aUser   = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a9"
	aDS     = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000aa"
	aParser = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000ab"
	aMon    = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000ac"
	aMetric = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000ad"
)

// accessStub answers the list endpoints name resolution uses and records
// every other request.
type accessStub struct {
	method, path, query, body string
}

func (s *accessStub) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/users":
			_, _ = w.Write([]byte(`{"users":[{"id":"` + aUser + `","email":"alice@example.com"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/groups":
			_, _ = w.Write([]byte(`{"groups":[{"group_id":"` + aGroup + `","name":"oncall","description":"pager"},` +
				`{"group_id":"` + aGroup2 + `","name":"sre"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/logs":
			_, _ = w.Write([]byte(`{"logs":[{"log_id":"` + aDS + `","log":"nginx","collection":"default"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/parsers":
			_, _ = w.Write([]byte(`{"parsers":[{"id":"` + aParser + `","name":"kvp"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/roles":
			_, _ = w.Write([]byte(`{"roles":[{"role_id":"Admin","display_name":"Administrator Role"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/metrics":
			_, _ = w.Write([]byte(`{"metrics":[{"metric_id":"` + aMetric + `","metric_name":"system.cpu"}]}`))
		default:
			b, _ := io.ReadAll(r.Body)
			s.method, s.path, s.query, s.body = r.Method, r.URL.Path, r.URL.RawQuery, string(b)
			switch {
			case strings.HasSuffix(r.URL.Path, "/groups"): // users groups, live shape
				_, _ = w.Write([]byte(`{"groups":["` + aGroup + `","aaaaaaaa-aaaa-aaaa-aaaa-0000000000ff"]}`))
			case r.Method == http.MethodGet:
				_, _ = w.Write([]byte(`{}`))
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		}
	}
}

func TestGroupAddMembersResolvesUsersAndGroups(t *testing.T) {
	var s accessStub
	_, stderr, err := runResource(t, s.handler(t), "",
		"groups", "add-members", "oncall", "--users", "alice@example.com", "--groups", "sre")
	if err != nil {
		t.Fatal(err)
	}
	if s.method != http.MethodPost || s.path != "/groups/"+aGroup+"/members" {
		t.Fatalf("%s %s", s.method, s.path)
	}
	want := `{"members":[{"member_type":"USER","member_id":"` + aUser + `"},{"member_type":"GROUP","member_id":"` + aGroup2 + `"}]}`
	if s.body != want {
		t.Fatalf("body = %s\nwant   %s", s.body, want)
	}
	if !strings.Contains(stderr, "Added 2 member(s) to group oncall.") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestGroupRemoveMembersUsesDelete(t *testing.T) {
	var s accessStub
	if _, _, err := runResource(t, s.handler(t), "", "groups", "remove-members", aGroup, "--users", aUser); err != nil {
		t.Fatal(err)
	}
	if s.method != http.MethodDelete || s.path != "/groups/"+aGroup+"/members" ||
		s.body != `{"members":[{"member_type":"USER","member_id":"`+aUser+`"}]}` {
		t.Fatalf("%s %s %s", s.method, s.path, s.body)
	}
}

func TestGroupMembershipNeedsMembers(t *testing.T) {
	var s accessStub
	_, _, err := runResource(t, s.handler(t), "", "groups", "add-members", aGroup)
	if err == nil || !strings.Contains(err.Error(), "--users and/or --groups") {
		t.Fatalf("err = %v", err)
	}
	if s.path != "" {
		t.Fatalf("made a request without members: %s %s", s.method, s.path)
	}
}

func TestGroupMembershipDryRun(t *testing.T) {
	var s accessStub
	_, stderr, err := runResource(t, s.handler(t), "", "groups", "add-members", aGroup, "--users", aUser, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if s.path != "" {
		t.Fatalf("dry run made a request: %s %s", s.method, s.path)
	}
	if !strings.Contains(stderr, "DRY RUN: would add 1 member(s) to group") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// The live API returns bare group ids; users groups names them from the
// groups list and keeps the id of a group it can't find.
func TestUserGroupsNamesBareIDs(t *testing.T) {
	var s accessStub
	out, _, err := runResource(t, s.handler(t), "", "users", "groups", "alice@example.com", "-o", "csv")
	if err != nil {
		t.Fatal(err)
	}
	if s.path != "/users/"+aUser+"/groups" {
		t.Fatalf("path = %s", s.path)
	}
	if !strings.Contains(out, "oncall,pager,"+aGroup) || !strings.Contains(out, "aaaaaaaa-aaaa-aaaa-aaaa-0000000000ff") {
		t.Fatalf("out = %q", out)
	}
}

func TestMonitorNotificationsSendsBounds(t *testing.T) {
	var s accessStub
	if _, _, err := runResource(t, s.handler(t), "", "monitors", "notifications", aMon, "--since", "2h"); err != nil {
		t.Fatal(err)
	}
	if s.path != "/monitors/"+aMon+"/notifications" || !strings.Contains(s.query, "from_ts=") || !strings.Contains(s.query, "to_ts=") {
		t.Fatalf("%s ?%s", s.path, s.query)
	}
}

func TestMetricTopKeys(t *testing.T) {
	var s accessStub
	if _, _, err := runResource(t, s.handler(t), "", "metrics", "top-keys", "system.cpu", "--since", "15m"); err != nil {
		t.Fatal(err)
	}
	if s.path != "/metrics/"+aMetric+"/top-keys" || !strings.Contains(s.query, "time_range=Last+15+minutes") {
		t.Fatalf("%s ?%s", s.path, s.query)
	}
}

func TestDatasetParserSetGetUnset(t *testing.T) {
	var s accessStub
	if _, _, err := runResource(t, s.handler(t), "", "datasets", "parser", "set", "default/nginx", "kvp"); err != nil {
		t.Fatal(err)
	}
	if s.method != http.MethodPut || s.path != "/datasets/"+aDS+"/parser" || s.body != `{"parser_id":"`+aParser+`"}` {
		t.Fatalf("set: %s %s %s", s.method, s.path, s.body)
	}
	if _, _, err := runResource(t, s.handler(t), "", "datasets", "parser", "get", "nginx"); err != nil {
		t.Fatal(err)
	}
	if s.method != http.MethodGet || s.path != "/datasets/"+aDS+"/parser" {
		t.Fatalf("get: %s %s", s.method, s.path)
	}
	_, stderr, err := runResource(t, s.handler(t), "", "datasets", "parser", "unset", "nginx", "--quiet")
	if err != nil {
		t.Fatal(err)
	}
	if s.method != http.MethodDelete || s.path != "/datasets/"+aDS+"/parser" || stderr != "" {
		t.Fatalf("unset: %s %s stderr=%q", s.method, s.path, stderr)
	}
}

// System roles have non-UUID ids, which must resolve exactly.
func TestRoleResolvesByNonUUIDID(t *testing.T) {
	var s accessStub
	if _, _, err := runResource(t, s.handler(t), "", "roles", "get", "Admin"); err != nil {
		t.Fatal(err)
	}
	if s.path != "/roles/Admin" {
		t.Fatalf("path = %s", s.path)
	}
	if _, _, err := runResource(t, s.handler(t), "", "roles", "get", "Administrator Role"); err != nil {
		t.Fatal(err)
	}
	if s.path != "/roles/Admin" {
		t.Fatalf("by display_name: path = %s", s.path)
	}
}

// roles update is a full replacement that blanks or 500s on a partial
// body, so the CLI refuses one before calling the API.
func TestRoleUpdateRequiresFullBody(t *testing.T) {
	var s accessStub
	_, _, err := runResource(t, s.handler(t), "", "roles", "update", "Admin", "-f", "description=x")
	if err == nil || !strings.Contains(err.Error(), "display_name, permissions") {
		t.Fatalf("err = %v", err)
	}
	if s.path != "" {
		t.Fatalf("made a request: %s %s", s.method, s.path)
	}
	if _, _, err := runResource(t, s.handler(t), "", "roles", "update", "Admin",
		"-f", "display_name=A", "-f", `permissions=["logs_read"]`); err != nil {
		t.Fatal(err)
	}
	if s.method != http.MethodPut || s.path != "/roles/Admin" {
		t.Fatalf("%s %s", s.method, s.path)
	}
}

func TestPermissionsListFlattensForTables(t *testing.T) {
	out, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"resources":[{"group_name":"Logs","permissions":[` +
			`{"name":"logs_read","type":"read","display_name":"Read logs"},` +
			`{"name":"logs_delete","type":"delete","display_name":"Delete logs"}]}]}`))
	}, "", "permissions", "list", "-o", "csv")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"group,name,type,display_name", "Logs,logs_read,read,Read logs", "Logs,logs_delete,delete,Delete logs"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, missing %q", out, want)
		}
	}
}
