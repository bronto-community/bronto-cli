package cli

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/bronto-community/bronto-cli/internal/secrets"
	"github.com/bronto-community/bronto-cli/internal/tools/signupmock/fake"
)

// TestSRPSignatureVector pins the Go SRP math to a vector computed with
// the Python reference that was proven against production Cognito (same
// fixed inputs, run through its logic offline).
func TestSRPSignatureVector(t *testing.T) {
	hexInt := func(s string) *big.Int {
		n, ok := new(big.Int).SetString(s, 16)
		if !ok {
			t.Fatal("bad hex")
		}
		return n
	}
	a := hexInt(strings.Repeat("1f2e3d4c5b6a79880123456789abcdef", 8))
	B := new(big.Int).Exp(srpG, hexInt(strings.Repeat("0fedcba9876543210a1b2c3d4e5f6071", 8)), srpN)
	if got := srpA(a).Text(16); !strings.HasPrefix(got, "ad3d539e52b1459c") {
		t.Fatalf("A = %s...", got[:16])
	}
	if got := B.Text(16); !strings.HasPrefix(got, "bf5004c40ec777f7") {
		t.Fatalf("B = %s...", got[:16])
	}
	sig, err := srpSignature("TestPool1", "3f6c1a2e-0000-4000-8000-00000000abcd", "Correct-Horse-9!",
		"b6f0a1c2d3e4f5061728394a5b6c7d8", // odd length: exercises the padding
		"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+Pw==",
		"Thu Nov 26 09:05:03 UTC 2026", a, B)
	if err != nil {
		t.Fatal(err)
	}
	if want := "894V+oLab0UEA62xuVBfDAAP69s+uTL8NTqMDhV9fFg="; sig != want {
		t.Fatalf("signature = %s, want %s", sig, want)
	}
}

// TestSignupThenConfirm runs 'bronto signup' with every flag (so it may
// not prompt) against the fake backend, verifies the email with 'bronto
// signup confirm' while it waits, and checks the key landed in the profile.
func TestSignupThenConfirm(t *testing.T) {
	keyring.MockInit()
	backend := fake.New()
	srv := httptest.NewServer(backend)
	defer srv.Close()
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	t.Setenv(cognitoURLEnv, srv.URL)
	t.Setenv("BRONTO_API_KEY", "")
	oldPoll, oldTTY := signupPollInterval, stdinIsTTY
	signupPollInterval, stdinIsTTY = 20*time.Millisecond, func() bool { return false }
	defer func() { signupPollInterval, stdinIsTTY = oldPoll, oldTTY }()

	var out, errOut bytes.Buffer
	done := make(chan error, 1)
	go func() {
		root := NewRootCmd()
		root.SetOut(&out)
		root.SetErr(&errOut)
		root.SetIn(strings.NewReader(""))
		root.SetArgs([]string{"signup", "--email", "ada@example.com", "--first-name", "Ada", "--last-name", "Lovelace",
			"--org", "Analytical Engines", "--region", "eu", "--accept-terms", "--generate-password",
			"--wait", "20s", "--base-url", srv.URL})
		done <- root.Execute()
	}()

	// The "inbox" has the email once SignUp went through.
	var mails []struct{ Link string }
	for i := 0; len(mails) == 0; i++ {
		if i > 200 {
			t.Fatal("no verification email")
		}
		time.Sleep(10 * time.Millisecond)
		resp, err := http.Get(srv.URL + "/__signup/inbox?email=ada@example.com")
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewDecoder(resp.Body).Decode(&mails)
		_ = resp.Body.Close()
	}
	select {
	case err := <-done:
		t.Fatalf("signup finished before the email was verified: %v\n%s", err, errOut.String())
	case <-time.After(100 * time.Millisecond): // a few polls answered UserNotConfirmedException
	}

	confirm := NewRootCmd()
	confirm.SetOut(&bytes.Buffer{})
	confirm.SetErr(&bytes.Buffer{})
	confirm.SetArgs([]string{"signup", "confirm", mails[0].Link})
	if err := confirm.Execute(); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("signup: %v\n%s", err, errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("signup did not finish after the email was verified")
	}
	if key, _, err := secrets.Get("default"); err != nil || key != fake.APIKey {
		t.Fatalf("stored key = %q, %v", key, err)
	}
	if names := backend.KeyNames(); len(names) != 1 || !strings.HasPrefix(names[0], "bronto-cli-") {
		t.Fatalf("keys created = %v", names)
	}
	if !strings.Contains(out.String(), "Welcome to Bronto, Ada!") {
		t.Fatalf("stdout = %q", out.String())
	}
	for _, want := range []string{"Generated password (SECRET", "Check your email", "Email verified."} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut.String())
		}
	}
	if all := out.String() + errOut.String(); strings.Contains(all, fake.APIKey) || strings.Contains(all, "fake.id.token") {
		t.Fatalf("output leaks the key or token:\n%s", all)
	}
}
