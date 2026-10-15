package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bronto-community/bronto-cli/internal/cli"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/telemetry"
	"github.com/bronto-community/bronto-cli/internal/version"
	"github.com/mattn/go-isatty"
)

// telemetryFlushTimeout bounds the span flush on exit, so an unreachable
// OTLP backend delays the exit by at most this much and never changes the
// exit code.
const telemetryFlushTimeout = 3 * time.Second

func main() {
	os.Exit(run())
}

func run() int {
	cmd := cli.NewRootCmd()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdown := telemetry.Setup(ctx, version.Version)
	defer func() {
		// Not the signal context: an interrupted run still flushes its spans.
		fctx, cancel := context.WithTimeout(context.Background(), telemetryFlushTimeout)
		defer cancel()
		_ = shutdown(fctx)
	}()
	// The root span is renamed to the resolved command path by cli.Execute.
	tctx, span := telemetry.StartCommand(ctx)

	err := cli.Execute(tctx, cmd, os.Args[1:])
	code, render := exitStatus(ctx, err)
	telemetry.EndCommand(span, code, errorType(ctx, err))
	if render {
		machine := !isatty.IsTerminal(os.Stderr.Fd()) && !isatty.IsCygwinTerminal(os.Stderr.Fd())
		clierr.Render(os.Stderr, err, machine)
	}
	return code
}

// exitStatus maps the outcome of an executed command to a process exit
// code. A run aborted by the signal context exits 130 (128+SIGINT) without
// rendering an error — the interrupt was the user's own action — unless
// the command's clean-up after the interrupt failed (cli.ShutdownError),
// which is rendered with its own exit code. An exec
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
	var se *cli.ShutdownError
	if errors.As(err, &se) {
		return clierr.ExitCode(se.Err), true
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return 130, false
	}
	return clierr.ExitCode(err), true
}

// errorType names a failed run's error for the root span's error.type
// attribute: the typed error code where there is one.
func errorType(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	var pe *cli.PluginExit
	if errors.As(err, &pe) {
		return "plugin_exit"
	}
	var se *cli.ShutdownError
	if !errors.As(err, &se) && (ctx.Err() != nil || errors.Is(err, context.Canceled)) {
		return "canceled"
	}
	var ce *clierr.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}
