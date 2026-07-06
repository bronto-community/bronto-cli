package clierr

import (
	"bytes"
	"encoding/json"
	"errors"
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
