package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
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

// -d <collection>/<dataset> splits like everywhere else; sent verbatim it
// created a dataset literally named "prod/app".
func TestSendSplitsCollectionFromDataset(t *testing.T) {
	plan := runSendDryRun(t, "", "-m", "x", "-d", "prod/app")
	if plan["dataset"] != "app" || plan["collection"] != "prod" {
		t.Fatalf("plan = %v", plan)
	}
}

func TestSendRejectsAmbiguousCollection(t *testing.T) {
	for _, args := range [][]string{
		{"-d", "prod/app", "--collection", "staging"},
		{"-d", "/app"},
		{"-d", "prod/"},
		{"-d", "a/b/c"},
	} {
		t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
		root := NewRootCmd()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetIn(strings.NewReader(""))
		root.SetArgs(append([]string{"send", "-m", "x", "--api-key", "k", "--dry-run"}, args...))
		err := root.Execute()
		if clierr.ExitCode(err) != 2 {
			t.Errorf("%v: err = %v, want a usage error", args, err)
		}
	}
}

// The plan shows every --tag, in both the one-shot and the stream shape,
// so a dry run reveals what lands in the x-bronto-tags header.
func TestSendDryRunShowsTags(t *testing.T) {
	for _, extra := range [][]string{
		{"-m", "x", "--tag", "env=prod", "--tag", "team=a"},
		{"--tag", "env=prod", "--tag", "team=a"},
	} {
		plan := runSendDryRun(t, "line\n", extra...)
		tags, _ := plan["tags"].([]any)
		if len(tags) != 2 || tags[0] != "env=prod" || tags[1] != "team=a" {
			t.Fatalf("%v: tags = %v", extra, plan["tags"])
		}
	}
	if plan := runSendDryRun(t, "", "-m", "x"); plan["tags"] != nil {
		t.Fatalf("no --tag must leave tags out: %v", plan["tags"])
	}
}

func TestSendRejectsEmptyTagKey(t *testing.T) {
	for _, tag := range []string{"=x", " =x", "novalue"} {
		_, _, err := runCLI(t, "send", "-d", "demo", "-m", "x", "--api-key", "k", "--dry-run", "--tag", tag)
		_ = wantCLIErr(t, err, "usage_invalid_tag", 2)
	}
}

// The dry run caps lines like the real stream (1 MiB), so it rejects
// exactly the input a real run would.
func TestSendDryRunRejectsOverlongLine(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader(strings.Repeat("a", maxSendLine+1) + "\n"))
	root.SetArgs([]string{"send", "-d", "demo", "--api-key", "k", "--dry-run", "-o", "json"})
	ce := wantCLIErr(t, root.Execute(), "ingest_read_error", 1)
	if strings.Contains(ce.Hint, "delivered") {
		t.Fatalf("a dry run delivered nothing: hint = %q", ce.Hint)
	}

	plan := runSendDryRun(t, strings.Repeat("a", maxSendLine-1)+"\n")
	if plan["events"] != 1.0 {
		t.Fatalf("a line under the cap must count: %v", plan)
	}
}
