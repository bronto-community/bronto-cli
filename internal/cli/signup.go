package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/bronto-community/bronto-cli/internal/api"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/secrets"
	"github.com/bronto-community/bronto-cli/internal/version"
)

// PROTOTYPE: 'bronto signup' creates a Bronto account from the terminal.
// It drives the same public Cognito app client as the web sign-up form
// (see cognito.go), waits for the email verification, then creates and
// stores an API key the way 'bronto auth login' would.

const (
	signupTermsURL = "https://www.bronto.io/legal/terms"
	// signupPhone is the placeholder the web form sends: the user pool
	// requires phone_number, the form never asks for one.
	signupPhone = "+35385827736"
	// signupSpecials are the special characters the web form accepts.
	signupSpecials = `!"#$%&()*,.:<>?@^{|}`
)

var (
	// signupPollInterval is how often the waiting signup retries sign-in.
	signupPollInterval = 2 * time.Second
	// signupCheckWait bounds the retries of the first credential check: a
	// brand-new key can answer 401 for a moment.
	signupCheckWait = 10 * time.Second
	stderrIsTTY     = func() bool { return term.IsTerminal(int(os.Stderr.Fd())) }
)

type signupOpts struct {
	email, first, last, org string
	acceptTerms             bool
	generatePassword        bool
	passwordStdin           bool
	wait                    time.Duration
}

func newSignupCmd() *cobra.Command {
	var o signupOpts
	cmd := &cobra.Command{
		Use:   "signup",
		Short: "Create a Bronto account and set up the CLI (experimental prototype)",
		Long: "EXPERIMENTAL PROTOTYPE: flags, output and behavior will change.\n\n" +
			"Creates a Bronto account and organization, waits until you click the link in\n" +
			"the verification email, then creates an API key and stores it as a profile,\n" +
			"like 'bronto auth login'.\n\n" +
			"Anything not given as a flag is asked for when a terminal is attached. With\n" +
			"every flag given it never prompts. The region (--region eu|us) is permanent:\n" +
			"an organization cannot move to another region later.\n\n" +
			"No access to a browser? 'bronto signup confirm' takes the link (or the code\n" +
			"in it) from the email; a waiting 'bronto signup' then continues by itself.\n" +
			"If the wait ran out, run the same command again with the same password.",
		Example: "  bronto signup\n" +
			"  bronto signup --email ada@example.com --first-name Ada --last-name Lovelace \\\n" +
			"    --org 'Analytical Engines' --region eu --accept-terms --generate-password",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runSignup(cmd, o) },
	}
	f := cmd.Flags()
	f.StringVar(&o.email, "email", "", "email address to sign up with (it gets a verification link)")
	f.StringVar(&o.first, "first-name", "", "your first name")
	f.StringVar(&o.last, "last-name", "", "your last name")
	f.StringVar(&o.org, "org", "", "name of the organization to create")
	f.BoolVar(&o.acceptTerms, "accept-terms", false, "accept the terms of service ("+signupTermsURL+")")
	f.BoolVar(&o.generatePassword, "generate-password", false, "generate the account password and print it once")
	f.BoolVar(&o.passwordStdin, "password-stdin", false, "read the account password from stdin instead of prompting")
	f.DurationVar(&o.wait, "wait", 30*time.Minute, "how long to wait for the email verification")
	cmd.AddCommand(newSignupConfirmCmd())
	return cmd
}

