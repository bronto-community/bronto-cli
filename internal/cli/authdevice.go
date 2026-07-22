package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// EXPERIMENTAL (issue #38): RFC 8628 device-authorization login. The
// Bronto platform does not publish these endpoints yet, so everything is
// configurable (device_auth_url / device_token_url / device_client_id)
// and an endpoint that answers 404/405/501 degrades into a clear
// "unsupported, paste a key instead" error.

const (
	deviceGrantType   = "urn:ietf:params:oauth:grant-type:device_code"
	deviceDefaultWait = 5 * time.Second
	deviceMaxWait     = 15 * time.Minute
)

// openBrowser is a seam; best-effort — the printed URL is the contract.
var openBrowser = func(u string) error {
	// #nosec G204 -- u is the OAuth verification URI the user just approved opening
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start() // #nosec G204
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start() // #nosec G204
	default:
		return exec.Command("xdg-open", u).Start() // #nosec G204
	}
}

type deviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// deviceEndpoints resolves the two RFC 8628 endpoints: explicit config
// wins, otherwise they derive from the API base URL.
func deviceEndpoints(app *App) (authURL, tokenURL, clientID string) {
	get := func(k string) string { v, _ := app.Config.Get(k); return v.Val }
	base := strings.TrimRight(app.Config.BaseURL(), "/")
	authURL = get("device_auth_url")
	if authURL == "" {
		authURL = base + "/oauth/device/authorization"
	}
	tokenURL = get("device_token_url")
	if tokenURL == "" {
		tokenURL = base + "/oauth/token"
	}
	clientID = get("device_client_id")
	if clientID == "" {
		clientID = "bronto-cli"
	}
	return authURL, tokenURL, clientID
}

func runAuthDeviceLogin(cmd *cobra.Command) error {
	app, err := NewApp(cmd)
	if err != nil {
		return err
	}
	authURL, tokenURL, clientID := deviceEndpoints(app)

	auth, err := deviceAuthorize(cmd.Context(), authURL, clientID)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(app.Stderr, "First, copy this one-time code: %s\n", auth.UserCode)
	uri := auth.VerificationURIComplete
	if uri == "" {
		uri = auth.VerificationURI
	}
	if stdinIsTTY() && stdoutIsTTY() {
		_, _ = fmt.Fprintf(app.Stderr, "Press Enter to open %s in your browser…", uri)
		_, _ = bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err := openBrowser(uri); err != nil {
			_, _ = fmt.Fprintf(app.Stderr, "Could not open a browser — visit %s manually.\n", uri)
		}
	} else {
		_, _ = fmt.Fprintf(app.Stderr, "Visit %s and enter the code.\n", uri)
	}

	key, err := devicePollToken(cmd.Context(), app, tokenURL, clientID, auth)
	if err != nil {
		return err
	}
	return storeLoginKey(cmd, app, key)
}

func deviceAuthorize(ctx context.Context, authURL, clientID string) (deviceAuthorization, error) {
	var auth deviceAuthorization
	status, body, err := devicePost(ctx, authURL, url.Values{"client_id": {clientID}})
	if err != nil {
		return auth, clierr.New("network_error", fmt.Sprintf("device authorization request failed: %v", err)).WithRetryable()
	}
	switch {
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented:
		return auth, clierr.New("auth_device_unsupported",
			"this Bronto endpoint does not offer browser (device-flow) login yet").
			WithHint("Use 'bronto auth login' and paste an API key. If your platform exposes a device flow elsewhere, point device_auth_url/device_token_url at it.")
	case status != http.StatusOK:
		return auth, clierr.New("auth_device_error",
			fmt.Sprintf("device authorization endpoint returned %d: %s", status, truncateBody(body)))
	}
	if err := json.Unmarshal(body, &auth); err != nil || auth.DeviceCode == "" || auth.UserCode == "" {
		return auth, clierr.New("auth_device_error", "device authorization response is not RFC 8628 shaped").
			WithHint("Expected JSON with device_code, user_code, verification_uri.")
	}
	return auth, nil
}

func devicePollToken(ctx context.Context, app *App, tokenURL, clientID string, auth deviceAuthorization) (string, error) {
	wait := deviceDefaultWait
	if auth.Interval > 0 {
		wait = time.Duration(auth.Interval) * time.Second
	}
	deadline := deviceMaxWait
	if auth.ExpiresIn > 0 {
		deadline = time.Duration(auth.ExpiresIn) * time.Second
	}
	expiry := time.Now().Add(deadline)
	if !app.Quiet {
		_, _ = fmt.Fprintln(app.Stderr, "Waiting for approval…")
	}
	for {
		if time.Now().After(expiry) {
			return "", clierr.New("auth_device_expired", "the device code expired before the login was approved").
				WithHint("Run 'bronto auth login --device' again.")
		}
		select {
		case <-ctx.Done():
			return "", clierr.New("auth_device_error", "device login cancelled")
		case <-time.After(wait):
		}
		status, body, err := devicePost(ctx, tokenURL, url.Values{
			"grant_type": {deviceGrantType}, "device_code": {auth.DeviceCode}, "client_id": {clientID},
		})
		if err != nil {
			return "", clierr.New("network_error", fmt.Sprintf("token request failed: %v", err)).WithRetryable()
		}
		var tok struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
		}
		_ = json.Unmarshal(body, &tok)
		switch {
		case status == http.StatusOK && tok.AccessToken != "":
			return tok.AccessToken, nil
		case tok.Error == "authorization_pending":
			// keep polling
		case tok.Error == "slow_down":
			wait += deviceDefaultWait
		case tok.Error == "access_denied":
			return "", clierr.New("auth_device_denied", "the login request was denied in the browser")
		case tok.Error == "expired_token":
			return "", clierr.New("auth_device_expired", "the device code expired before the login was approved").
				WithHint("Run 'bronto auth login --device' again.")
		default:
			return "", clierr.New("auth_device_error",
				fmt.Sprintf("token endpoint returned %d: %s", status, truncateBody(body)))
		}
	}
}

// devicePost sends a form-encoded POST with a plain client: OAuth
// endpoints must never receive the X-BRONTO-API-KEY header the app
// client injects.
func devicePost(ctx context.Context, endpoint string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

func truncateBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
