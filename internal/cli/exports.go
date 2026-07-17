package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

// exportPollInterval is the --wait poll cadence against GET /exports/{id}.
// Package-level so tests can shrink it instead of waiting on a real clock.
var exportPollInterval = 3 * time.Second

// newExportsCreateCmd hand-writes "exports create": unlike every other
// resource's generic create, it accepts EITHER a raw body (--input/-f, same
// as the generic path) OR a set of convenience flags (--dataset/--where/
// --since/--from/--to) that build the {"search_details": {...}} shape the
// /exports endpoint expects — mirroring bronto.SearchRequest.Body()'s
// from/time_range/where field layout. It also owns --wait (poll until
// COMPLETE/FAILED) and --download (implies --wait, then streams the
// completed export's presigned location to a file). Registered in root.go
// as an extra that REPLACES the generic "create" for the exports resource.
func newExportsCreateCmd() *cobra.Command {
	var fields []string
	var input string
	var dataset, where, since, from, to string
	var wait bool
	var download string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an export",
		Example: "  bronto exports create --dataset <dataset> --since 1h --where \"status=500\" --wait\n" +
			"  bronto exports create --input body.json\n" +
			"  bronto exports create --dataset <dataset> --since 1h --download out.json.gz",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if download != "" {
				wait = true
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			// Only resolve when the convenience path is actually in play:
			// with --input the flags conflict and exportRequestBody says so
			// without a wasted /logs lookup.
			if dataset != "" && input == "" {
				if dataset, err = resolveDatasetRef(cmd.Context(), app, dataset); err != nil {
					return err
				}
			}
			body, err := exportRequestBody(cmd, input, fields, dataset, where, since, from, to)
			if err != nil {
				return err
			}
			payload, err := doJSONRequest(cmd.Context(), app, http.MethodPost, "/exports", body)
			if err != nil {
				return err
			}
			obj, _ := payload.(map[string]any)
			if !wait {
				p, err := app.Printer(false)
				if err != nil {
					return err
				}
				return p.PrintJSON(payload)
			}
			id := exportID(obj)
			if id == "" {
				return clierr.New("export_no_id", "create response had no id to poll")
			}
			final, err := waitForExport(cmd.Context(), app, id)
			if err != nil {
				return err
			}
			if download != "" {
				location, _ := final["location"].(string)
				if location == "" {
					return clierr.New("export_no_location", "completed export has no download location")
				}
				if err := downloadExport(cmd.Context(), app, location, download); err != nil {
					return err
				}
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintJSON(final)
		},
	}
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "key=value pair for the request body (repeatable)")
	cmd.Flags().StringVar(&input, "input", "", "request body from file, or - for stdin")
	cmd.Flags().StringVarP(&dataset, "dataset", "d", "", "dataset (name or UUID) to export (convenience flag)")
	cmd.Flags().StringVar(&where, "where", "", "query filter (convenience flag)")
	cmd.Flags().StringVar(&since, "since", "", "relative lookback, e.g. 1h (convenience flag)")
	cmd.Flags().StringVar(&from, "from", "", "absolute start time, RFC3339 (convenience flag)")
	cmd.Flags().StringVar(&to, "to", "", "absolute end time, RFC3339 (convenience flag)")
	cmd.Flags().BoolVar(&wait, "wait", false, "poll GET /exports/{id} every 3s until COMPLETE or FAILED")
	cmd.Flags().StringVar(&download, "download", "",
		"download the completed export to this path (implies --wait)")
	return cmd
}

