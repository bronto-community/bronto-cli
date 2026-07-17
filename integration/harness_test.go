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
