package cli

import (
	"context"
	"encoding/json"
)

// sendWrite sends a mutating request and prints what comes back (the
// updated object, or the --dry-run plan); a 204 is confirmed on stderr
// with done.
func sendWrite(ctx context.Context, app *App, method, path string, body any, done string) error {
	var raw []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		raw = b
	default:
		var err error
		if raw, err = json.Marshal(b); err != nil {
			return err
		}
	}
	payload, err := doJSONRequest(ctx, app, method, path, raw)
	if err != nil {
		return err
	}
	if payload == nil {
		app.Notef("%s\n", done)
		return nil
	}
	p, err := app.Printer(false)
	if err != nil {
		return err
	}
	return p.PrintJSON(payload)
}
