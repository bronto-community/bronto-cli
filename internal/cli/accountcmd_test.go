package cli

import (
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"net/http"
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

// TestWidgetsPatchPrintsTheWidgetAsJSON: the PATCH answers 204, so with
// -o json or --jq the command prints the widget fetched afterwards, as
// widgets update does, and otherwise confirms on stderr.
func TestWidgetsPatchPrintsTheWidgetAsJSON(t *testing.T) {
	var got []string
	handler := func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPatch {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + tDash + `","name":"renamed"}`))
	}
	out, errOut, err := runResource(t, handler, "", "widgets", "patch", tDash, "-f", "name=renamed")
	if err != nil || out != "" || !strings.Contains(errOut, "Updated widget "+tDash) {
		t.Fatalf("plain: %v, stdout %q, stderr %q", err, out, errOut)
	}
	got = nil
	out, _, err = runResource(t, handler, "", "widgets", "patch", tDash, "-f", "name=renamed", "-o", "json", "--jq", ".name")
	if err != nil || strings.TrimSpace(out) != `"renamed"` {
		t.Fatalf("-o json --jq: %v, stdout %q", err, out)
	}
	if want := "PATCH /widgets/" + tDash + " GET /widgets/" + tDash; strings.Join(got, " ") != want {
		t.Fatalf("requests %v, want %s", got, want)
	}
}

// TestPatchAndResolveRejectInvalidCombinations: bodies and flag pairs the
// API's contract excludes are usage errors before any request.
func TestPatchAndResolveRejectInvalidCombinations(t *testing.T) {
	for _, args := range [][]string{
		{"widgets", "patch", tDash, "-f", "name=x", "-f", `display={"stacked_bars":true}`},
		{"widgets", "patch", tDash, "-f", "name=x", "-f", `aux={"mode":"sql"}`},
		{"widgets", "patch", tDash, "-f", "title=x"},
		{"datasets", "resolve", "--from-expr", "a = 'b'", "--expand-type", "SYNONYM", "--match-type", "SUBSTRING"},
		{"datasets", "resolve", "--from-expr", "a = 'b'", "--expand-type", "synonym", "--match-type", "isubstring"},
	} {
		var got []string
		_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
			got = append(got, r.Method+" "+r.URL.Path)
			_, _ = w.Write([]byte(`{}`))
		}, "", args...)
		if clierr.ExitCode(err) != 2 || len(got) != 0 {
			t.Errorf("%v: err %v (exit %d), requests %v; want a usage error and no request", args, err, clierr.ExitCode(err), got)
		}
	}
	// display and sort together are one part.
	_, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }, "",
		"widgets", "patch", tDash, "-f", `display={"stacked_bars":true}`, "-f", `sort={"direction":"asc"}`)
	if err != nil {
		t.Errorf("display+sort: %v", err)
	}
}