func runSignup(cmd *cobra.Command, o signupOpts) error {
	app, err := newApp(cmd, appOptions{allowNewProfile: true})
	if err != nil {
		return err
	}
	if o.generatePassword && o.passwordStdin {
		return clierr.New("usage_conflicting_flags", "--generate-password and --password-stdin can't be combined")
	}
	// Never replace a key that is already stored: its value can't be read back from Bronto.
	profile := profileOrDefault(app.Config.Profile())
	if _, _, err := secrets.Get(profile); err == nil {
		return clierr.New("signup_profile_in_use", fmt.Sprintf("profile %q already has a stored API key", profile)).
			WithHint("Pass --profile <name> to keep the new account in its own profile, or 'bronto auth logout' first.")
	}

	ctx := cmd.Context()
	errw := app.Stderr
	in := bufio.NewReader(cmd.InOrStdin())
	interactive := stdinIsTTY() && !o.passwordStdin
	ask := func(val, flag, label string) (string, error) {
		if val != "" {
			return val, nil
		}
		if !interactive {
			return "", clierr.New("usage_missing_flag", "--"+flag+" is required when there is no terminal to ask on").
				WithHint("Run 'bronto signup --help' for the flags, or run it in a terminal to be prompted.")
		}
		for {
			_, _ = fmt.Fprint(errw, label+": ")
			line, err := in.ReadString('\n')
			if s := strings.TrimSpace(line); s != "" {
				return s, nil
			}
			if err != nil {
				return "", clierr.New("usage_missing_flag", "--"+flag+" is required")
			}
		}
	}

	if o.email, err = ask(o.email, "email", "Email"); err != nil {
		return err
	}
	if !strings.Contains(o.email, "@") {
		return clierr.New("usage_invalid_args", fmt.Sprintf("%q is not an email address", o.email))
	}
	if o.first, err = ask(o.first, "first-name", "First name"); err != nil {
		return err
	}
	if o.last, err = ask(o.last, "last-name", "Last name"); err != nil {
		return err
	}
	if o.org, err = ask(o.org, "org", "Organization name"); err != nil {
		return err
	}
	// Only an explicit choice counts: the region is permanent, so a region
	// from config or BRONTO_REGION must not pick it silently.
	region := ""
	if f := cmd.Flags().Lookup("region"); f != nil && f.Changed {
		region = f.Value.String()
	}
	if region, err = ask(region, "region", "Region, eu or us (permanent: it can't be changed later)"); err != nil {
		return err
	}
	region = strings.ToLower(region)
	if _, ok := signupRegions[region]; !ok {
		return clierr.New("usage_invalid_region", fmt.Sprintf("--region must be \"eu\" or \"us\", got %q", region))
	}

	password, err := signupPassword(cmd, o, interactive)
	if err != nil {
		return err
	}

	if !o.acceptTerms {
		answer, err := ask("", "accept-terms", "Accept the terms of service at "+signupTermsURL+"? [y/N]")
		if err != nil {
			return err
		}
		if a := strings.ToLower(answer); a != "y" && a != "yes" {
			return clierr.New("usage_confirmation_required", "the terms of service were not accepted")
		}
	}

	cg := newCognitoClient(region)
	err = cg.call(ctx, "SignUp", map[string]any{
		"ClientId": cg.region.clientID, "Username": o.email, "Password": password,
		"UserAttributes": []map[string]string{
			{"Name": "email", "Value": o.email},
			{"Name": "name", "Value": o.first},
			{"Name": "family_name", "Value": o.last},
			{"Name": "phone_number", "Value": signupPhone},
			{"Name": "custom:organization", "Value": o.org},
		},
	}, nil)
	switch {
	case err == nil:
		_, _ = fmt.Fprintf(errw, "\nAccount created for %s.\n", o.email)
		if o.generatePassword {
			// The one place a password is ever printed.
			_, _ = fmt.Fprintf(errw, "\nGenerated password (SECRET, shown only once: save it in a password manager now):\n\n    %s\n", password)
		}
	case isCognitoErr(err, "UsernameExistsException"):
		if o.generatePassword {
			return clierr.New("signup_failed", "an account for "+o.email+" already exists").
				WithHint("To finish an earlier sign-up, run this again with that account's password (--password-stdin or the prompt) instead of --generate-password. Otherwise log in at " + signupAppURL(app, region) + ".")
		}
		_, _ = fmt.Fprintf(errw, "\nAn account for %s already exists: picking up where it left off.\n", o.email)
	default:
		return signupErr(err)
	}

	token, err := signupWait(ctx, app, cg, o, region, password)
	if err != nil {
		return err
	}

	baseURL := "https://api." + region + ".bronto.io"
	if _, ok := app.Config.Get("base_url"); ok {
		baseURL = app.Config.BaseURL()
	}
	key, err := signupCreateKey(ctx, baseURL, token)
	if err != nil {
		return err
	}
	// Store before checking: the key value exists nowhere else.
	profile, where, err := storeLogin(app, key, region)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(errw, "API key created and stored in the %s (profile %q).\n", where, profile)
	if err := signupCheckKey(ctx, regionCandidate{region: region, baseURL: baseURL}, key); err != nil {
		return err
	}

	appURL := signupAppURL(app, region)
	_, _ = fmt.Fprintln(app.Stdout)
	printBox(app.Stdout, []string{
		"Welcome to Bronto, " + o.first + "!",
		"",
		"Organization  " + o.org,
		"Region        " + region,
		"Account       " + o.email,
		"CLI           ready (profile " + profile + ")",
	})
	_, _ = fmt.Fprintf(app.Stdout, "\nNext steps:\n"+
		"  bronto send -d hello -m 'hello world'    send a first log event\n"+
		"  bronto search -d hello --since 15m       and find it again\n"+
		"  bronto datasets list                     see your datasets\n"+
		"  %s                 the web app (log in with your email and password)\n", appURL)
	return nil
}

