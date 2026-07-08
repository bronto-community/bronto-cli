package main

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/svrnm/bronto-cli/internal/cli"
	"github.com/svrnm/bronto-cli/internal/clierr"
)

func TestExitStatusNilErrIsZeroNoRender(t *testing.T) {
	code, render := exitStatus(context.Background(), nil)
	if code != 0 || render {
		t.Fatalf("exitStatus(nil, nil) = (%d, %v), want (0, false)", code, render)
	}
}

func TestExitStatusCanceledContextIs130NoRender(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wrapped := &url.Error{Op: "Get", URL: "http://example.com", Err: context.Canceled}
	code, render := exitStatus(ctx, wrapped)
	if code != 130 || render {
		t.Fatalf("exitStatus(canceled, wrapped) = (%d, %v), want (130, false)", code, render)
	}
}

func TestExitStatusContextCanceledErrorIs130NoRender(t *testing.T) {
	wrapped := &url.Error{Op: "Get", URL: "http://example.com", Err: context.Canceled}
	code, render := exitStatus(context.Background(), wrapped)
	if code != 130 || render {
		t.Fatalf("exitStatus(live, context.Canceled err) = (%d, %v), want (130, false)", code, render)
	}
}

func TestExitStatusPlainErrorLiveContextIsOneRender(t *testing.T) {
	code, render := exitStatus(context.Background(), errors.New("boom"))
	if code != 1 || !render {
		t.Fatalf("exitStatus(live, plain error) = (%d, %v), want (1, true)", code, render)
	}
}

func TestExitStatusUsageErrorIsTwoRender(t *testing.T) {
	err := clierr.New("usage_invalid_flag", "bad flag")
	code, render := exitStatus(context.Background(), err)
	if code != 2 || !render {
		t.Fatalf("exitStatus(live, usage error) = (%d, %v), want (2, true)", code, render)
	}
}

// TestExitStatusPluginExitPassesCodeThroughUnrendered pins the exec-plugin
// exit-code contract: a plugin's own exit code passes through verbatim,
// with no clierr rendering (the plugin already wrote its own output).
func TestExitStatusPluginExitPassesCodeThroughUnrendered(t *testing.T) {
	code, render := exitStatus(context.Background(), &cli.PluginExit{Code: 7})
	if code != 7 || render {
		t.Fatalf("exitStatus(live, PluginExit{7}) = (%d, %v), want (7, false)", code, render)
	}
}
