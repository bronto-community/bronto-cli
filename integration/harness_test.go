package integration

import (
	"os/exec"
	"testing"
	"time"
)

// TestConfigureCancel verifies that configureCancel sets both Cancel and
// WaitDelay. This lived in harness.go (a non-_test.go file) until this
// test, where `go test` never compiled or ran it at all — moving it here
// is what makes it actually execute.
func TestConfigureCancel(t *testing.T) {
	cmd := exec.Command("true")
	if cmd.Cancel != nil {
		t.Errorf("before configureCancel: Cancel already set")
	}
	if cmd.WaitDelay != 0 {
		t.Errorf("before configureCancel: WaitDelay already set")
	}
	configureCancel(cmd)
	if cmd.Cancel == nil {
		t.Errorf("after configureCancel: Cancel is nil")
	}
	if cmd.WaitDelay != 10*time.Second {
		t.Errorf("after configureCancel: WaitDelay = %v, want 10s", cmd.WaitDelay)
	}
}

// TestBackoffIntervalGrowsAndClamps pins the poll schedule shared by
// PollUntil and pollSeedVisible: each unsuccessful check waits pollBackoff×
// longer, up to the ceiling. Hermetic — no credentials, no live account.
func TestBackoffIntervalGrowsAndClamps(t *testing.T) {
	got := []time.Duration{2 * time.Second}
	for i := 0; i < 6; i++ {
		got = append(got, backoffInterval(got[len(got)-1]))
	}
	want := []time.Duration{
		2 * time.Second,
		3 * time.Second,
		4500 * time.Millisecond,
		6750 * time.Millisecond,
		10125 * time.Millisecond,
		15187500 * time.Microsecond,
		20 * time.Second,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("interval[%d] = %s, want %s (full schedule: %v)", i, got[i], want[i], got)
		}
	}
	if got := backoffInterval(time.Minute); got != 20*time.Second {
		t.Errorf("backoffInterval(1m) = %s, want the 20s ceiling", got)
	}
}

// TestSeedPollRequestBudget is the load guard: over the readiness poll's
// full 3-minute window, the backing-off schedule must cost a handful of
// ticks, not one every few seconds. Each tick is a live search (plus a
// `datasets list` until the log_id resolves) against the shared test
// account, which is what this whole change is about.
func TestSeedPollRequestBudget(t *testing.T) {
	ticks, elapsed := 0, time.Duration(0)
	for interval := seedPollInterval; elapsed < 3*time.Minute; interval = backoffInterval(interval) {
		ticks++
		elapsed += interval
	}
	if ticks > 15 {
		t.Fatalf("a full 3m readiness poll costs %d ticks, want <= 15", ticks)
	}
	if ticks < 5 {
		t.Fatalf("a full 3m readiness poll costs only %d ticks — too coarse to catch a fast propagation early", ticks)
	}
}