// signupPassword resolves the account password: generated, piped, or asked
// for twice on a hidden prompt.
func signupPassword(cmd *cobra.Command, o signupOpts, interactive bool) (string, error) {
	rules := "8+ characters with a digit, upper and lower case, and one of " + signupSpecials
	switch {
	case o.generatePassword:
		return generatePassword()
	case o.passwordStdin:
		b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 4096))
		if err != nil {
			return "", err
		}
		pw := strings.TrimRight(string(b), "\r\n")
		if !validPassword(pw) {
			return "", clierr.New("usage_invalid_args", "the password needs "+rules)
		}
		return pw, nil
	case !interactive:
		return "", clierr.New("usage_missing_flag", "a password is required: pass --generate-password or --password-stdin").
			WithHint("Or run 'bronto signup' in a terminal to be prompted.")
	}
	errw := cmd.ErrOrStderr()
	_, _ = fmt.Fprintln(errw, "Choose a password: "+rules)
	for {
		_, _ = fmt.Fprint(errw, "Password: ")
		b1, err := readPassword(int(os.Stdin.Fd()))
		_, _ = fmt.Fprintln(errw)
		if err != nil {
			return "", clierr.New("usage_missing_flag", "no password given")
		}
		if !validPassword(string(b1)) {
			_, _ = fmt.Fprintln(errw, "That doesn't meet the rules. Try again.")
			continue
		}
		_, _ = fmt.Fprint(errw, "Repeat password: ")
		b2, err := readPassword(int(os.Stdin.Fd()))
		_, _ = fmt.Fprintln(errw)
		if err != nil {
			return "", clierr.New("usage_missing_flag", "no password given")
		}
		if string(b1) == string(b2) {
			return string(b1), nil
		}
		_, _ = fmt.Fprintln(errw, "The two don't match. Try again.")
	}
}

func validPassword(pw string) bool {
	return len(pw) >= 8 && strings.ContainsAny(pw, "0123456789") &&
		strings.ContainsAny(pw, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") &&
		strings.ContainsAny(pw, "abcdefghijklmnopqrstuvwxyz") &&
		strings.ContainsAny(pw, signupSpecials)
}

// generatePassword returns 20 random characters that meet the rules. The
// specials are the shell-safe subset, so the password survives copy-paste.
func generatePassword() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!#%*,.:?@^"
	for {
		b := make([]byte, 20)
		for i := range b {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", err
			}
			b[i] = alphabet[n.Int64()]
		}
		if validPassword(string(b)) {
			return string(b), nil
		}
	}
}

