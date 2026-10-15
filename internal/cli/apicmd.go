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
	"github.com/bronto-community/bronto-cli/internal/output"
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
		Long: "Calls any API endpoint, including ones without a dedicated command.\n" +
			"bronto adds the API key and sends the request to your region's base URL.",
		Example: "  bronto api GET /logs\n" +
			"  bronto api GET /monitors -f limit=10\n" +
			"  bronto api POST /search --input query.json\n" +
			"  echo '{\"time_range\":\"Last 15 minutes\"}' | bronto api POST /search --input -",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeAPIArgs, // <METHOD> then known collection paths
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
				// Only JSON bodies are checked: --content-type text/plain
				// (or any other non-JSON type) sends the bytes as given.
				if isJSONContentType(contentType) {
					if err := validateJSONBody(bodyBytes, input); err != nil {
						return err
					}
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
					if !ok || k == "" {
						return clierr.New("usage_invalid_field", fmt.Sprintf("--field %q is not key=value", kv))
					}
					q.Add(k, v) // repeated query keys are legitimate (?tag=a&tag=b)
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
				return printDryRunPlan(app, dryRunPlan(method, path, bodyBytes))
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
			resp, err := api.Do(app.HTTPClient, req)
			if err != nil {
				return err
			}
			respBody := resp.Body
			if len(respBody) == 0 { // e.g. 204 No Content, HEAD
				return nil
			}
			var doc any
			// bronto.DecodeJSON (UseNumber), NOT json.Unmarshal: plain
			// decoding rounds >2^53 ids (metadata.sequence) through float64.
			if err := bronto.DecodeJSON(respBody, &doc); err != nil {
				_, err := app.Stdout.Write(respBody) // non-JSON: pass through
				return err
			}
			format, err := app.DetectFormat(false)
			if err != nil {
				return err
			}
			if app.JQ == nil && !app.ListFieldsOnly && len(app.FieldFilter) == 0 {
				if out, ok := apiBodyFor(format, respBody); ok {
					_, err := app.Stdout.Write(out)
					return err
				}
			}
			p, err := app.PrinterFor(format)
			if err != nil {
				return err
			}
			return p.PrintJSON(doc)
		},
	}
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil,
		"key=value: a query parameter for GET/DELETE, a JSON body field otherwise (repeatable)")
	cmd.Flags().StringVar(&input, "input", "", "read the request body from a file, or - for stdin")
	cmd.Flags().StringVar(&contentType, "content-type", "application/json", "Content-Type header for request bodies")
	return cmd
}

// parseFieldArgs turns repeated key=value pairs into a JSON body object.
// Each value is tried as JSON first (so `-f limit=10` produces a number and
// `-f enabled=true` a bool); anything that doesn't parse as JSON is kept as
// a plain string. An empty key or a key given twice is a usage error
// rather than a silent {"": …} or last-one-wins. Shared by the api escape
// hatch and the generic resource commands (resources.go).
func parseFieldArgs(fields []string) (map[string]any, error) {
	obj := map[string]any{}
	for _, kv := range fields {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, clierr.New("usage_invalid_field", fmt.Sprintf("--field %q is not key=value", kv))
		}
		if _, dup := obj[k]; dup {
			return nil, clierr.New("usage_invalid_field", fmt.Sprintf("--field key %q is given more than once", k)).
				WithHint("Pass each key once; for a list value use JSON, e.g. -f 'tags=[\"a\",\"b\"]'.")
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

// apiBodyFor renders a JSON response body for format without decoding and
// re-encoding it, so key order, number spelling and characters like & < >
// stay exactly as the API sent them: verbatim for json and raw, indented
// for table (a terminal), compacted to one line for jsonl. ok is false for
// csv, which needs the decoded document.
func apiBodyFor(format output.Format, body []byte) (out []byte, ok bool) {
	var buf bytes.Buffer
	switch format {
	case output.FormatJSON, output.FormatRaw:
		buf.Write(bytes.TrimRight(body, "\r\n"))
	case output.FormatTable:
		if err := json.Indent(&buf, body, "", "  "); err != nil {
			return nil, false
		}
	case output.FormatJSONL:
		if err := json.Compact(&buf, body); err != nil {
			return nil, false
		}
	default:
		return nil, false
	}
	buf.WriteByte('\n')
	return buf.Bytes(), true
}

// isJSONContentType reports whether a Content-Type names a JSON body:
// application/json or any +json suffix type, parameters ignored.
func isJSONContentType(ct string) bool {
	mt, _, _ := strings.Cut(ct, ";")
	mt = strings.ToLower(strings.TrimSpace(mt))
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// validateJSONBody rejects an --input body that isn't a JSON object or
// array before it is sent: invalid JSON would otherwise reach the API as a
// raw string, and an empty file as a body-less request, both failing there
// with a less helpful 400.
func validateJSONBody(body []byte, input string) error {
	src := input
	if input == "-" {
		src = "stdin"
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return clierr.New("usage_invalid_body", fmt.Sprintf("--input %s is empty", src)).
			WithHint("The request body must be a JSON object or array.")
	}
	if (trimmed[0] != '{' && trimmed[0] != '[') || !json.Valid(trimmed) {
		return clierr.New("usage_invalid_body", fmt.Sprintf("--input %s is not a JSON object or array", src)).
			WithHint("Check the file with 'jq . " + src + "'; for a non-JSON body use bronto api --content-type.")
	}
	return nil
}
