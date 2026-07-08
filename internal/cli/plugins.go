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

	"github.com/svrnm/bronto-cli/internal/clierr"
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
// It returns nil (dispatch to a plugin does not apply) when: argv has no
// non-flag token to treat as a command name; that name is already a known
// top-level command (real subcommands, including ones cobra registers
// lazily like "help"/"completion", always win over a same-named plugin);
// the name fails pluginNamePattern; or no `bronto-<name>` executable is
// found on PATH. In all of those cases the caller proceeds with normal
// cobra execution, which produces whatever error (or success) it always
// would have.
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

// pluginDispatchArgs extracts the attempted top-level command name and its
// trailing args from the raw argv the process was invoked with, e.g.
// ["foo", "--bar", "baz"] -> ("foo", ["--bar", "baz"], true). Returns
// ok=false if argv has no non-flag element (nothing to dispatch on).
func pluginDispatchArgs(argv []string) (name string, rest []string, ok bool) {
	for i, a := range argv {
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a, argv[i+1:], true
	}
	return "", nil, false
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