// signupWait polls sign-in until the email is verified and returns the
// IdToken. Cognito answers UserNotConfirmedException until then.
func signupWait(ctx context.Context, app *App, cg *cognitoClient, o signupOpts, region, password string) (string, error) {
	errw := app.Stderr
	start := time.Now()
	tty := stderrIsTTY()
	waiting := false
	for {
		token, err := cg.signIn(ctx, o.email, password)
		if err == nil {
			if waiting && tty {
				_, _ = fmt.Fprint(errw, "\r\033[K")
			}
			if waiting {
				_, _ = fmt.Fprintln(errw, "Email verified.")
			}
			return token, nil
		}
		switch {
		case isCognitoErr(err, "UserNotConfirmedException"):
		case isCognitoErr(err, "NotAuthorizedException"), isCognitoErr(err, "UserNotFoundException"):
			return "", clierr.New("signup_failed", "could not sign in as "+o.email+": wrong password").
				WithHint("Use the password the account was created with, or reset it at " + signupAppURL(app, region) + ".")
		default:
			return "", signupErr(err)
		}
		if !waiting {
			waiting = true
			_, _ = fmt.Fprintf(errw, "\nCheck your email: we sent a verification link to %s.\n"+
				"Click it and this command continues by itself (it waits up to %s).\n"+
				"No browser at hand? In another terminal: bronto signup confirm '<link from the email>'\n\n", o.email, shortDuration(o.wait))
		}
		elapsed := time.Since(start).Round(time.Second)
		if elapsed >= o.wait {
			if tty {
				_, _ = fmt.Fprintln(errw)
			}
			return "", clierr.New("signup_wait_timeout", "the email was not verified within "+shortDuration(o.wait)).
				WithHint("The account exists. After clicking the link, run the same command again with the same password to finish.")
		}
		if tty {
			_, _ = fmt.Fprintf(errw, "\r\033[KWaiting for the link to be clicked... %s", elapsed)
		} else if s := int(elapsed.Seconds()); s > 0 && s%60 < int(signupPollInterval.Seconds()) {
			_, _ = fmt.Fprintf(errw, "Still waiting for the email verification (%s)...\n", elapsed)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(signupPollInterval):
		}
	}
}

// shortDuration prints 30m0s as 30m and 1h0m0s as 1h.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

