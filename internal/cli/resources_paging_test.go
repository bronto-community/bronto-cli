package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func errorHint(err error) string {
	var ce *clierr.Error
	if errors.As(err, &ce) {
		return ce.Hint
	}
	return ""
}

// encryptionKeysServer serves n encryption keys, failing the test if the
// list is requested without the API's maximum page size.
func encryptionKeysServer(t *testing.T, n int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/encryption-keys" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("limit"); got != "100" {
			t.Errorf("limit = %q, want 100 (the API defaults to 50)", got)
		}
		keys := make([]map[string]any, n)
		for i := range keys {
			keys[i] = map[string]any{"id": fmt.Sprintf("k%03d", i), "name": fmt.Sprintf("key-%03d", i)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"encryption_keys": keys})
	}
}

func TestEncryptionKeysListRequestsMaxPage(t *testing.T) {
	out, stderr, err := runResource(t, encryptionKeysServer(t, 3), "", "encryption-keys", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 3 {
		t.Fatalf("rows = %v (%v)", rows, err)
	}
	if strings.Contains(stderr, "Warning") {
		t.Fatalf("a short page must not warn: %q", stderr)
	}
}

func TestEncryptionKeysListWarnsOnFullPage(t *testing.T) {
	_, stderr, err := runResource(t, encryptionKeysServer(t, 100), "", "encryption-keys", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "maximum of 100 encryption keys") {
		t.Fatalf("stderr = %q, want a truncation warning", stderr)
	}
}

func TestEncryptionKeysResolveMissOnFullPageSaysSo(t *testing.T) {
	_, _, err := runResource(t, encryptionKeysServer(t, 100), "", "encryption-keys", "get", "key-999")
	if err == nil || !strings.Contains(errorHint(err), "first 100 encryption keys") {
		t.Fatalf("err = %v (hint %q), want a truncation hint", err, errorHint(err))
	}
	// A key on the page still resolves.
	if _, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/encryption-keys/k042" {
			_, _ = w.Write([]byte(`{"id":"k042","name":"key-042"}`))
			return
		}
		encryptionKeysServer(t, 100)(w, r)
	}, "", "encryption-keys", "get", "key-042"); err != nil {
		t.Fatal(err)
	}
}
