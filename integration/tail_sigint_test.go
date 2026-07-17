package integration

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTail_SIGINTCleanExit live-verifies the kill-signal contract the plan
// calls out for `tail`: Start launches `bronto tail` against the seeded
// dataset with a long lookback window, Stop sends SIGINT (never SIGKILL —
// see Start's doc comment) after the process has had a few seconds to
// settle into its poll loop, and the process must exit cleanly.
//
// "Cleanly" here means exit code 0, not the signal-aborted 130 exit
// cmd/bronto/main.go's exitStatus maps a canceled root context to:
// tail.go's poll loop explicitly returns nil (not ctx.Err()) when
// cmd.Context().Done() fires, specifically so an operator hitting Ctrl-C
// gets a clean exit rather than an error-shaped one. This test is what
// makes Start/Stop non-dead code: nothing else in this package used them
// before.
func TestTail_SIGINTCleanExit(t *testing.T) {
	key := skipIfNoCreds(t)
	dataset, _ := seededData(t)
	r := NewRunner(t, key)
	logID := logIDForDataset(t, r, dataset)

	cmd, stdout, stderr := r.Start(t, "tail", "-d", logID, "--window", "5m", "--interval", "2s")

	time.Sleep(3 * time.Second)

	if err := Stop(cmd, 10*time.Second); err != nil {
		t.Fatalf("tail did not exit cleanly after SIGINT: %v\nstdout: %s\nstderr: %s",
			err, stdout.String(), stderr.String())
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("tail exited %d after SIGINT, want 0 (clean exit)\nstdout: %s\nstderr: %s",
			code, stdout.String(), stderr.String())
	}

	// When the instrumented binary flushes coverage on this graceful exit
	// (the whole reason Stop uses SIGINT and never Kill), GOCOVERDIR should
	// gain at least one covcounters file. Only asserted when GOCOVERDIR is
	// actually set (e.g. under scripts/coverage.sh's integration leg) —
	// under a plain `go test ./integration/` this binary isn't instrumented
	// at all, so there's nothing to check.
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		matches, err := filepath.Glob(filepath.Join(dir, "covcounters.*"))
		if err != nil {
			t.Fatalf("globbing GOCOVERDIR %s for covcounters files: %v", dir, err)
		}
		if len(matches) == 0 {
			t.Errorf("GOCOVERDIR=%s is set but no covcounters.* files were found after tail exited", dir)
		}
	}
}
