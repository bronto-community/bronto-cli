// Package integration black-box tests the built bronto binary via
// os/exec: real process, real exit codes, real signal handling — named
// coverage gaps like tail's SIGINT path (tail_sigint_test.go) and main()'s
// own exit mapping (cmd/bronto/main.go's exitStatus) live only there and
// can't be reached by in-process unit tests. Plugin exec is NOT one of
// these, despite living in the same "black box" family: it's already
// covered in-process by internal/cli/plugins_test.go (defaultRunPlugin,
// tryPluginDispatch), so nothing here duplicates it.
//
// Every test in this package is gated at runtime, not at compile time: there
// is no //go:build tag, so the package always compiles and lints in any
// `go test ./...` / CI run. Tests that need a live Bronto account call
// skipIfNoCreds(t), which t.Skip()s visibly when BRONTO_IT_MGMT_KEY is
// unset. A handful of tests (see smoke_test.go) are hermetic — binary
// builds, --help exits 0 — and always run, so the package still exercises
// itself in plain, credential-less CI.
package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// binPath is resolved once by TestMain (main_test.go) before any test runs:
// either BRONTO_IT_BIN verbatim, or a freshly built instrumented binary.
var binPath string

// Runner executes the built bronto binary as a black-box subprocess with a
// hermetic, per-Runner environment: BRONTO_CONFIG_DIR never touches the
// developer's real config/keychain.
type Runner struct {
	Bin       string
	ConfigDir string
	APIKey    string
	Region    string
}

// hermeticNoKeySentinel replaces an empty apiKey in NewRunner. An actually-
// empty BRONTO_API_KEY is indistinguishable, to internal/config/config.go's
// resolution (its `set` helper skips empty values entirely), from "no env
// override" — which would leave api_key unresolved and let NewApp's
// keychain fallback (internal/cli/context.go) resolve whatever key is
// stored in the developer's real OS keychain, defeating this harness's
// hermetic isolation. A syntactically-plausible-but-bogus, non-empty key
// closes that fallback off while still failing predictably
// (auth_invalid_key) against any live endpoint it happens to reach.
const hermeticNoKeySentinel = "bronto-it-hermetic-no-key"

// NewRunner returns a Runner wired for t: a hermetic BRONTO_CONFIG_DIR
// rooted in t.TempDir(), apiKey as BRONTO_API_KEY (or hermeticNoKeySentinel
// when apiKey is empty), and BRONTO_REGION from BRONTO_IT_REGION (default
// "eu").
func NewRunner(t *testing.T, apiKey string) *Runner {
	t.Helper()
	if binPath == "" {
		t.Fatal("integration: binary path not resolved (TestMain should have set it before m.Run())")
	}
	if apiKey == "" {
		apiKey = hermeticNoKeySentinel
	}
	return &Runner{
		Bin:       binPath,
		ConfigDir: t.TempDir(),
		APIKey:    apiKey,
		Region:    regionOrDefault(),
	}
}

// newSweepRunner builds a Runner for the start-of-run sweeper (main_test.go),
// which runs in TestMain before any *testing.T exists.
func newSweepRunner(bin, apiKey, configDir string) *Runner {
	return &Runner{Bin: bin, ConfigDir: configDir, APIKey: apiKey, Region: regionOrDefault()}
}

func regionOrDefault() string {
	if r := os.Getenv("BRONTO_IT_REGION"); r != "" {
		return r
	}
	return "eu"
}

// Result is one subprocess invocation's captured output.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Run executes the binary with args, optionally feeding stdin, and returns
// its captured stdout/stderr and exit code. A non-zero exit via
// *exec.ExitError is NOT itself a Go error — callers assert on ExitCode
// explicitly, since a non-zero exit is an expected, documented outcome for
// plenty of these tests (auth-negative, delete-without---yes, ...). err is
// non-nil only for a failure to even run the process (binary missing,
// context canceled before start, etc).
func (r *Runner) Run(ctx context.Context, stdin string, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	cmd.Env = r.env()
	configureCancel(cmd)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return res, err
	}
	return res, nil
}

// configureCancel sets cmd.Cancel to send SIGINT instead of the stdlib
// default Kill, and sets cmd.WaitDelay to allow graceful shutdown.
// This ensures coverage counters flush on context cancellation.
func configureCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
}

// Start launches the binary in the background (e.g. a long-running command
// like tail) and returns the running *exec.Cmd plus its stdout/stderr
// buffers. Callers MUST stop it with Stop (SIGINT) — never
// cmd.Process.Kill: an instrumented binary's GOCOVERDIR counters only flush
// on a graceful exit, and SIGKILL silently discards that test's coverage
// contribution.
func (r *Runner) Start(t *testing.T, args ...string) (cmd *exec.Cmd, stdout, stderr *bytes.Buffer) {
	t.Helper()
	cmd = exec.Command(r.Bin, args...)
	cmd.Env = r.env()
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s %v: %v", r.Bin, args, err)
	}
	return cmd, stdout, stderr
}

