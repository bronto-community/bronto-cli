package cli

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// newForwardConfigTestCmd builds "forward-configs test [<forward-config>]":
// POST /forward-configs/test-destination, where Bronto writes and deletes a
// test object to check it can reach an S3 destination. The destination
// comes from an existing forward config, from --bucket/--prefix/
// --storage-class, or from a config with the flags overriding its fields.
// The API answers 200 {"result": false} when it can't write (live-verified
// 2026-10-15), so a false result becomes a non-zero exit.
func newForwardConfigTestCmd() *cobra.Command {
	var bucket, prefix, storageClass string
	cmd := &cobra.Command{
		Use:   "test [forward-config] [--bucket <bucket>] [--prefix <prefix>] [--storage-class <class>]",
		Short: "Check that Bronto can write to a forward config's S3 destination",
		Example: "  bronto forward-configs test archive\n" +
			"  bronto forward-configs test --bucket my-archive-bucket --prefix bronto/",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeKindRef("forward-configs"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && bucket == "" {
				return clierr.New("usage_invalid_flags", "pass a forward config or --bucket (the destination to test)").
					WithHint("e.g. bronto forward-configs test --bucket <bucket>")
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			dest := map[string]any{"destination_type": "S3"}
			if len(args) == 1 {
				saved, err := forwardConfigDestination(cmd, app, args[0])
				if err != nil {
					return err
				}
				for k, v := range saved {
					dest[k] = v
				}
			}
			for k, v := range map[string]string{"bucket": bucket, "prefix": prefix, "storage_class": storageClass} {
				if v != "" {
					dest[k] = v
				}
			}
			body, err := json.Marshal(dest)
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodPost, "/forward-configs/test-destination", body)
			if err != nil {
				return err
			}
			if isDryRunPlan(payload) {
				return printDryRunPlan(app, payload)
			}
			where := "s3://" + strings.TrimSuffix(asString(dest["bucket"])+"/"+asString(dest["prefix"]), "/")
			if m, _ := payload.(map[string]any); m["result"] != true {
				return clierr.New("forward_destination_failed", "Bronto can't write to "+where).
					WithHint("Check the bucket name and that its bucket policy lets Bronto write and delete objects.")
			}
			app.Notef("Bronto can write to %s.\n", where)
			return nil
		},
	}
	cmd.Flags().StringVar(&bucket, "bucket", "", "S3 bucket to test (overrides the forward config's)")
	cmd.Flags().StringVar(&prefix, "prefix", "", "key prefix for the test object (overrides the forward config's)")
	cmd.Flags().StringVar(&storageClass, "storage-class", "", "S3 storage class: STANDARD, STANDARD_IA or GLACIER_IR")
	return cmd
}

// forwardConfigDestination looks up a forward config by id or name and
// returns its destination object. There is no per-id GET, so it reads the
// list once and resolves the reference against it.
func forwardConfigDestination(cmd *cobra.Command, app *App, ref string) (map[string]any, error) {
	desc, _ := descByName("forward-configs")
	payload, err := doJSONRequest(cmd.Context(), app, http.MethodGet, desc.Base, nil)
	if err != nil {
		return nil, err
	}
	rows := rowsFromPayload(payload, desc.ListRowKeys...)
	id, err := matchResourceRef(desc, rows, ref)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if desc.rowID(row) == id {
			if d, ok := row["destination"].(map[string]any); ok {
				return d, nil
			}
		}
	}
	return nil, clierr.New("resource_not_found", "forward config "+ref+" has no destination").
		WithHint("Pass --bucket to test a destination directly.")
}

// asString returns v as a string, or "" when it isn't one.
func asString(v any) string {
	s, _ := v.(string)
	return s
}
