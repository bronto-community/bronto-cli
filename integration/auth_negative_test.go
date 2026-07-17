package integration

import (
	"os"
	"testing"
)

// TestAuthNegative_IngestionKeyOnReadEndpoint live-verifies the 403
// ingestion-key-hint contract from internal/api/transport.go
// (ErrorFromStatus): an ingestion-only key hitting a management read
// endpoint must come back as auth_insufficient_role (exit 3).
//
// The CLI's error rendering depends on whether stderr itself is a
// terminal, not on any -o/--output flag (cmd/bronto/main.go), and an
// os/exec-captured subprocess's stderr is never a terminal — so the
// error always renders as the machine JSON envelope here. That envelope
// carries {code, message, retryable} but NOT the human-readable Hint
// text ("You are likely using an ingestion key...") — Render only prints
// Hint in non-machine mode. So the live assertion is on the envelope's
// stable `code` field, not a substring match against hint text.
func TestAuthNegative_IngestionKeyOnReadEndpoint(t *testing.T) {
	skipIfNoCreds(t)
	ingestKey := os.Getenv("BRONTO_IT_INGEST_KEY")
	if ingestKey == "" {
		t.Skip("BRONTO_IT_INGEST_KEY not set; skipping ingestion-key negative test")
	}

	r := NewRunner(t, ingestKey)
	res, err := r.Run(t.Context(), "", "datasets", "list")
	if err != nil {
		t.Fatalf("running datasets list: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("datasets list with an ingestion key exited %d, want 3\nstdout: %s\nstderr: %s",
			res.ExitCode, res.Stdout, res.Stderr)
	}
	env := ParseErrorEnvelope(t, res.Stderr)
	if env.Error.Code != "auth_insufficient_role" {
		t.Fatalf("error code = %q, want %q\nstderr: %s", env.Error.Code, "auth_insufficient_role", res.Stderr)
	}
}

// TestAuthNegative_CorruptedManagementKey asserts the 401 path: a
// syntactically-plausible but wrong management key against a live region
// must come back as auth_invalid_key (exit 3), never a network_error or a
// silent success.
func TestAuthNegative_CorruptedManagementKey(t *testing.T) {
	skipIfNoCreds(t)

	r := NewRunner(t, "corrupted-not-a-real-management-key")
	res, err := r.Run(t.Context(), "", "ping")
	if err != nil {
		t.Fatalf("running ping: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("ping with a corrupted key exited %d, want 3\nstdout: %s\nstderr: %s",
			res.ExitCode, res.Stdout, res.Stderr)
	}
	env := ParseErrorEnvelope(t, res.Stderr)
	if env.Error.Code != "auth_invalid_key" {
		t.Fatalf("error code = %q, want %q\nstderr: %s", env.Error.Code, "auth_invalid_key", res.Stderr)
	}
}
