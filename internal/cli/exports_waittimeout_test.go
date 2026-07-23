package cli

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// withFastExportWait shrinks both the poll cadence and the overall wait cap
// so timeout tests neither sit on a real clock nor hang the suite.
func withFastExportWait(t *testing.T, cap time.Duration) {
	t.Helper()
	oldInterval, oldCap := exportPollInterval, exportWaitTimeout
	exportPollInterval = time.Millisecond
	exportWaitTimeout = cap
	t.Cleanup(func() { exportPollInterval, exportWaitTimeout = oldInterval, oldCap })
}

// runWithHardDeadline runs fn and fails the test if it does not return
// within limit — so a regression that removes the poll cap surfaces as a
// clean failure here instead of hanging the whole package until the go
// test alarm fires (which is exactly how this bug hit CI on PR #63).
func runWithHardDeadline(t *testing.T, limit time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("waitForExport did not return within %s — the poll loop is unbounded", limit)
	}
}

// TestExportsCreateWaitTimesOutWhenNeverTerminal is the core guard: an
// export that never reaches COMPLETE/FAILED (stuck IN_PROGRESS, or any
// status the CLI doesn't recognize as terminal) must make --wait give up
// with a typed error, not poll forever. Without the cap this test hangs
// until the go test alarm; runWithHardDeadline turns that into a failure.
func TestExportsCreateWaitTimesOutWhenNeverTerminal(t *testing.T) {
	withFastExportWait(t, 30*time.Millisecond)
	var polls int32
	runWithHardDeadline(t, 10*time.Second, func() {
		_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/exports":
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"export_id":"exp-1","status":"CREATED"}`))
			case r.Method == http.MethodGet && r.URL.Path == "/exports/exp-1":
				atomic.AddInt32(&polls, 1)
				_, _ = w.Write([]byte(`{"export_id":"exp-1","status":"IN_PROGRESS"}`))
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}, "", "exports", "create", "--dataset", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "--since", "1h", "--wait")
		if err == nil {
			t.Error("expected a timeout error, got nil")
			return
		}
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != "export_wait_timeout" {
			t.Errorf("want export_wait_timeout, got %v", err)
			return
		}
		if clierr.ExitCode(err) != 1 {
			t.Errorf("exit code = %d, want 1", clierr.ExitCode(err))
		}
	})
	if atomic.LoadInt32(&polls) < 1 {
		t.Fatal("expected at least one poll before timing out")
	}
}

// TestExportsCreateWaitTimeoutFlagOverrides pins that --wait-timeout is
// wired through: a caller can bound the wait explicitly, independent of
// the package default.
func TestExportsCreateWaitTimeoutFlagOverrides(t *testing.T) {
	oldInterval := exportPollInterval
	exportPollInterval = time.Millisecond
	t.Cleanup(func() { exportPollInterval = oldInterval })

	runWithHardDeadline(t, 10*time.Second, func() {
		_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/exports":
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"export_id":"exp-1","status":"CREATED"}`))
			case r.Method == http.MethodGet && r.URL.Path == "/exports/exp-1":
				_, _ = w.Write([]byte(`{"export_id":"exp-1","status":"IN_PROGRESS"}`))
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}, "", "exports", "create", "--dataset", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			"--since", "1h", "--wait", "--wait-timeout", "25ms")
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != "export_wait_timeout" {
			t.Errorf("want export_wait_timeout from --wait-timeout, got %v", err)
		}
	})
}
