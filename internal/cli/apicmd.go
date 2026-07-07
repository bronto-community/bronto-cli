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

	"github.com/svrnm/bronto-cli/internal/api"
	"github.com/svrnm/bronto-cli/internal/clierr"
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

			var body io.Reader
			hasBodyMethod := method == "POST" || method == "PUT" || method == "PATCH"
			switch {
			case input != "" && len(fields) > 0 && hasBodyMethod:
				return clierr.New("usage_conflicting_flags", "--input and --field are mutually exclusive for body requests")
			case input == "-":
				body = cmd.InOrStdin()
			case input != "":
				f, err := os.Open(input)
				if err != nil {
					return clierr.New("usage_input_file", err.Error())
				}
				defer func() { _ = f.Close() }()
				body = f
			case hasBodyMethod && len(fields) > 0:
				obj := map[string]any{}
				for _, kv := range fields {
					k, v, ok := strings.Cut(kv, "=")
					if !ok {
						return clierr.New("usage_invalid_field", fmt.Sprintf("--field %q is not key=value", kv))
					}
					var parsed any
					if err := json.Unmarshal([]byte(v), &parsed); err == nil {
						obj[k] = parsed
					} else {
						obj[k] = v
					}
				}
				b, err := json.Marshal(obj)
				if err != nil {
					return err
				}
				body = bytes.NewReader(b)
			}

			u := app.Config.BaseURL() + path
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
				if strings.Contains(u, "?") {
					sep = "&"
				}
				u += sep + q.Encode()
			}

			req, err := http.NewRequestWithContext(cmd.Context(), method, u, body)
			if err != nil {
				return err
			}
			if body != nil {
				req.Header.Set("Content-Type", contentType)
			}
			resp, err := app.HTTPClient.Do(req)
			if err != nil {
				return clierr.New("api_unreachable", err.Error())
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
			if err := json.Unmarshal(respBody, &doc); err != nil {
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
