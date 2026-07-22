// Package cli contains the Cobra command tree.
package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/version"
)

func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bronto",
		Short: "CLI for the Bronto observability platform",
		Long: "bronto is a command-line client for the Bronto observability platform.\nDocs: https://docs.bronto.io\n\n" +
			"plugins: an executable named bronto-<name> on PATH is invoked when <name> is the first argument.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.String(),
		Run:           func(cmd *cobra.Command, _ []string) { _ = cmd.Help() },
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.SuggestionsMinimumDistance = 2
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return clierr.New("usage_invalid_flag", err.Error()).
			WithHint("Run 'bronto --help' for usage.")
	})

	pf := cmd.PersistentFlags()
	pf.String("api-key", "", "Bronto management API key (prefer BRONTO_API_KEY env)")
	pf.String("profile", "", "named profile to use")
	pf.String("region", "", "Bronto region: eu or us")
	pf.String("base-url", "", "override the API base URL")
	pf.StringP("output", "o", "", "output format: table|json|jsonl|raw|csv")
	pf.Bool("no-color", false, "disable color output")
	pf.Bool("quiet", false, "suppress non-data messages on stderr")
	pf.String("jq", "", "jq expression applied to json/jsonl output (each result prints on its own line); "+
		"values that fail the expression are skipped")
	pf.StringSlice("fields", nil, "select specific fields (comma-separated); use '?' to list available field names")
	pf.Int("timeout", 0, "HTTP timeout in seconds (config: timeout, env: BRONTO_TIMEOUT)")
	pf.Int("max-retries", 2, "retries for idempotent requests on 429/5xx (config: max_retries, env: BRONTO_MAX_RETRIES)")
	pf.Bool("debug", false, "trace API requests/responses on stderr (API key redacted)")
	pf.Bool("dry-run", false, "print mutating API calls instead of executing them (reads still run)")
	cmd.AddCommand(newVersionCmd())
	cmd.AddCommand(newConfigCmd())
	cmd.AddCommand(newAuthCmd())
	cmd.AddCommand(newLoginAliasCmd())
	cmd.AddCommand(newPingCmd())
	cmd.AddCommand(newAPICmd())
	cmd.AddCommand(newSearchCmd())
	cmd.AddCommand(newContextCmd())
	cmd.AddCommand(newFieldsCmd())
	cmd.AddCommand(newTailCmd())
	cmd.AddCommand(newTracesCmd())
	cmd.AddCommand(newSendCmd())
	cmd.AddCommand(newUsageCmd())
	cmd.AddCommand(newQueryCmd())
	cmd.AddCommand(newPluginsCmd())

	topLevel := map[string]*cobra.Command{}
	for _, d := range resourceRegistry {
		if d.AttachTo != "" {
			continue // nested resources attach after their parent exists
		}
		var rc *cobra.Command
		switch d.Name {
		case "monitors":
			rc = newResourceCmd(d, newMonitorEventsCmd(), newMonitorMuteCmd(), newMonitorCheckCmd())
		case "exports":
			rc = newResourceCmd(d, newExportsCreateCmd())
		case "users":
			rc = newResourceCmd(d,
				newUserActionCmd("deactivate", "Deactivate a user"),
				newUserActionCmd("reactivate", "Reactivate a deactivated user"),
				newUserActionCmd("resend-invite", "Resend a pending user's invitation"))
		case "groups":
			rc = newResourceCmd(d, newGroupMembersCmd())
		default:
			rc = newResourceCmd(d)
		}
		topLevel[d.Name] = rc
		cmd.AddCommand(rc)
	}
	for _, d := range resourceRegistry {
		if d.AttachTo == "" {
			continue
		}
		parent, ok := topLevel[d.AttachTo]
		if !ok {
			panic("resource " + d.Name + " attaches to unknown command " + d.AttachTo)
		}
		parent.AddCommand(newResourceCmd(d))
	}

	wrapArgsValidators(cmd)

	return cmd
}

// wrapArgsValidators walks the command tree and wraps every command's Args
// validator so positional-argument validation failures (e.g. cobra's
// "accepts N arg(s), received M" / "unknown command ..." / "requires at
// least N arg(s)" errors) surface as usage_invalid_args clierr.Errors. That
// gives them the correct exit code (2, per the usage_ prefix contract)
// instead of falling through to the generic exit code (1) that a plain
// error produces.
func wrapArgsValidators(cmd *cobra.Command) {
	if cmd.Args != nil {
		orig := cmd.Args
		cmd.Args = func(c *cobra.Command, args []string) error {
			err := orig(c, args)
			if err == nil {
				return nil
			}
			var ce *clierr.Error
			if errors.As(err, &ce) {
				return err
			}
			hint := "Run '" + c.CommandPath() + " --help' for usage."
			// "unknown command" deserves a did-you-mean: cobra computes
			// suggestions but its plumbing never fires through custom Args
			// validators, so surface them in the hint ourselves.
			if len(args) > 0 && strings.Contains(err.Error(), "unknown command") {
				if suggestions := c.SuggestionsFor(args[0]); len(suggestions) > 0 {
					hint = fmt.Sprintf("Did you mean %q? %s", suggestions[0], hint)
				}
			}
			return clierr.New("usage_invalid_args", err.Error()).WithHint(hint)
		}
	}
	for _, sub := range cmd.Commands() {
		wrapArgsValidators(sub)
	}
}

// Execute runs the command tree and normalizes cobra errors that surface
// untyped (currently: required-flag validation) into typed usage errors.
// It also implements kubectl/gh-style exec plugin dispatch: before letting
// cobra parse argv at all, it checks whether argv[0] — the first argument,
// with no flags preceding it — is NOT one of bronto's own commands, and if
// a `bronto-<argv[0]>` executable exists on PATH, runs it with the
// remaining args instead — returning a *PluginExit carrying the plugin's
// own exit code. This has to happen before cobra's own parsing: cobra
// would otherwise try (and fail, with an unrelated "unknown flag" error)
// to parse flags meant for the plugin against the root command's flag
// set. Global flags before the plugin name are NOT recognized as
// belonging to it (matching kubectl/gh): `bronto --profile prod myplug`
// does not dispatch to bronto-myplug. See tryPluginDispatch (plugins.go)
// for the full decision logic.
//
// argv is the raw argument vector the root command was invoked with (e.g.
// os.Args[1:], or a test's SetArgs slice) — it is used to recover the
// attempted command name and its trailing args for plugin dispatch. All
// entry points — main and tests — should run commands through this.
func Execute(ctx context.Context, cmd *cobra.Command, argv []string) error {
	if err := tryPluginDispatch(cmd, argv); err != nil {
		return err
	}
	return WrapExecuteError(cmd.ExecuteContext(ctx))
}

// WrapExecuteError wraps cobra required-flag errors that surface from Execute
// as plain errors into usage_missing_flag clierr.Errors so they exit with
// the correct code (2). This should be called on the error returned from
// cmd.Execute().
func WrapExecuteError(err error) error {
	if err == nil {
		return nil
	}
	errMsg := err.Error()
	if strings.HasPrefix(errMsg, "required flag(s)") {
		return clierr.New("usage_missing_flag", errMsg).
			WithHint("Run 'bronto --help' for usage.")
	}
	return err
}
