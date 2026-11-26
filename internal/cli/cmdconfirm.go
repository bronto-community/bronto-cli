package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// confirmOrAbort is confirmDestructive for the hand-written deletes: ok
// is false when the user declined (already reported), and a dry run
// never asks.
func confirmOrAbort(cmd *cobra.Command, app *App, prompt string, yes bool) (ok bool, err error) {
	if app.DryRun {
		return true, nil
	}
	if err := confirmDestructive(cmd, app, prompt, yes); err != nil {
		if errors.Is(err, errAborted) {
			_, _ = fmt.Fprintln(app.Stderr, "Aborted.")
			return false, nil
		}
		return false, err
	}
	return true, nil
}
