package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/mattn/go-isatty"
	"github.com/svrnm/bronto-cli/internal/cli"
	"github.com/svrnm/bronto-cli/internal/clierr"
)

func main() {
	os.Exit(run())
}

func run() int {
	cmd := cli.NewRootCmd()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := cli.Execute(ctx, cmd, os.Args[1:])
	code, render := exitStatus(ctx, err)
	if render {
		machine := !isatty.IsTerminal(os.Stderr.Fd()) && !isatty.IsCygwinTerminal(os.Stderr.Fd())
		clierr.Render(os.Stderr, err, machine)
	}
	return code
}

// exitStatus maps the outcome of an executed command to a process exit
// code. A run aborted by the signal context exits 130 (128+SIGINT) without
// rendering an error — the interrupt was the user's own action. An exec
// plugin's own exit code passes through verbatim, unrendered: plugins own
// their exit codes and their own output.
func exitStatus(ctx context.Context, err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var pe *cli.PluginExit
	if errors.As(err, &pe) {
		return pe.Code, false
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return 130, false
	}
	return clierr.ExitCode(err), true
}
