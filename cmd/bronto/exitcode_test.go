package main

import (
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

func TestUsageErrorsExitTwo(t *testing.T) {
	err := clierr.New("usage_invalid_flag", "unknown flag")
	if clierr.ExitCode(err) != 2 {
		t.Fatal("usage errors must exit 2")
	}
}
