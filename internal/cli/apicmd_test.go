package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

func runAPI(t *testing.T, handler http.HandlerFunc, args ...string) (string, error) {
	t.Helper()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	full := append([]string{"api"}, args...)
	full = append(full, "--base-url", srv.URL, "--api-key", "k")
	root.SetArgs(full)
	err := root.Execute()
	return out.String(), err
}

func TestAPIGetWithQueryFields(t *testing.T) {
	out, err := runAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/logs" || r.URL.Query().Get("limit") != "5" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		_, _ = w.Write([]byte(`{"logs":[]}`))
	}, "GET", "/logs", "-f", "limit=5")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout not JSON: %v (%q)", err, out)
	}
}

func TestAPIPostBuildsJSONBody(t *testing.T) {
	_, err := runAPI(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatalf("body not JSON: %q", b)
		}
		// limit=10 parses as JSON number; name stays string
		if body["limit"] != float64(10) || body["name"] != "x" {
			t.Errorf("body = %v", body)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing content type")
		}
		_, _ = w.Write([]byte(`{}`))
	}, "POST", "/search", "-f", "limit=10", "-f", "name=x")
	if err != nil {
		t.Fatal(err)
	}
}

func TestAPINon2xxIsTypedError(t *testing.T) {
	_, err := runAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"no such monitor"}`))
	}, "GET", "/monitors/nope")
	if err == nil {
		t.Fatal("want error")
	}
	if clierr.ExitCode(err) != 4 {
		t.Fatalf("exit = %d, want 4", clierr.ExitCode(err))
	}
}

func TestAPIRejectsBadMethod(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"api", "YEET", "/logs", "--api-key", "k"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage error exit 2, got %v (exit %d)", err, clierr.ExitCode(err))
	}
}

func TestAPIInputRespectsContentTypeOverride(t *testing.T) {
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader("plain text payload"))
	root.SetArgs([]string{"api", "POST", "/ingest", "--input", "-",
		"--content-type", "text/plain", "--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotCT != "text/plain" {
		t.Fatalf("Content-Type = %q, want text/plain", gotCT)
	}
}
