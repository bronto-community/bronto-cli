package clierr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{"api_error", 1},
		{"usage_invalid_flag", 2},
		{"config_secret_in_project_file", 2},
		{"config_unknown_key", 2},
		{"auth_invalid_key", 3},
		{"auth_insufficient_role", 3},
		{"dataset_not_found", 4},
		{"rate_limited", 5},
		{"timeout", 5},
	}
	for _, c := range cases {
		if got := New(c.code, "x").ExitCode(); got != c.want {
			t.Errorf("ExitCode(%q) = %d, want %d", c.code, got, c.want)
		}
	}
}

func TestExitCodeUnknownError(t *testing.T) {
	if got := ExitCode(errors.New("boom")); got != 1 {
		t.Fatalf("ExitCode(plain error) = %d, want 1", got)
	}
	if got := ExitCode(nil); got != 0 {
		t.Fatalf("ExitCode(nil) = %d, want 0", got)
	}
}

func TestErrorReturnsMessage(t *testing.T) {
	if got := New("some_code", "boom happened").Error(); got != "boom happened" {
		t.Fatalf("Error() = %q, want %q", got, "boom happened")
	}
}

// nilUnwrapError implements error and Unwrap() error (returning nil), but is
// never itself a *Error. It pins asCLIError's unwrap loop: it must follow
// Unwrap once (finding no *Error), then exit the loop cleanly via the
// err != nil loop condition rather than the "no Unwrap method" early return.
type nilUnwrapError struct{ msg string }

func (e nilUnwrapError) Error() string { return e.msg }
func (e nilUnwrapError) Unwrap() error { return nil }

func TestExitCodeUnwrapsChainWithNoTerminalError(t *testing.T) {
	if got := ExitCode(nilUnwrapError{msg: "wrapped, never a *Error"}); got != 1 {
		t.Fatalf("ExitCode = %d, want 1 (falls back to unknown_error mapping)", got)
	}
}

func TestExitCodeFindsWrappedError(t *testing.T) {
	wrapped := fmt.Errorf("context: %w", New("rate_limited", "slow down"))
	if got := ExitCode(wrapped); got != 5 {
		t.Fatalf("ExitCode(wrapped rate_limited) = %d, want 5", got)
	}
}

func TestRenderMachineMode(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, New("rate_limited", "slow down").WithRetryable(), true)
	var env struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, buf.String())
	}
	if env.Error.Code != "rate_limited" || !env.Error.Retryable {
		t.Fatalf("bad envelope: %+v", env)
	}
}

func TestRenderMachineModePlainErrorFallsBackToUnknownError(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, errors.New("boom"), true)
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, buf.String())
	}
	if env.Error.Code != "unknown_error" || env.Error.Message != "boom" {
		t.Fatalf("bad envelope: %+v", env)
	}
}

func TestRenderHumanIncludesHintAndDocs(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, New("auth_insufficient_role", "403 from API").
		WithHint("You are likely using an ingestion key; create a management key.").
		WithDocs("https://docs.bronto.io/api-reference/api-keys/overview"), false)
	out := buf.String()
	for _, want := range []string{"403 from API", "ingestion key", "docs.bronto.io"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q: %q", want, out)
		}
	}
}

func TestRenderMachineEnvelopeIncludesHint(t *testing.T) {
	var buf strings.Builder
	Render(&buf, New("auth_invalid_key", "bad key").WithHint("Run 'bronto auth login'."), true)
	var env map[string]map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &env); err != nil {
		t.Fatal(err)
	}
	if env["error"]["hint"] != "Run 'bronto auth login'." {
		t.Fatalf("envelope = %v", env)
	}

	buf.Reset()
	Render(&buf, New("x", "plain message"), true)
	if strings.Contains(buf.String(), `"hint"`) {
		t.Fatalf("hintless error must omit the field: %s", buf.String())
	}
}
