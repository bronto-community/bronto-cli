package cli

import (
	"strings"
	"testing"
)

func TestAccountCommands(t *testing.T) {
	checkCmdRequests(t, []cmdRequest{
		{[]string{"policies", "list", "logset", tDash}, "GET /policies/resources/logset/" + tDash},
		{[]string{"datasets", "resolve", "--from-expr", "env = 'prod'", "--where", "a = 1", "--since", "10m", "--match-type", "IEXACT"},
			`POST /datasets/resolve {"from_expr":"env = 'prod'","match_type":"IEXACT","time_range":"Last 10 minutes","where":"a = 1"}`},
		// A read, though a POST: --dry-run still sends it.
		{[]string{"datasets", "resolve", "--from-expr", "a = 1", "--since", "10m", "--dry-run"},
			`POST /datasets/resolve {"from_expr":"a = 1","time_range":"Last 10 minutes"}`},
		{[]string{"widgets", "patch", tDash, "-f", `display={"stacked_bars":true}`},
			"PATCH /widgets/" + tDash + ` {"display":{"stacked_bars":true}}`},
	})
	checkCmdUsageErrors(t, [][]string{
		{"datasets", "resolve", "--where", "a = 1"}, // live, the API answers 500 without --from-expr
		{"widgets", "patch", tDash},                 // no body
	})

	var got []string
	out, _, err := runResource(t, cmdStub(&got), "", "widgets", "patch", tDash, "-f", "name=x", "--dry-run")
	if err != nil || len(got) != 0 || !strings.Contains(out, `"dry_run": true`) {
		t.Fatalf("widgets patch --dry-run: %v, requests %v, stdout %q", err, got, out)
	}
}
