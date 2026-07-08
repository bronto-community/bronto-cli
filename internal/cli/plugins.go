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
	"github.com/spf13/pflag"

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
// It first calls cobra's own exported cmd.Find, which walks the command
// tree the same way cobra's real dispatch does: flag-arity-aware (a flag
// like --region consumes its value token and that value is never mistaken
// for a command name) and without invoking ParseFlags (so plugin-only
// flags can't blow up parsing here). If Find resolves argv to any command
// other than rootCmd itself, a REAL subcommand matched (e.g. "search" in
// "--region us search foo") and this is never plugin territory — even
// though "us" precedes "search" and isn't itself a valid token, Find's
// traversal already proved "search" is the intended command, so no
// plugin lookup happens at all, for "us" or otherwise.
//
// Only when Find finds no real subcommand (found == rootCmd) do we go on
// to extract a plugin-name candidate ourselves (pluginDispatchArgs), since
// Find does not hand back a flag-stripped arg list in that case (it
// returns argv unchanged) — see pluginDispatchArgs for why that scan is
// still needed and how it stays flag-arity-aware.
//
// It returns nil (dispatch to a plugin does not apply) when: Find errors
// (e.g. an ambiguous parse); Find resolves to a real subcommand; argv has
// no non-flag token left to treat as a command name; that name is a
// reserved top-level name (cobra registers "help"/"completion" lazily, so
// they may not appear in rootCmd.Commands() yet here); the name fails
// pluginNamePattern; or no `bronto-<name>` executable is found on PATH. In
// all of those cases the caller proceeds with normal cobra execution,
// which produces whatever error (or success) it always would have.
func tryPluginDispatch(rootCmd *cobra.Command, argv []string) error {
	found, _, ferr := rootCmd.Find(argv)
	if ferr != nil || found != rootCmd {
		return nil
	}
	name, rest, ok := pluginDispatchArgs(rootCmd, argv)
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
//
// Only called once cmd.Find (in tryPluginDispatch) has already established
// argv doesn't resolve to a real subcommand, i.e. cmd is rootCmd itself.
// cmd.Find does not return a flag-stripped arg list for that case (it
// hands back argv unchanged), so this reimplements cobra's own flag-arity
// rule directly against cmd's flags (already merged with persistent flags
// as a side effect of the Find call): a flag token consumes the next argv
// element as its value UNLESS the flag has a NoOptDefVal (e.g. bool
// flags, which can appear bare). This is what keeps a flag's VALUE (e.g.
// "us" in "--region us") from ever being mistaken for the command name.
func pluginDispatchArgs(cmd *cobra.Command, argv []string) (name string, rest []string, ok bool) {
	flags := cmd.Flags()
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--":
			return "", nil, false
		case strings.HasPrefix(a, "--") && !strings.Contains(a, "=") && !flagHasNoOptDefVal(flags, a[2:]):
			i++ // a's value is the next token; skip both
		case strings.HasPrefix(a, "-") && len(a) == 2 && !strings.Contains(a, "=") && !flagShorthandHasNoOptDefVal(flags, a[1:]):
			i++
		case a != "" && !strings.HasPrefix(a, "-"):
			return a, argv[i+1:], true
		}
	}
	return "", nil, false
}

// flagHasNoOptDefVal and flagShorthandHasNoOptDefVal mirror cobra's own
// (unexported) hasNoOptDefVal/shortHasNoOptDefVal helpers, using pflag's
// exported Flag.NoOptDefVal to answer: can this flag appear on the command
// line without a following value token?
func flagHasNoOptDefVal(flags *pflag.FlagSet, name string) bool {
	f := flags.Lookup(name)
	return f != nil && f.NoOptDefVal != ""
}

func flagShorthandHasNoOptDefVal(flags *pflag.FlagSet, name string) bool {
	if len(name) == 0 {
		return false
	}
	f := flags.ShorthandLookup(name[:1])
	return f != nil && f.NoOptDefVal != ""
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