// Stop sends SIGINT to cmd and waits (bounded by timeout) for it to exit.
// Never SIGKILL here: see Start's doc comment.
func Stop(cmd *exec.Cmd, timeout time.Duration) error {
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return errors.New("process did not exit within timeout after SIGINT")
	}
}

// env builds the subprocess environment: the ambient environment (PATH,
// HOME, GOCOVERDIR, ...) so instrumented binaries can run and flush
// coverage at all, but with every ambient BRONTO_* variable stripped except
// BRONTO_IT_* (this harness's own namespace, never read by the CLI itself)
// — a developer's shell might have BRONTO_PROFILE/BRONTO_BASE_URL/etc set
// for their own everyday use of the CLI, and letting any of those leak into
// a "hermetic" subprocess would silently change its behavior out from
// under the test. BRONTO_CONFIG_DIR/BRONTO_API_KEY/BRONTO_REGION are then
// (re)injected from the Runner, always winning.
func (r *Runner) env() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "BRONTO_") && !strings.HasPrefix(key, "BRONTO_IT_") {
			continue // stripped: only BRONTO_IT_* passes through unmodified
		}
		env = append(env, kv)
	}
	return append(env,
		"BRONTO_CONFIG_DIR="+r.ConfigDir,
		"BRONTO_API_KEY="+r.APIKey,
		"BRONTO_REGION="+r.Region,
	)
}

// mgmtKey returns BRONTO_IT_MGMT_KEY.
func mgmtKey() string { return os.Getenv("BRONTO_IT_MGMT_KEY") }

// skipIfNoCreds skips t (visibly, via t.Skip) when BRONTO_IT_MGMT_KEY isn't
// set, and returns the key otherwise. Every test that needs a live Bronto
// account must call this first.
func skipIfNoCreds(t *testing.T) string {
	t.Helper()
	key := mgmtKey()
	if key == "" {
		t.Skip("BRONTO_IT_MGMT_KEY not set; skipping live integration test")
	}
	return key
}

var (
	runIDOnce sync.Once
	runIDVal  string
)

// runID returns a short, stable identifier for the current run:
// GITHUB_RUN_ID when set (so every resource created by one CI run shares a
// namespace), else 8 random hex characters. Computed once per test binary
// so every resource created by this run shares the same id.
func runID() string {
	runIDOnce.Do(func() {
		if id := os.Getenv("GITHUB_RUN_ID"); id != "" {
			runIDVal = id
			return
		}
		b := make([]byte, 4)
		if _, err := rand.Read(b); err != nil {
			runIDVal = "local"
			return
		}
		runIDVal = hex.EncodeToString(b)
	})
	return runIDVal
}

// resourceName returns a name for a throwaway CI-created resource:
// bronto-ci-<unixts>-<runid>-<suffix>. The sweeper (sweeper.go) matches the
// bronto-ci-<unixts>- prefix and deletes stale ones older than an hour.
func resourceName(suffix string) string {
	return "bronto-ci-" + strconv.FormatInt(time.Now().Unix(), 10) + "-" + runID() + "-" + suffix
}

// PollUntil calls check every interval until it returns (true, nil) or
// timeout elapses, in which case it fails t (fail-hard, no retries/flaky
// markers — per the plan, a timeout means something is actually wrong).
// Context-aware: also stops early if t's own context (t.Context()) is done.
func PollUntil(t *testing.T, timeout, interval time.Duration, check func() (bool, error)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastErr error
	for {
		ok, err := check()
		if err != nil {
			lastErr = err
		} else if ok {
			return
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				t.Fatalf("PollUntil: timed out after %s: %v", timeout, lastErr)
			}
			t.Fatalf("PollUntil: timed out after %s waiting for condition", timeout)
			return
		case <-ticker.C:
		}
	}
}

// ErrorEnvelope mirrors the machine-readable error envelope clierr.Render
// writes to stderr whenever stderr is not a terminal — which, for an
// os/exec-captured subprocess, is always: rendering mode depends on
// whether stderr itself is a TTY, not on the command's own -o/--output
// flag (see cmd/bronto/main.go's exitStatus/machine detection).
type ErrorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
}

// ParseErrorEnvelope parses a subprocess's stderr as the CLI's machine
// error envelope, failing t if it doesn't parse.
func ParseErrorEnvelope(t *testing.T, stderr string) ErrorEnvelope {
	t.Helper()
	var env ErrorEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr)), &env); err != nil {
		t.Fatalf("stderr is not a JSON error envelope: %v\nstderr: %s", err, stderr)
	}
	return env
}
