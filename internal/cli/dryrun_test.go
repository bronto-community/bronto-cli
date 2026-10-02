package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// TestDryRunCreatePrintsPlanWithoutContact pins the --dry-run contract for
// mutating verbs: the exact would-be request prints as a plan document and
// the server is never contacted.
func TestDryRunCreatePrintsPlanWithoutContact(t *testing.T) {
	out, _, err := runResource(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be contacted under --dry-run")
	}, "", "monitors", "create", "-f", "name=x", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var plan map[string]any
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("plan output not JSON: %v (%q)", err, out)
	}
	if plan["dry_run"] != true || plan["method"] != "POST" || plan["path"] != "/monitors" {
		t.Fatalf("plan = %v", plan)
	}
	body, _ := plan["body"].(map[string]any)
	if body["name"] != "x" {
		t.Fatalf("plan body = %v", body)
	}
}

// decodePlan parses a dry-run plan from stdout and checks its method and
// path; stderr must stay empty, as it does for create --dry-run.
func decodePlan(t *testing.T, out, stderr, method, path string) map[string]any {
	t.Helper()
	var plan map[string]any
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("plan output not JSON: %v (%q)", err, out)
	}
	if plan["dry_run"] != true || plan["method"] != method || plan["path"] != path {
		t.Fatalf("plan = %v, want %s %s", plan, method, path)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty (the plan on stdout is the whole answer)", stderr)
	}
	return plan
}

func TestDryRunDeleteSkipsConfirmationAndContact(t *testing.T) {
	// No --yes and no TTY: without --dry-run this would be a usage error;
	// with it, nothing is destructive so no confirmation is needed.
	out, stderr, err := runResource(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be contacted under --dry-run")
	}, "", "monitors", "delete", "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	plan := decodePlan(t, out, stderr, "DELETE", "/monitors/aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1")
	if _, ok := plan["body"]; ok {
		t.Fatalf("delete plan has a body: %v", plan)
	}
}

// TestDryRunDeleteByNameShowsResolvedID pins: the plan's path carries the
// id the name resolved to (the read still runs), not the name typed.
func TestDryRunDeleteByNameShowsResolvedID(t *testing.T) {
	out, stderr, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("mutating request under --dry-run: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1","name":"cpu"}]`))
	}, "", "monitors", "delete", "cpu", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	decodePlan(t, out, stderr, "DELETE", "/monitors/aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1")
}

// TestDryRunActionVerbsPrintPlan pins the create-style plan on stdout for
// every hand-written mutating verb, which used to print only a stderr note.
func TestDryRunActionVerbsPrintPlan(t *testing.T) {
	const ds = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000d1"
	const parser = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000d2"
	const dash = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000d3"
	const mon = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1"
	const usr = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1"
	cases := []struct {
		args         []string
		method, path string
		body         string // compact JSON of the plan body, "" = none
	}{
		{[]string{"monitors", "mute", mon}, "POST", "/monitors/" + mon + "/status", `{"mute_until":-1}`},
		{[]string{"monitors", "mute", mon, "--unmute"}, "POST", "/monitors/" + mon + "/status", `{"mute_until":0}`},
		{[]string{"users", "deactivate", usr}, "POST", "/users/" + usr + "/deactivate", ""},
		{[]string{"users", "reactivate", usr}, "POST", "/users/" + usr + "/reactivate", ""},
		{[]string{"users", "resend-invite", usr}, "POST", "/users/" + usr + "/resend-invite", ""},
		{[]string{"groups", "add-members", aGroup, "--users", aUser}, "POST", "/groups/" + aGroup + "/members",
			`{"members":[{"member_id":"` + aUser + `","member_type":"USER"}]}`},
		{[]string{"groups", "remove-members", aGroup, "--users", aUser}, "DELETE", "/groups/" + aGroup + "/members",
			`{"members":[{"member_id":"` + aUser + `","member_type":"USER"}]}`},
		{[]string{"datasets", "parser", "set", ds, parser}, "PUT", "/datasets/" + ds + "/parser", `{"parser_id":"` + parser + `"}`},
		{[]string{"datasets", "parser", "unset", ds}, "DELETE", "/datasets/" + ds + "/parser", ""},
		{[]string{"dashboards", "attach-widgets", dash, "--widget-ids", wWidget}, "POST", "/dashboards/" + dash + "/widgets",
			`{"widget_ids":["` + wWidget + `"]}`},
		{[]string{"dashboards", "remove-widget", dash, wWidget}, "DELETE", "/dashboards/" + dash + "/widgets/" + wWidget, ""},
		{[]string{"widgets", "attach-widgets", wWidget, "--widget-ids", "id-1"}, "POST", "/widgets/" + wWidget + "/widgets",
			`{"widget_ids":["id-1"]}`},
		{[]string{"widgets", "remove-widget", wWidget, "id-1"}, "DELETE", "/widgets/" + wWidget + "/widgets/id-1", ""},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args[:2], " "), func(t *testing.T) {
			args := append(append([]string{}, c.args...), "--dry-run", "-o", "json")
			out, stderr, err := runResource(t, func(_ http.ResponseWriter, r *http.Request) {
				t.Fatalf("server must not be contacted under --dry-run: %s %s", r.Method, r.URL.Path)
			}, "", args...)
			if err != nil {
				t.Fatal(err)
			}
			plan := decodePlan(t, out, stderr, c.method, c.path)
			body, hasBody := plan["body"]
			switch {
			case c.body == "" && hasBody:
				t.Fatalf("unexpected body %v", body)
			case c.body != "":
				b, _ := json.Marshal(body)
				if string(b) != c.body {
					t.Fatalf("body = %s, want %s", b, c.body)
				}
			}
		})
	}
}

// TestDryRunReadsStillExecute pins that GETs run normally so list/get and
// dataset-name resolution keep working under --dry-run.
func TestDryRunReadsStillExecute(t *testing.T) {
	contacted := false
	out, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		contacted = true
		_, _ = w.Write([]byte(`[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1"}]`))
	}, "", "monitors", "list", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !contacted {
		t.Fatal("reads must still execute under --dry-run")
	}
	if !strings.Contains(out, "aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1") {
		t.Fatalf("out = %q", out)
	}
}

func TestDryRunExportsCreatePrintsPlanNotPoll(t *testing.T) {
	out, _, err := runResource(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be contacted under --dry-run")
	}, "", "exports", "create",
		"--dataset", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "--since", "1h",
		"--wait", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var plan map[string]any
	if err := json.Unmarshal([]byte(out), &plan); err != nil || plan["dry_run"] != true || plan["method"] != "POST" {
		t.Fatalf("plan = %q err=%v", out, err)
	}
}

func TestMaxRetriesInvalidConfigIsError(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"ping", "--api-key", "k", "--max-retries", "-3"})
	err := root.Execute()
	var ce *clierr.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "config_invalid_max_retries" {
		t.Fatalf("want config_invalid_max_retries, got %v", err)
	}
}
