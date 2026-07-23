package cli

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestAPIDryRunMutatingMethodsPrintPlanWithoutContact extends the --dry-run
// contract pinned in dryrun_test.go to the escape-hatch command: `bronto
// api` is precisely where a user previews an undocumented mutation, so a
// mutating method under --dry-run must print the plan document and never
// contact the server — same shape doJSONRequest produces for every other
// mutating command.
func TestAPIDryRunMutatingMethodsPrintPlanWithoutContact(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			args := []string{"api", method, "/monitors/aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1",
				"--dry-run", "-o", "json"}
			if method != "DELETE" {
				args = append(args, "-f", "name=x")
			}
			out, _, err := runResource(t, func(_ http.ResponseWriter, _ *http.Request) {
				t.Errorf("server must not be contacted under --dry-run (%s)", method)
			}, "", args...)
			if err != nil {
				t.Fatal(err)
			}
			var plan map[string]any
			if err := json.Unmarshal([]byte(out), &plan); err != nil {
				t.Fatalf("plan output not JSON: %v (%q)", err, out)
			}
			if plan["dry_run"] != true || plan["method"] != method {
				t.Fatalf("plan = %v", plan)
			}
			if method != "DELETE" {
				body, _ := plan["body"].(map[string]any)
				if body["name"] != "x" {
					t.Fatalf("plan body = %v", body)
				}
			}
		})
	}
}

// TestAPIDryRunReadsStillExecute mirrors TestDryRunReadsStillExecute for
// the escape hatch: GETs run normally under --dry-run.
func TestAPIDryRunReadsStillExecute(t *testing.T) {
	contacted := false
	out, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		contacted = true
		_, _ = w.Write([]byte(`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1"}`))
	}, "", "api", "GET", "/monitors/aaaaaaaa-aaaa-aaaa-aaaa-0000000000a1", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !contacted {
		t.Fatal("reads must still execute under --dry-run")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc["id"] == "" {
		t.Fatalf("out = %q err=%v", out, err)
	}
}
