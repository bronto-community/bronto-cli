package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// pluginNamePattern restricts the command names that are ever looked up as
// exec plugins. It MUST be checked before any name reaches lookPath: without
// it, a crafted "command" like "../evil" or "-x" could turn a PATH lookup
// into a path-traversal or flag-injection primitive.
var pluginNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// lookPath resolves a plugin executable on PATH. Package-level so tests can
// stub it instead of touching the real filesystem/PATH.
var lookPath = exec.LookPath

// runPlugin executes the plugin at path with args, wiring stdio through,
// and reports its exit code. Package-level so tests can stub it. A non-nil
// error means the plugin could not be run at all (as opposed to running and
// exiting non-zero, which is reported via the returned code with a nil
// error).
var runPlugin = defaultRunPlugin

func defaultRunPlugin(path string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	c := exec.Command(path, args...)
	c.Stdin = stdin
	c.Stdout = stdout
	c.Stderr = stderr
	err := c.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// A signal-killed plugin (e.g. SIGKILL/SIGSEGV) has no waitable exit
		// code: ExitCode() returns -1, which the caller (main) turns into
		// process exit 255. That's the same behavior kubectl exhibits for
		// killed plugins, so it's left as-is rather than special-cased.
		return exitErr.ExitCode(), nil
	}
	return 0, err
}

// PluginExit carries an exec plugin's own exit code through Execute's error
// return so main can exit with it verbatim: plugins own their exit codes
// and their own stdout/stderr, so this must NOT be rendered as a clierr
// error.
type PluginExit struct{ Code int }

func (p *PluginExit) Error() string { return fmt.Sprintf("plugin exited with status %d", p.Code) }

// reservedTopLevelNames are command names cobra may serve at the top level
// that are NOT necessarily present in rootCmd.Commands() yet at the point
// tryPluginDispatch runs (cobra lazily registers "help" and the completion
// commands inside ExecuteC, which we intentionally run before). They must
// never be shadowed by a same-named exec plugin.
var reservedTopLevelNames = map[string]bool{
	"help":             true,
	"completion":       true,
	"__complete":       true,
	"__completeNoDesc": true,
}

// tryPluginDispatch implements kubectl/gh-style exec plugin dispatch. It
// runs BEFORE the command tree parses argv at all — deliberately, so that
// flags meant for the plugin (which cobra's root command knows nothing
// about) never reach cobra's flag parser and blow up as "unknown flag"
// instead of giving the plugin a chance to run. See Execute (root.go).
//
// The dispatch contract is deliberately narrow, matching kubectl/gh: a
// plugin is only ever considered when its name is argv[0] — the very
// first token, with no flags (global or otherwise) preceding it. This is
// a design choice, not an oversight: it means `bronto --profile prod
// myplug` does NOT dispatch to bronto-myplug (global flags before the
// plugin name are silently invisible to any exec plugin, since plugins
// don't share bronto's flag parser), but it also means dispatch detection
// requires no knowledge of cobra's flag arity/parsing rules at all — argv[0]
// either names a plugin or it doesn't. Users who want global flags to
// apply put them after the plugin name, where the plugin itself is free
// to interpret them (or not).
//
// It returns nil (dispatch to a plugin does not apply) when: argv is empty;
// argv[0] starts with "-" (i.e. is itself a flag); argv[0] is a reserved or
// already-registered top-level command name; argv[0] fails
// pluginNamePattern; or no `bronto-<name>` executable is found on PATH. In
// all of those cases the caller proceeds with normal cobra execution,
// which produces whatever error (or success) it always would have.
func tryPluginDispatch(rootCmd *cobra.Command, argv []string) error {
	name, rest, ok := pluginDispatchArgs(argv)
	if !ok || isKnownTopLevelCommand(rootCmd, name) || !pluginNamePattern.MatchString(name) {
		return nil
	}
	path, lerr := lookPath("bronto-" + name)
	if lerr != nil {
		return nil
	}
	code, rerr := runPlugin(path, rest, os.Stdin, os.Stdout, os.Stderr)
	if rerr != nil {
		return clierr.New("plugin_exec_error", fmt.Sprintf("failed to run plugin %q: %v", path, rerr))
	}
	return &PluginExit{Code: code}
}

func isKnownTopLevelCommand(rootCmd *cobra.Command, name string) bool {
	if reservedTopLevelNames[name] {
		return true
	}
	for _, c := range rootCmd.Commands() {
		if c.Name() == name {
			return true
		}
		for _, alias := range c.Aliases {
			if alias == name {
				return true
			}
		}
	}
	return false
}

// pluginDispatchArgs extracts a candidate plugin name and its trailing args
// from argv, e.g. ["foo", "--bar", "baz"] -> ("foo", ["--bar", "baz"], true).
// Per the first-token-only contract (see tryPluginDispatch), the candidate
// is ONLY argv[0], and only when it isn't itself a flag: no scanning past
// it, no flag-arity awareness needed. Returns ok=false when argv is empty
// or argv[0] starts with "-".
func pluginDispatchArgs(argv []string) (name string, rest []string, ok bool) {
	if len(argv) == 0 || argv[0] == "" || strings.HasPrefix(argv[0], "-") {
		return "", nil, false
	}
	return argv[0], argv[1:], true
}

func newPluginsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugins",
		Short: "Discover exec plugins (bronto-* executables on PATH)",
	}
	cmd.AddCommand(newPluginsListCmd())
	return cmd
}

func newPluginsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List bronto-* executables found on PATH",
		Example: "  bronto plugins list\n" +
			"  bronto plugins list -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows([]string{"name", "path"}, discoverPlugins(os.Getenv("PATH")))
		},
	}
}

// discoverPlugins scans the directories in pathEnv (a PATH-style, OS list
// separator-delimited string) for executable files named "bronto-*",
// mirroring shell PATH-lookup semantics: the first match for a given
// plugin name wins, later duplicates on PATH are ignored.
func discoverPlugins(pathEnv string) []map[string]any {
	seen := map[string]bool{}
	var rows []map[string]any
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), "bronto-") {
				continue
			}
			name := strings.TrimPrefix(e.Name(), "bronto-")
			if name == "" || seen[name] {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Mode()&0o111 == 0 {
				continue
			}
			seen[name] = true
			rows = append(rows, map[string]any{"name": name, "path": filepath.Join(dir, e.Name())})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["name"].(string) < rows[j]["name"].(string) })
	return rows
}
