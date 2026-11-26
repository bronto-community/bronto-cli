package cli

import (
	"strings"
	"testing"
)

func TestTagsCommands(t *testing.T) {
	checkCmdRequests(t, []cmdRequest{
		{[]string{"tags", "list", "--name", "team", "--entity-type", "dashboard"}, "GET /tags/search?entity_type=dashboard&name=team"},
		{[]string{"tags", "create", "dashboard", "Overview", "team=sre"},
			`POST /tags {"entity":{"id":"` + tDash + `","type":"dashboard"},"name":"team","value":"sre"}`},
		{[]string{"tags", "update", "dashboard", tDash, "team=a=b"},
			`PUT /tags {"entity":{"id":"` + tDash + `","type":"dashboard"},"name":"team","value":"a=b"}`},
		{[]string{"tags", "delete", "slo", tDash, "team", "--yes"},
			`DELETE /tags {"entity":{"id":"` + tDash + `","type":"slo"},"name":"team"}`},
	})
	checkCmdUsageErrors(t, [][]string{
		{"tags", "create", "dashboard", tDash, "team"},  // no value
		{"tags", "create", "planet", tDash, "team=sre"}, // unknown entity type
		{"tags", "delete", "dashboard", tDash, "team"},  // non-TTY without --yes
	})

	var got []string
	out, _, err := runResource(t, cmdStub(&got), "", "tags", "delete", "dashboard", "Overview", "team", "--dry-run")
	if err != nil || len(got) != 0 || !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, tDash) {
		t.Fatalf("tags delete --dry-run: %v, requests %v, stdout %q", err, got, out)
	}
	// One row per name, value and entity type; json keeps the API rows.
	out, _, err = runResource(t, cmdStub(&got), "", "tags", "list", "-o", "csv")
	if err != nil || out != "name,value,type,entities\nteam,sre,DASHBOARD,2\nteam,sre,LOG,1\n" {
		t.Fatalf("tags list csv: %v, %q", err, out)
	}
	out, _, err = runResource(t, cmdStub(&got), "", "tags", "list", "-o", "json")
	if err != nil || !strings.Contains(out, `"entities"`) {
		t.Fatalf("tags list json: %v, %q", err, out)
	}
}
