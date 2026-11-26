package cli

import (
	"context"
	"encoding/json"
)

// sendWrite sends a mutating request and prints what comes back (the
// updated object, or the --dry-run plan). A response without a body is
// confirmed on stderr with done; when the caller names the path of the
// object it changed (fetch), -o json/jsonl and --jq print that object
// instead, as the generated update verb does.
func sendWrite(ctx context.Context, app *App, method, path string, body any, done string, fetch ...string) error {
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
		if len(fetch) > 0 && wantsJSONResult(app) {
			return printFetched(ctx, app, fetch[0])
		}
		app.Notef("%s\n", done)
		return nil
	}
	p, err := app.Printer(false)
	if err != nil {
		return err
	}
	return p.PrintJSON(payload)
}
