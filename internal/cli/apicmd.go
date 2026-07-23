package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/api"
	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
)

var allowedMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true,
}

func newAPICmd() *cobra.Command {
	var fields []string
	var input string
	var contentType string
	cmd := &cobra.Command{
		Use:   "api <METHOD> <path>",
		Short: "Make an authenticated request to any Bronto API endpoint",
		Long: "Escape hatch for endpoints without a dedicated command.\n" +
			"Auth and region resolution are handled for you.",
		Example: "  bronto api GET /logs\n" +
			"  bronto api GET /monitors -f limit=10\n" +
			"  bronto api POST /search --input query.json\n" +
			"  echo '{\"time_range\":\"Last 15 minutes\"}' | bronto api POST /search --input -",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			path := args[1]
			if !allowedMethods[method] {
				return clierr.New("usage_invalid_method", fmt.Sprintf("unsupported HTTP method %q", args[0])).
					WithHint("Use GET, POST, PUT, PATCH, DELETE, or HEAD.")
			}
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			if app.Config.APIKey() == "" {
				return clierr.New("auth_missing_key", "no API key configured").
					WithHint("Set BRONTO_API_KEY or pass --api-key.")
			}

			var bodyBytes []byte
			hasBodyMethod := method == "POST" || method == "PUT" || method == "PATCH"
			switch {
			case input != "" && len(fields) > 0 && hasBodyMethod:
				return clierr.New("usage_conflicting_flags", "--input and --field are mutually exclusive for body requests")
			case input != "":
				bodyBytes, err = readBodyInput(cmd, input)
				if err != nil {
					return err
				}
			case hasBodyMethod && len(fields) > 0:
				obj, err := parseFieldArgs(fields)
				if err != nil {
					return err
				}
				bodyBytes, err = json.Marshal(obj)
				if err != nil {
					return err
				}
			}

			if !hasBodyMethod && len(fields) > 0 {
				q := url.Values{}
				for _, kv := range fields {
					k, v, ok := strings.Cut(kv, "=")
					if !ok {
						return clierr.New("usage_invalid_field", fmt.Sprintf("--field %q is not key=value", kv))
					}
					q.Add(k, v)
				}
				sep := "?"
				if strings.Contains(path, "?") {
					sep = "&"
				}
				path += sep + q.Encode()
			}

			// The escape hatch honors --dry-run like every command built on
			// doJSONRequest: mutating methods print the plan document and
			// never touch the network. It's the command a user reaches for
			// to preview an UNdocumented mutation, so skipping this here
			// would execute exactly the calls --dry-run exists to preview.
			if app.DryRun && method != http.MethodGet && method != http.MethodHead {
				p, err := app.Printer(false)
				if err != nil {
					return err
				}
				return p.PrintJSON(dryRunPlan(method, path, bodyBytes))
			}

			var body io.Reader
			if bodyBytes != nil {
				body = bytes.NewReader(bodyBytes)
			}
			req, err := http.NewRequestWithContext(cmd.Context(), method, app.Config.BaseURL()+path, body)
			if err != nil {
				return err
			}
			if body != nil {
				req.Header.Set("Content-Type", contentType)
			}
			resp, err := app.HTTPClient.Do(req)
			if err != nil {
				return clierr.New("network_error", err.Error()).WithRetryable().
					WithHint("Check your network and the API base URL / region.")
			}
			defer func() { _ = resp.Body.Close() }()
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			if apiErr := api.ErrorFromStatus(resp.StatusCode, respBody); apiErr != nil {
				return apiErr
			}
			if len(respBody) == 0 {
				return nil
			}
			var doc any
			// bronto.DecodeJSON (UseNumber), NOT json.Unmarshal: plain
			// decoding rounds >2^53 ids (metadata.sequence) through float64.
			if err := bronto.DecodeJSON(respBody, &doc); err != nil {
				_, err := app.Stdout.Write(respBody) // non-JSON: pass through
				return err
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintJSON(doc)
		},
	}
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil,
		"key=value pair: query param for GET/DELETE, JSON body field otherwise (repeatable)")
	cmd.Flags().StringVar(&input, "input", "", "request body from file, or - for stdin")
	cmd.Flags().StringVar(&contentType, "content-type", "application/json", "Content-Type header for request bodies")
	return cmd
}

// parseFieldArgs turns repeated key=value pairs into a JSON body object.
// Each value is tried as JSON first (so `-f limit=10` produces a number and
// `-f enabled=true` a bool); anything that doesn't parse as JSON is kept as
// a plain string. Shared by the api escape hatch and the generic resource
// commands (resources.go).
func parseFieldArgs(fields []string) (map[string]any, error) {
	obj := map[string]any{}
	for _, kv := range fields {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, clierr.New("usage_invalid_field", fmt.Sprintf("--field %q is not key=value", kv))
		}
		var parsed any
		// UseNumber here too: `-f seq=4367602734065516544` must reach the
		// wire byte-exact (json.Number re-marshals verbatim).
		if err := bronto.DecodeJSON([]byte(v), &parsed); err == nil {
			obj[k] = parsed
		} else {
			obj[k] = v
		}
	}
	return obj, nil
}

// readBodyInput reads a request body from a file path, or from stdin when
// input is "-". Shared by the api escape hatch and the generic resource
// commands (resources.go).
func readBodyInput(cmd *cobra.Command, input string) ([]byte, error) {
	if input == "-" {
		return io.ReadAll(cmd.InOrStdin())
	}
	f, err := os.Open(input) // #nosec G304 -- input is the user's own --input path; reading user files is the feature
	if err != nil {
		return nil, clierr.New("usage_input_file", err.Error())
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}
