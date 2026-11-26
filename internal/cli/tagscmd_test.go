package cli

import (
	"net/http"
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
		// The list prints types the way the API answers them (upper case
		// live); the same word, in any case, works as input.
		{[]string{"tags", "create", "DASHBOARD", "Overview", "team=sre"},
			`POST /tags {"entity":{"id":"` + tDash + `","type":"dashboard"},"name":"team","value":"sre"}`},
		{[]string{"tags", "delete", "Slo", tDash, "team", "--yes"},
			`DELETE /tags {"entity":{"id":"` + tDash + `","type":"slo"},"name":"team"}`},
		{[]string{"tags", "list", "--entity-type", "DASHBOARD"}, "GET /tags/search?entity_type=dashboard"},
	})
	checkCmdUsageErrors(t, [][]string{
		{"tags", "create", "dashboard", tDash, "team"},  // no value
		{"tags", "create", "planet", tDash, "team=sre"}, // unknown entity type
		{"tags", "list", "--entity-type", "planet"},     // unknown entity type
		{"tags", "delete", "dashboard", tDash, "team"},  // non-TTY without --yes
	})

	var got []string
	out, _, err := runResource(t, cmdStub(&got), "", "tags", "delete", "dashboard", "Overview", "team", "--dry-run")
	if err != nil || len(got) != 0 || !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, tDash) {
		t.Fatalf("tags delete --dry-run: %v, requests %v, stdout %q", err, got, out)
	}
	// One row per name, value and entity type; json keeps the API rows.
	out, _, err = runResource(t, cmdStub(&got), "", "tags", "list", "-o", "csv")
	if err != nil || out != "name,value,type,entities\nteam,sre,dashboard,2\nteam,sre,log,1\n" {
		t.Fatalf("tags list csv: %v, %q", err, out)
	}
	out, _, err = runResource(t, cmdStub(&got), "", "tags", "list", "-o", "json")
	if err != nil || !strings.Contains(out, `"entities"`) {
		t.Fatalf("tags list json: %v, %q", err, out)
	}
}

// TestTagsListRows: the table reads the spec's shape (values, types as
// strings) as well as the live one, and a tag without entity types, or
// without values, still gets a row.
func TestTagsListRows(t *testing.T) {
	const payload = `{"tags":[` +
		`{"name":"env","values":[{"value":"prod","types":["dashboard","log"]}]},` +
		`{"name":"orphan","value":[{"value":"x","types":[]}]},` +
		`{"name":"bare","value":[]}]}`
	out, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	}, "", "tags", "list", "-o", "csv")
	want := "name,value,type,entities\nenv,prod,dashboard,\nenv,prod,log,\norphan,x,,\nbare,,,\n"
	if err != nil || out != want {
		t.Fatalf("tags list csv: %v\n got %q\nwant %q", err, out, want)
	}
}