// signupCreateKey creates the CLI's API key with the signed-in user's
// token. The three roles are all there are; the API silently accepts
// unknown role names, so they are spelled out here.
func signupCreateKey(ctx context.Context, baseURL, idToken string) (string, error) {
	host, _ := os.Hostname()
	if host == "" {
		host = "cli"
	}
	body, _ := json.Marshal(map[string]any{
		"name":  "bronto-cli-" + host,
		"roles": []string{"IngestionApi", "SearchApi", "AdminApi"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api-keys", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+idToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", api.UserAgent(version.Version))
	resp, err := api.Do(&http.Client{Timeout: 30 * time.Second}, req)
	if err != nil {
		return "", err
	}
	var out struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil || out.APIKey == "" {
		return "", clierr.New("api_unexpected_response", "the API created a key but did not return its value").
			WithHint("Create a key in the web app and run 'bronto auth login'.")
	}
	return out.APIKey, nil
}

// signupCheckKey is the 'auth login' credential check, retried for a few
// seconds while the new key is still being rejected.
func signupCheckKey(ctx context.Context, c regionCandidate, key string) error {
	deadline := time.Now().Add(signupCheckWait)
	for {
		err := probeRegion(ctx, c, key)
		if err == nil || !isAuthRejection(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func signupAppURL(app *App, region string) string {
	if v, ok := app.Config.Get("app_url"); ok && v.Val != "" {
		return strings.TrimRight(v.Val, "/")
	}
	return "https://app." + region + ".bronto.io"
}

// signupErr turns a Cognito service error into a CLI error. Cognito's
// messages are written for end users ("Password did not conform with
// policy: ..."), so they pass through.
func signupErr(err error) error {
	var ce *cognitoError
	if !errors.As(err, &ce) {
		return err
	}
	e := clierr.New("signup_failed", ce.Message+" ("+ce.Type+")")
	switch ce.Type {
	case "CodeMismatchException", "ExpiredCodeException":
		e = e.WithHint("Use the newest verification email: pass its whole link, in quotes.")
	case "TooManyRequestsException", "LimitExceededException":
		e = e.WithRetryable().WithHint("Wait a minute and try again.")
	}
	return e
}

// printBox draws a rounded box around lines.
func printBox(w io.Writer, lines []string) {
	width := 0
	for _, l := range lines {
		width = max(width, utf8.RuneCountInString(l))
	}
	_, _ = fmt.Fprintln(w, "╭"+strings.Repeat("─", width+4)+"╮")
	for _, l := range lines {
		_, _ = fmt.Fprintln(w, "│  "+l+strings.Repeat(" ", width-utf8.RuneCountInString(l))+"  │")
	}
	_, _ = fmt.Fprintln(w, "╰"+strings.Repeat("─", width+4)+"╯")
}

var appHostRe = regexp.MustCompile(`^app\.(eu|us)\.bronto\.io$`)

func newSignupConfirmCmd() *cobra.Command {
	var email string
	cmd := &cobra.Command{
		Use:   "confirm <link-or-code>",
		Short: "Verify a sign-up email from the terminal (experimental prototype)",
		Long: "EXPERIMENTAL PROTOTYPE.\n\n" +
			"Does what clicking the link in the verification email does. Pass the whole\n" +
			"link (in quotes); the email address and region are read from it. With only\n" +
			"the confirmation code, pass --email and --region too.\n\n" +
			"A 'bronto signup' that is waiting for this account continues by itself.",
		Example: "  bronto signup confirm 'https://app.eu.bronto.io/verify?confirmation_code=123456&email=ada@example.com'\n" +
			"  bronto signup confirm 123456 --email ada@example.com --region eu",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(cmd, appOptions{allowNewProfile: true})
			if err != nil {
				return err
			}
			code, region := strings.TrimSpace(args[0]), ""
			if strings.Contains(code, "://") {
				u, err := url.Parse(code)
				if err != nil || u.Query().Get("confirmation_code") == "" {
					return clierr.New("usage_invalid_args", "that link has no confirmation_code in it").
						WithHint("Copy the whole link from the verification email, in quotes.")
				}
				code = u.Query().Get("confirmation_code")
				if email == "" {
					// A '+' in an unescaped address decodes as a space.
					email = strings.ReplaceAll(u.Query().Get("email"), " ", "+")
				}
				if m := appHostRe.FindStringSubmatch(u.Hostname()); m != nil {
					region = m[1]
				}
			}
			if f := cmd.Flags().Lookup("region"); f != nil && f.Changed {
				region = f.Value.String()
			}
			if _, ok := signupRegions[region]; !ok || email == "" {
				return clierr.New("usage_missing_flag", "the email address and region are needed to verify a code").
					WithHint("Pass the whole link from the email, or add --email <address> --region eu|us.")
			}
			cg := newCognitoClient(region)
			err = cg.call(cmd.Context(), "ConfirmSignUp", map[string]any{
				"ClientId": cg.region.clientID, "Username": email, "ConfirmationCode": code,
			}, nil)
			if err != nil {
				return signupErr(err)
			}
			app.Notef("Email verified for %s. A waiting 'bronto signup' continues by itself;\n"+
				"if none is running, run 'bronto signup --email %s' again to finish.\n", email, email)
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "the email address being verified (read from the link when given one)")
	return cmd
}
