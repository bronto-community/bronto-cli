package cli

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// --- --jq must be rejected BEFORE a mutation runs -------------------------

// TestJQWithoutOutputOnTTYRejectedBeforeMutation pins that a --jq/-o
// mismatch fails up front. With --jq and no -o on a TTY, the effective
// format is table (incompatible with --jq), but the rejection used to
// happen in PrinterFor — AFTER doJSONRequest had already executed the
// mutation, dropping the response (including a created resource's id).
// 2026-07-23 audit.
func TestJQWithoutOutputOnTTYRejectedBeforeMutation(t *testing.T) {
	old := stdoutIsTTY
	stdoutIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY = old })

	_, _, err := runResource(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("server must not be contacted: the --jq/-o mismatch must be rejected before the mutation")
	}, "", "monitors", "create", "-f", "name=x", "--jq", ".id")
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage error exit 2, got %v", err)
	}
}

// TestJQWithoutOutputOnPipeStillWorks guards against over-rejecting: piped
// (non-TTY) with no -o resolves to json/jsonl, which --jq accepts.
func TestJQWithoutOutputOnPipeStillWorks(t *testing.T) {
	// default stdoutIsTTY stub is non-TTY under `go test`.
	_, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"a"},{"name":"b"}]`))
	}, "", "monitors", "list", "--jq", ".name")
	if err != nil {
		t.Fatalf("--jq with no -o on a pipe must work (json default), got %v", err)
	}
}

// --- api-keys list must mask key material by default ----------------------

const fakeAPIKey = "supersecretkeymaterial1234567890"

func apiKeysListHandler(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`[{"name":"ci","api_key":"` + fakeAPIKey + `","id":"k1"}]`))
}

// TestApiKeysListMasksSecretInJSON pins that json/jsonl (the piped/CI
// default) mask key material like table/csv already do — otherwise
// `api-keys list` in a pipeline writes every account key into build logs.
// 2026-07-23 audit.
func TestApiKeysListMasksSecretInJSON(t *testing.T) {
	for _, format := range []string{"json", "jsonl"} {
		out, _, err := runResource(t, apiKeysListHandler, "", "api-keys", "list", "-o", format)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if strings.Contains(out, fakeAPIKey) {
			t.Errorf("%s: full api key leaked into output: %s", format, out)
		}
		if !strings.Contains(out, "…") {
			t.Errorf("%s: expected a masked key, got %s", format, out)
		}
	}
}

// TestApiKeysListRevealShowsFullSecret pins the opt-out: --reveal prints
// full key material for the cases that genuinely need it.
func TestApiKeysListRevealShowsFullSecret(t *testing.T) {
	out, _, err := runResource(t, apiKeysListHandler, "", "api-keys", "list", "--reveal", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, fakeAPIKey) {
		t.Fatalf("--reveal must show the full key, got %s", out)
	}
}

// TestRevealFlagScopedToSecretResources pins that --reveal is not a global
// flag — a resource without secret material (monitors) doesn't get it.
func TestRevealFlagScopedToSecretResources(t *testing.T) {
	_, _, err := runResource(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("server must not be contacted for an unknown-flag usage error")
	}, "", "monitors", "list", "--reveal", "-o", "json")
	if err == nil {
		t.Fatal("expected an unknown-flag error for monitors list --reveal")
	}
}
