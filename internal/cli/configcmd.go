package cli

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/clierr"
	"github.com/svrnm/bronto-cli/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and modify bronto configuration",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "Show all resolved config values and where each came from",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			vals := app.Config.Values()
			keys := make([]string, 0, len(vals))
			for k := range vals {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			rows := make([]map[string]any, 0, len(keys))
			for _, k := range keys {
				v := vals[k]
				val := v.Val
				if k == "api_key" && val != "" {
					val = val[:min(8, len(val))] + "…" // never print full secrets
				}
				rows = append(rows, map[string]any{"key": k, "value": val, "source": string(v.Source)})
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows([]string{"key", "value", "source"}, rows)
		},
	}

	get := &cobra.Command{
		Use:   "get <key>",
		Short: "Print a single resolved config value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			v, ok := app.Config.Get(args[0])
			if !ok {
				return clierr.New("config_key_not_found", fmt.Sprintf("no value for %q", args[0]))
			}
			fmt.Fprintln(app.Stdout, v.Val)
			return nil
		},
	}

	set := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Persist a config value in the user config file",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			dir := os.Getenv("BRONTO_CONFIG_DIR")
			if dir == "" {
				d, err := os.UserConfigDir()
				if err != nil {
					return err
				}
				dir = d
			}
			if err := config.SetUserValue(dir, app.Config.Profile(), args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintf(app.Stderr, "Set %s in profile %q\n", args[0], profileOrDefault(app.Config.Profile()))
			return nil
		},
	}

	cmd.AddCommand(list, get, set)
	return cmd
}

func profileOrDefault(p string) string {
	if p == "" {
		return "default"
	}
	return p
}
