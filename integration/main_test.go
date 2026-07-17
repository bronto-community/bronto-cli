package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

// runMain resolves the binary under test and, when live credentials are
// available, runs the start-of-run sweeper before handing off to m.Run().
// It never skips the whole suite over a missing key: individual tests
// self-skip via skipIfNoCreds so `go test ./integration/` always compiles,
// runs, and reports (with visible skips) in ordinary CI.
func runMain(m *testing.M) int {
	if mgmtKey() == "" {
		fmt.Fprintln(os.Stderr, "integration: BRONTO_IT_MGMT_KEY not set; running with all live tests skipped")
	}

	bin, cleanup, err := resolveBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration: %v\n", err)
		return 1
	}
	defer cleanup()
	binPath = bin

	if mgmtKey() != "" {
		if err := sweepBeforeRun(bin, mgmtKey()); err != nil {
			// The sweeper is opportunistic best-effort cleanup, not the
			// thing under test: a failure here is worth logging loudly
			// but must not block the actual suite from running.
			fmt.Fprintf(os.Stderr, "integration: sweeper: %v\n", err)
		}
	}

	return m.Run()
}

// sweepBeforeRun runs the sweeper with its own bounded context and a
// throwaway hermetic config dir (there's no *testing.T yet to hang a
// t.TempDir() off).
func sweepBeforeRun(bin, key string) error {
	dir, err := os.MkdirTemp("", "bronto-it-sweep-")
	if err != nil {
		return fmt.Errorf("creating sweeper config dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return RunSweeper(ctx, newSweepRunner(bin, key, dir))
}

// resolveBinary honors BRONTO_IT_BIN (CI passes the instrumented build made
// by scripts/coverage.sh); otherwise it builds an instrumented binary once
// into a temp dir for local `go test ./integration/` runs. The returned
// cleanup removes that temp dir; it's a no-op when BRONTO_IT_BIN was
// honored, since this package doesn't own that binary.
func resolveBinary() (bin string, cleanup func(), err error) {
	if b := os.Getenv("BRONTO_IT_BIN"); b != "" {
		return b, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "bronto-it-bin-")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp dir for build: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	bin = filepath.Join(dir, "bronto")
	// -coverpkg=./... is a pattern resolved relative to the build command's
	// working directory: run from the module root (one level up from this
	// package) rather than leaving it defaulted to integration/, or the
	// pattern would only match this package instead of the whole module —
	// silently instrumenting nothing useful.
	cmd := exec.Command("go", "build", "-cover", "-covermode=atomic", "-coverpkg=./...", "-o", bin, "./cmd/bronto")
	cmd.Dir = ".."
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("building instrumented bronto binary: %w", err)
	}
	return bin, cleanup, nil
}
