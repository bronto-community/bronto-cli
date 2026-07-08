package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

// stubPlugin installs stub lookPath/runPlugin implementations for the
// duration of the test and restores the originals on cleanup.
func stubPlugin(t *testing.T, look func(string) (string, error),
	run func(string, []string, io.Reader, io.Writer, io.Writer) (int, error)) *stubRecorder {
	t.Helper()
	rec := &stubRecorder{}
	origLook, origRun := lookPath, runPlugin
	lookPath = func(name string) (string, error) {
		rec.lookedUp = append(rec.lookedUp, name)
		return look(name)
	}
	runPlugin = func(path string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
		rec.ranPath = path
		rec.ranArgs = args
		return run(path, args, stdin, stdout, stderr)
	}
	t.Cleanup(func() { lookPath, runPlugin = origLook, origRun })
	return rec
}

type stubRecorder struct {
	lookedUp []string
	ranPath  string
	ranArgs  []string
}

func TestExecuteDispatchesToPluginAndReturnsItsExitCode(t *testing.T) {
	rec := stubPlugin(t,
		func(name string) (string, error) {
			if name == "bronto-foo" {
				return "/usr/local/bin/bronto-foo", nil
			}
			return "", errors.New("not found")
		},
		func(path string, args []string, _ io.Reader, _, _ io.Writer) (int, error) {
			return 7, nil
		},
	)

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	argv := []string{"foo", "bar", "--baz"}
	root.SetArgs(argv)
	err := Execute(context.Background(), root, argv)

	var pe *PluginExit
	if !errors.As(err, &pe) {
		t.Fatalf("want *PluginExit, got %T: %v", err, err)
	}
	if pe.Code != 7 {
		t.Fatalf("PluginExit.Code = %d, want 7", pe.Code)
	}
	if rec.ranPath != "/usr/local/bin/bronto-foo" {
		t.Fatalf("ran path = %q", rec.ranPath)
	}
	if len(rec.ranArgs) != 2 || rec.ranArgs[0] != "bar" || rec.ranArgs[1] != "--baz" {
		t.Fatalf("ran args = %v, want [bar --baz]", rec.ranArgs)
	}
}

func TestExecuteUnknownCommandWithoutPluginStillUsageInvalidArgs(t *testing.T) {
	stubPlugin(t,
		func(string) (string, error) { return "", errors.New("not found") },
		func(string, []string, io.Reader, io.Writer, io.Writer) (int, error) { return 0, nil },
	)

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	argv := []string{"no-such-command"}
	root.SetArgs(argv)
	err := Execute(context.Background(), root, argv)

	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_invalid_args" {
		t.Fatalf("want usage_invalid_args clierr.Error, got %T: %v", err, err)
	}
	if got := clierr.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode = %d, want 2", got)
	}
}

func TestExecuteInvalidPluginNameNeverReachesLookPath(t *testing.T) {
	rec := stubPlugin(t,
		func(string) (string, error) { return "/should/not/be/used", nil },
		func(string, []string, io.Reader, io.Writer, io.Writer) (int, error) { return 0, nil },
	)

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	argv := []string{"../evil", "x"}
	root.SetArgs(argv)
	err := Execute(context.Background(), root, argv)

	if len(rec.lookedUp) != 0 {
		t.Fatalf("lookPath must not be called for invalid names, got calls: %v", rec.lookedUp)
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_invalid_args" {
		t.Fatalf("want usage_invalid_args clierr.Error, got %T: %v", err, err)
	}
}

func TestExecuteRunPluginFailureSurfacesAsPluginExecError(t *testing.T) {
	stubPlugin(t,
		func(string) (string, error) { return "/usr/local/bin/bronto-foo", nil },
		func(string, []string, io.Reader, io.Writer, io.Writer) (int, error) {
			return 0, errors.New("exec format error")
		},
	)

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	argv := []string{"foo"}
	root.SetArgs(argv)
	err := Execute(context.Background(), root, argv)

	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "plugin_exec_error" {
		t.Fatalf("want plugin_exec_error clierr.Error, got %T: %v", err, err)
	}
}

