package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
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

func TestReadBodyInputFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "body.json")
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	got, err := readBodyInput(cmd, path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("got %q", got)
	}
}

func TestReadBodyInputFromStdin(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("piped body"))
	got, err := readBodyInput(cmd, "-")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "piped body" {
		t.Fatalf("got %q", got)
	}
}

func TestReadBodyInputMissingFileIsUsageError(t *testing.T) {
	cmd := &cobra.Command{}
	_, err := readBodyInput(cmd, filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("want error for a missing file")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_input_file" {
		t.Fatalf("want usage_input_file clierr.Error, got %T: %v", err, err)
	}
	if clierr.ExitCode(err) != 2 {
		t.Fatalf("exit = %d, want 2", clierr.ExitCode(err))
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

func TestAPIInt64Fidelity(t *testing.T) {
	// Found live 2026-07-22: `bronto api POST /search` printed
	// metadata.sequence 4244404144331292672 as ...292700 — plain
	// json.Unmarshal rounds >2^53 integers through float64. Both
	// directions must be byte-exact.
	const bigID = "4244404144331292672"

	t.Run("response", func(t *testing.T) {
		out, err := runAPI(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"metadata":{"sequence":` + bigID + `}}`))
		}, "GET", "/search")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, bigID) {
			t.Fatalf("sequence corrupted in output:\n%s", out)
		}
	})

	t.Run("request body from -f", func(t *testing.T) {
		var gotBody []byte
		_, err := runAPI(t, func(w http.ResponseWriter, r *http.Request) {
			gotBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{}`))
		}, "POST", "/things", "-f", "seq="+bigID)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(gotBody), bigID) {
			t.Fatalf("sequence corrupted on the wire: %s", gotBody)
		}
	})
}

// TestParseFieldArgsRejectsEmptyAndDuplicateKeys pins that -f '=v' and a
// key given twice are usage errors, not {"": "v"} or a silent last-wins.
func TestParseFieldArgsRejectsEmptyAndDuplicateKeys(t *testing.T) {
	for _, fields := range [][]string{{"=v"}, {"name=a", "name=b"}} {
		_, err := parseFieldArgs(fields)
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != "usage_invalid_field" || clierr.ExitCode(err) != 2 {
			t.Errorf("%v: want usage_invalid_field, got %v", fields, err)
		}
	}
	if _, err := parseFieldArgs([]string{"a=1", "b=x=y"}); err != nil {
		t.Fatalf("valid fields rejected: %v", err)
	}
	// Query parameters may repeat (?tag=a&tag=b), but an empty key still fails.
	var gotQuery string
	if _, err := runAPI(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{}`))
	}, "GET", "/x", "-f", "tag=a", "-f", "tag=b"); err != nil || gotQuery != "tag=a&tag=b" {
		t.Fatalf("repeated query keys: query=%q err=%v", gotQuery, err)
	}
	if _, err := runAPI(t, func(http.ResponseWriter, *http.Request) {
		t.Error("server contacted")
	}, "GET", "/x", "-f", "=v"); clierr.ExitCode(err) != 2 {
		t.Fatalf("empty query key: want exit 2, got %v", err)
	}
}

// TestAPIInputValidatesJSONBodies pins that a JSON --input must be an
// object or array: invalid or empty input fails client-side (exit 2)
// instead of reaching the API as a raw string or a body-less request.
func TestAPIInputValidatesJSONBodies(t *testing.T) {
	for _, body := range []string{"", "  \n", "{not json", `"a string"`, "42"} {
		root := NewRootCmd()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetIn(strings.NewReader(body))
		root.SetArgs([]string{"api", "POST", "/search", "--input", "-", "--base-url", "http://127.0.0.1:1", "--api-key", "k"})
		err := root.Execute()
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != "usage_invalid_body" || clierr.ExitCode(err) != 2 {
			t.Errorf("body %q: want usage_invalid_body exit 2, got %v", body, err)
		}
	}
	for ct, want := range map[string]bool{
		"application/json": true, "Application/JSON; charset=utf-8": true,
		"application/merge-patch+json": true, "text/plain": false, "application/x-ndjson": false,
	} {
		if got := isJSONContentType(ct); got != want {
			t.Errorf("isJSONContentType(%q) = %v, want %v", ct, got, want)
		}
	}
}

// TestAPIPrintsBodyVerbatim pins that without --jq/--fields the response
// body prints as the API sent it: key order kept, & < > not escaped, big
// numbers untouched. jsonl compacts it; csv still decodes.
func TestAPIPrintsBodyVerbatim(t *testing.T) {
	body := `{"z":1,"a":"x&y<z>","n":4367602734065516544}`
	handler := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
	out, err := runAPI(t, handler, "GET", "/x")
	if err != nil {
		t.Fatal(err)
	}
	if out != body+"\n" {
		t.Fatalf("json = %q, want the body verbatim", out)
	}
	out, err = runAPI(t, handler, "GET", "/x", "-o", "jsonl")
	if err != nil || out != body+"\n" {
		t.Fatalf("jsonl = %q (%v)", out, err)
	}
	out, err = runAPI(t, handler, "GET", "/x", "-o", "table")
	if err != nil || !strings.HasPrefix(out, "{\n  \"z\": 1,\n  \"a\": \"x&y<z>\"") {
		t.Fatalf("table = %q (%v)", out, err)
	}
	out, err = runAPI(t, handler, "GET", "/x", "-o", "csv")
	if err != nil || out != "a,n,z\nx&y<z>,4367602734065516544,1\n" {
		t.Fatalf("csv = %q (%v)", out, err)
	}
}

// TestCommaListComplete pins the comma-list adapter used by --fields and
// --users/--groups: the segment after the last comma is completed and the
// typed prefix kept on each candidate.
func TestCommaListComplete(t *testing.T) {
	var gotToComplete string
	single := func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		gotToComplete = toComplete
		return []string{"host", "status\tdesc"}, cobra.ShellCompDirectiveNoFileComp
	}
	cands, _ := commaListComplete(single)(nil, nil, "path,st")
	if gotToComplete != "st" {
		t.Fatalf("inner completer got %q, want the last segment", gotToComplete)
	}
	if strings.Join(cands, "|") != "path,host|path,status\tdesc" {
		t.Fatalf("cands = %q", cands)
	}
	cands, _ = commaListComplete(single)(nil, nil, "st")
	if strings.Join(cands, "|") != "host|status\tdesc" {
		t.Fatalf("no-comma cands = %q", cands)
	}
}

// Values that merely start with JSON stay strings.
func TestParseFieldArgsKeepsNonJSONStrings(t *testing.T) {
	got, err := parseFieldArgs([]string{"window=20 minutes", "date=2024-01-01", "v=1.0.0", "n=10", "b=true", `o={"a":1}`})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"window": "20 minutes", "date": "2024-01-01", "v": "1.0.0"} {
		if got[k] != want {
			t.Errorf("%s = %#v, want %q", k, got[k], want)
		}
	}
	if got["n"] != json.Number("10") || got["b"] != true {
		t.Errorf("JSON scalars lost their type: %#v %#v", got["n"], got["b"])
	}
	if o, ok := got["o"].(map[string]any); !ok || o["a"] != json.Number("1") {
		t.Errorf("object = %#v", got["o"])
	}
}
