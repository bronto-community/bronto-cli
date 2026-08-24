package cli

import (
	"testing"
	"time"
)

// TestNextExportPollIntervalGrowsAndClamps pins the --wait backoff schedule:
// each non-terminal poll waits exportPollBackoff× longer than the last, up to
// exportPollMaxInterval. The clamp is what bounds a long export's request
// count (an 8-minute export polls ~20 times, not ~160 at a fixed 3s).
func TestNextExportPollIntervalGrowsAndClamps(t *testing.T) {
	oldInterval, oldMax := exportPollInterval, exportPollMaxInterval
	exportPollInterval, exportPollMaxInterval = 2*time.Second, 30*time.Second
	t.Cleanup(func() { exportPollInterval, exportPollMaxInterval = oldInterval, oldMax })

	got := []time.Duration{exportPollInterval}
	for i := 0; i < 8; i++ {
		got = append(got, nextExportPollInterval(got[len(got)-1]))
	}
	want := []time.Duration{
		2 * time.Second,
		3 * time.Second,
		4500 * time.Millisecond,
		6750 * time.Millisecond,
		10125 * time.Millisecond,
		15187500 * time.Microsecond,
		22781250 * time.Microsecond,
		30 * time.Second,
		30 * time.Second,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("interval[%d] = %s, want %s (full schedule: %v)", i, got[i], want[i], got)
		}
	}
}

// TestNextExportPollIntervalClampsAnInitialIntervalAboveTheCap covers the
// configuration where the first interval already exceeds the cap (a caller
// or test raising exportPollInterval alone): the backoff must clamp down
// rather than keep growing past the cap.
func TestNextExportPollIntervalClampsAnInitialIntervalAboveTheCap(t *testing.T) {
	oldMax := exportPollMaxInterval
	exportPollMaxInterval = time.Second
	t.Cleanup(func() { exportPollMaxInterval = oldMax })

	if got := nextExportPollInterval(10 * time.Second); got != time.Second {
		t.Fatalf("nextExportPollInterval(10s) with a 1s cap = %s, want 1s", got)
	}
}

// TestExportPollBudgetForATypicalLongExport is the load guard this change
// exists for: the number of GET /exports/{id} requests a long-running export
// costs must stay far below the fixed-cadence count. Integration CI's export
// leg runs ~8 minutes; at the old fixed 3s that was ~160 requests against the
// test account, which dominated the suite's whole request budget.
func TestExportPollBudgetForATypicalLongExport(t *testing.T) {
	oldInterval, oldMax := exportPollInterval, exportPollMaxInterval
	exportPollInterval, exportPollMaxInterval = 2*time.Second, 30*time.Second
	t.Cleanup(func() { exportPollInterval, exportPollMaxInterval = oldInterval, oldMax })

	const exportDuration = 8 * time.Minute
	polls, elapsed := 0, time.Duration(0)
	for interval := exportPollInterval; elapsed < exportDuration; interval = nextExportPollInterval(interval) {
		polls++
		elapsed += interval
	}
	if polls > 25 {
		t.Fatalf("an %s export costs %d polls, want <= 25", exportDuration, polls)
	}
	if polls < 5 {
		t.Fatalf("an %s export costs only %d polls — the backoff is too coarse to report progress", exportDuration, polls)
	}
}