func TestPluginDispatchArgsFindsFirstNonFlagToken(t *testing.T) {
	name, rest, ok := pluginDispatchArgs([]string{"foo", "--bar", "baz"})
	if !ok || name != "foo" || len(rest) != 2 || rest[0] != "--bar" || rest[1] != "baz" {
		t.Fatalf("got (%q, %v, %v)", name, rest, ok)
	}
	if _, _, ok := pluginDispatchArgs(nil); ok {
		t.Fatal("empty argv should report ok=false")
	}
	if _, _, ok := pluginDispatchArgs([]string{"--only-flags"}); ok {
		t.Fatal("all-flags argv should report ok=false")
	}
}

// TestPluginDispatchSkipsFlagValues pins the first-token-only dispatch
// contract: a plugin name must be argv[0] itself. "--region" starts with a
// flag, so this never even reaches lookPath, regardless of what follows.
func TestPluginDispatchSkipsFlagValues(t *testing.T) {
	oldLook := lookPath
	var looked []string
	lookPath = func(name string) (string, error) { looked = append(looked, name); return "", exec.ErrNotFound }
	t.Cleanup(func() { lookPath = oldLook })

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	argv := []string{"--region", "us", "search", "x", "--api-key", "k", "--base-url", "http://127.0.0.1:9"}
	root.SetArgs(argv)
	_ = Execute(context.Background(), root, argv)
	for _, n := range looked {
		if n == "bronto-us" {
			t.Fatalf("flag value dispatched as plugin: %v", looked)
		}
	}
	if len(looked) != 0 {
		t.Fatalf("no plugin lookup should happen when argv[0] is a flag, got: %v", looked)
	}
}

// TestPluginDispatchRequiresFirstToken pins the kubectl/gh-style contract:
// dispatch ONLY happens when the plugin name is argv[0]. A global flag
// preceding it (e.g. "--region us myplug arg") means dispatch does NOT
// apply, even though "myplug" is not a real bronto command — the plugin
// lookup never even happens, and execution falls through to cobra's normal
// (usage) error handling for the unknown "--region"-led invocation.
func TestPluginDispatchRequiresFirstToken(t *testing.T) {
	rec := stubPlugin(t,
		func(name string) (string, error) {
			if name == "bronto-myplug" {
				return "/usr/local/bin/bronto-myplug", nil
			}
			return "", errors.New("not found")
		},
		func(path string, args []string, _ io.Reader, _, _ io.Writer) (int, error) {
			return 3, nil
		},
	)

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	argv := []string{"--region", "us", "myplug", "arg"}
	root.SetArgs(argv)
	err := Execute(context.Background(), root, argv)

	var pe *PluginExit
	if errors.As(err, &pe) {
		t.Fatalf("want no plugin dispatch when a flag precedes the name, got *PluginExit{%d}", pe.Code)
	}
	if len(rec.lookedUp) != 0 {
		t.Fatalf("lookPath must not be called, got: %v", rec.lookedUp)
	}
}

func TestPluginNamePatternRejectsTraversalAndFlags(t *testing.T) {
	for _, bad := range []string{"../evil", "-x", "Foo", "foo/bar", ""} {
		if pluginNamePattern.MatchString(bad) {
			t.Errorf("pattern accepted invalid name %q", bad)
		}
	}
	for _, good := range []string{"foo", "foo-bar", "f", "9lives"} {
		if !pluginNamePattern.MatchString(good) {
			t.Errorf("pattern rejected valid name %q", good)
		}
	}
}

func TestPluginsListFindsExecutableOnPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix exec-bit semantics assumed")
	}
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	dir := t.TempDir()
	script := filepath.Join(dir, "bronto-hello")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A non-executable bronto-* file must be skipped.
	if err := os.WriteFile(filepath.Join(dir, "bronto-noexec"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"plugins", "list", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("plugins list: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, out.String())
	}
	if len(rows) != 1 || rows[0]["name"] != "hello" || rows[0]["path"] != script {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestDiscoverPluginsFirstMatchOnPATHWins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix exec-bit semantics assumed")
	}
	dir1, dir2 := t.TempDir(), t.TempDir()
	for _, dir := range []string{dir1, dir2} {
		if err := os.WriteFile(filepath.Join(dir, "bronto-dup"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rows := discoverPlugins(dir1 + string(os.PathListSeparator) + dir2)
	if len(rows) != 1 || rows[0]["path"] != filepath.Join(dir1, "bronto-dup") {
		t.Fatalf("rows = %+v", rows)
	}
}
