package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// percentile(x, 95) comes back from the live API as a bare 500 (verified
// 2026-10-21); the CLI names the cause from the request, not the error.
func TestSearchPercentileOverOneHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"Internal Server Error"}`))
	}))
	defer srv.Close()
	for _, tc := range []struct {
		sel      string
		wantHint bool
	}{
		{"percentile($span.duration_nano, 95)", true},
		{"PERCENTILE(x,99.9)", true},
		{"percentile(x, 0.95)", false},
		{"p95(x)", false},
	} {
		root := NewRootCmd()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"search", "-d", "11111111-1111-1111-1111-111111111111",
			"--base-url", srv.URL, "--api-key", "k", "-o", "json", "--max-retries", "0", "--select", tc.sel})
		err := root.Execute()
		var ce *clierr.Error
		if !errors.As(err, &ce) {
			t.Fatalf("%s: want a clierr, got %v", tc.sel, err)
		}
		if got := strings.Contains(ce.Hint, "p95(x)"); got != tc.wantHint {
			t.Errorf("%s: hint = %q, want hint %v", tc.sel, ce.Hint, tc.wantHint)
		}
		if tc.wantHint && !strings.Contains(ce.Hint, tc.sel) {
			t.Errorf("%s: hint should quote the offending call: %q", tc.sel, ce.Hint)
		}
	}
}

// A group key with a digit segment fails unquoted ("Invalid query."), so
// search sends it double-quoted.
func TestSearchQuotesDigitGroupKeys(t *testing.T) {
	body := searchBody(t, `{"groups":[]}`, "-g", "$span.events.0.exception.type", "-g", "$service.name")
	got, _ := json.Marshal(body["groups"])
	if string(got) != `["\"$span.events.0.exception.type\"","$service.name"]` {
		t.Fatalf("groups = %s", got)
	}
}
