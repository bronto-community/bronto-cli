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

func TestDryRunDeleteSkipsConfirmationAndContact(t *testing.T) {
	// No --yes and no TTY: without --dry-run this would be a usage error;
	// with it, nothing is destructive so no confirmation is needed.
	_, stderr, err := runResource(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be contacted under --dry-run")
	}, "", "monitors", "delete", "m1", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "DRY RUN: would delete monitor m1.") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// TestDryRunReadsStillExecute pins that GETs run normally so list/get and
// dataset-name resolution keep working under --dry-run.
func TestDryRunReadsStillExecute(t *testing.T) {
	contacted := false
	out, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		contacted = true
		_, _ = w.Write([]byte(`[{"id":"m1"}]`))
	}, "", "monitors", "list", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !contacted {
		t.Fatal("reads must still execute under --dry-run")
	}
	if !strings.Contains(out, "m1") {
		t.Fatalf("out = %q", out)
	}
}

func TestDryRunMuteAndTestMessages(t *testing.T) {
	_, stderr, err := runResource(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be contacted under --dry-run")
	}, "", "monitors", "mute", "m1", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "DRY RUN: would set mute_until=-1 on monitor m1.") {
		t.Fatalf("mute stderr = %q", stderr)
	}

	_, stderr, err = runResource(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be contacted under --dry-run")
	}, "", "monitors", "test", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "DRY RUN: would send test notifications") {
		t.Fatalf("test stderr = %q", stderr)
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
