package main

import (
	"os"

	"github.com/mattn/go-isatty"
	"github.com/svrnm/bronto-cli/internal/cli"
	"github.com/svrnm/bronto-cli/internal/clierr"
)

func main() {
	os.Exit(run())
}

func run() int {
	cmd := cli.NewRootCmd()
	if err := cmd.Execute(); err != nil {
		err = cli.WrapExecuteError(err)
		machine := !isatty.IsTerminal(os.Stderr.Fd()) && !isatty.IsCygwinTerminal(os.Stderr.Fd())
		clierr.Render(os.Stderr, err, machine)
		return clierr.ExitCode(err)
	}
	return 0
}
