package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// runSendDryRun runs `bronto send --dry-run` with the given stdin and args,
// returning the parsed plan document. No server is involved: the dry-run
// path must never touch the network.
func runSendDryRun(t *testing.T, stdin string, extra ...string) map[string]any {
	t.Helper()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader(stdin))
	args := append([]string{"send", "-d", "demo", "--api-key", "k", "--dry-run", "-o", "json"}, extra...)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var plan map[string]any
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("plan not JSON: %v (%q)", err, out.String())
	}
	return plan
}

func TestSendDryRunStreamCountsWithoutSending(t *testing.T) {
	plan := runSendDryRun(t, "{\"message\":\"a\"}\n\n{\"message\":\"b\"}\n")
	if plan["dry_run"] != true || plan["dataset"] != "demo" {
		t.Fatalf("plan = %v", plan)
	}
	if plan["events"] != 2.0 {
		t.Fatalf("events = %v, want 2 (blank lines skipped)", plan["events"])
	}
	if b, _ := plan["bytes"].(float64); b <= 0 {
		t.Fatalf("bytes = %v, want > 0", plan["bytes"])
	}
}

func TestSendDryRunOneShotIncludesEvent(t *testing.T) {
	plan := runSendDryRun(t, "", "-m", "hello world")
	if plan["dry_run"] != true {
		t.Fatalf("plan = %v", plan)
	}
	events, _ := plan["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("events = %v, want the single would-be event", plan["events"])
	}
	ev, _ := events[0].(map[string]any)
	if ev["message"] != "hello world" {
		t.Fatalf("event = %v", ev)
	}
}