// exportRequestBody resolves the export create body from exactly one of:
// --input/-f (delegates to resourceRequestBody, same contract as every
// other resource's create), or the --dataset/--where/--since/--from/--to
// convenience flags. The two families are mutually exclusive.
func exportRequestBody(cmd *cobra.Command, input string, fields []string, dataset, where, since, from, to string) ([]byte, error) {
	bodyFlags := input != "" || len(fields) > 0
	convFlags := dataset != "" || where != "" || since != "" || from != "" || to != ""
	switch {
	case bodyFlags && convFlags:
		return nil, clierr.New("usage_conflicting_flags",
			"--input/--field and the convenience flags (--dataset/--where/--since/--from/--to) are mutually exclusive")
	case bodyFlags:
		return resourceRequestBody(cmd, input, fields)
	case convFlags:
		spec, err := timerange.Resolve(since, from, to, nil)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"search_details": exportSearchDetails(dataset, where, spec)})
	default:
		return nil, clierr.New("usage_missing_body",
			"provide --input <file|-> / -f key=value, or --dataset/--where/--since convenience flags").
			WithHint("Example: --dataset <name-or-uuid> --since 1h --where \"status=500\"")
	}
}

// exportSearchDetails builds the search_details object, mirroring
// bronto.SearchRequest.Body()'s from/time_range-or-from_ts+to_ts/where
// layout (see internal/bronto/search.go). Per ExportDetails schema, where
// is always present (empty string if not provided).
func exportSearchDetails(dataset, where string, spec timerange.Spec) map[string]any {
	d := map[string]any{}
	if dataset != "" {
		d["from"] = dataset
	}
	if spec.TimeRange != "" {
		d["time_range"] = spec.TimeRange
	} else if spec.FromTs != 0 || spec.ToTs != 0 {
		d["from_ts"] = spec.FromTs
		d["to_ts"] = spec.ToTs
	}
	d["where"] = where
	return d
}

// exportID extracts the created export's id. The vendored openapi.yaml's
// Export schema keys it "export_id"; "id" is accepted as a fallback for
// defensive compatibility.
func exportID(obj map[string]any) string {
	if obj == nil {
		return ""
	}
	if id, ok := obj["export_id"].(string); ok && id != "" {
		return id
	}
	if id, ok := obj["id"].(string); ok {
		return id
	}
	return ""
}

// waitForExport polls GET /exports/{id} every exportPollInterval (ctx-aware,
// same select-on-context-Done-or-timer shape as tail.go's poll loop) until
// status is COMPLETE (returns the final payload) or FAILED (returns a typed
// export_failed error, exit code 1). Any other status (CREATED, IN_PROGRESS)
// continues polling.
// Note: FAILED is not in the vendored spec's status enum (CREATED/IN_PROGRESS/COMPLETE)
// but is handled defensively as a real-world edge case.
func waitForExport(ctx context.Context, app *App, id string) (map[string]any, error) {
	for {
		payload, err := doJSONRequest(ctx, app, http.MethodGet, "/exports/"+url.PathEscape(id), nil)
		if err != nil {
			return nil, err
		}
		obj, _ := payload.(map[string]any)
		status, _ := obj["status"].(string)
		switch status {
		case "COMPLETE":
			return obj, nil
		case "FAILED":
			msg := fmt.Sprintf("export %s failed", id)
			if fd, ok := obj["failure_detail"].(string); ok && fd != "" {
				msg = fd
			}
			return obj, clierr.New("export_failed", msg)
		}
		// CREATED, IN_PROGRESS, or other statuses continue polling
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(exportPollInterval):
		}
	}
}

// downloadExport GETs a completed export's (presigned) location URL and
// streams the response to path. It uses a FRESH plain *http.Client rather
// than app.HTTPClient: app.HTTPClient's Transport unconditionally injects
// the X-BRONTO-API-KEY header, but the presigned URL is not a Bronto API
// endpoint — sending our API key to it would be wrong (and most presigned
// URL schemes reject unexpected auth headers/signatures outright).
func downloadExport(ctx context.Context, app *App, location, path string) error {
	if !app.Quiet {
		_, _ = fmt.Fprintf(app.Stderr, "Downloading export to %s...\n", path)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return err
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return clierr.New("network_error", err.Error()).WithRetryable().
			WithHint("Check your network; the download URL may also have expired.")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return clierr.New("export_download_failed", fmt.Sprintf("download returned HTTP %d", resp.StatusCode))
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return err
	}
	if !app.Quiet {
		_, _ = fmt.Fprintf(app.Stderr, "Downloaded %d bytes to %s.\n", n, path)
	}
	return nil
}
