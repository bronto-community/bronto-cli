package main

import (
	"context"
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
	if err := cli.Execute(context.Background(), cmd); err != nil {
		machine := !isatty.IsTerminal(os.Stderr.Fd()) && !isatty.IsCygwinTerminal(os.Stderr.Fd())
		clierr.Render(os.Stderr, err, machine)
		return clierr.ExitCode(err)
	}
	return 0
}
