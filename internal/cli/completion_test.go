package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runComplete drives cobra's hidden __complete command against a stub server.
// The last element of line is the token being completed. The stub connection
// is passed via env (not flags) so it doesn't perturb flag-value completions
// like "--eq <tab>". Returns the candidate lines and the ":<directive>" line.
func runComplete(t *testing.T, handler http.HandlerFunc, line ...string) (cands []string, directive string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	t.Setenv("BRONTO_BASE_URL", srv.URL)
	t.Setenv("BRONTO_API_KEY", "k")
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"__complete"}, line...))
	if err := root.Execute(); err != nil {
		t.Fatalf("__complete: %v", err)
	}
	for _, ln := range strings.Split(out.String(), "\n") {
		switch {
		case ln == "":
		case strings.HasPrefix(ln, ":"):
			directive = ln
		default:
			cands = append(cands, ln)
		}
	}
	return cands, directive
}

func noAPI(t *testing.T) http.HandlerFunc {
	return func(_ http.ResponseWriter, r *http.Request) { t.Errorf("unexpected API call: %s", r.URL.Path) }
}

func TestCompleteOutputFormat(t *testing.T) {
	cands, dir := runComplete(t, noAPI(t), "search", "-o", "")
	if dir != ":4" { // NoFileComp
		t.Fatalf("directive = %q", dir)
	}
	joined := strings.Join(cands, " ")
	for _, f := range []string{"table", "json", "jsonl", "raw", "csv"} {
		if !strings.Contains(joined, f) {
			t.Errorf("missing format %q in %v", f, cands)
		}
	}
}

func TestCompleteRegion(t *testing.T) {
	cands, _ := runComplete(t, noAPI(t), "search", "--region", "")
	if len(cands) != 2 || !strings.HasPrefix(cands[0], "eu") || !strings.HasPrefix(cands[1], "us") {
		t.Fatalf("region cands = %v", cands)
	}
}

func TestCompleteDatasets(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/logs" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"logs":[
			{"log":"api-logs","collection":"prod","log_id":"11111111-1111-1111-1111-111111111111"},
			{"log":"web","collection":"prod","log_id":"22222222-2222-2222-2222-222222222222"}]}`))
	}
	cands, dir := runComplete(t, h, "search", "-d", "")
	if dir != ":4" {
		t.Fatalf("directive = %q", dir)
	}
	if len(cands) != 2 || !strings.HasPrefix(cands[0], "api-logs\t") {
		t.Fatalf("dataset cands = %v", cands)
	}
}

func TestCompleteResourceNames(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/monitors" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"name":"cpu-high","id":"aaaa1111-0000-0000-0000-000000000001"},
			{"name":"disk-full","id":"aaaa1111-0000-0000-0000-000000000002"}]`))
	}
	cands, dir := runComplete(t, h, "monitors", "get", "")
	if dir != ":4" {
		t.Fatalf("directive = %q", dir)
	}
	// name<TAB>id, sorted
	if len(cands) != 2 || !strings.HasPrefix(cands[0], "cpu-high\taaaa1111") {
		t.Fatalf("resource cands = %v", cands)
	}
}

func TestCompleteSearchPositionalSuppressesFiles(t *testing.T) {
	// The query positional must NOT fall back to file completion.
	_, dir := runComplete(t, noAPI(t), "search", "")
	if dir != ":4" { // NoFileComp
		t.Fatalf("directive = %q (want NoFileComp)", dir)
	}
}

func TestCompleteInputFlagKeepsFileCompletion(t *testing.T) {
	// --input takes a file: it must keep cobra's default (file) completion,
	// i.e. NOT NoFileComp.
	_, dir := runComplete(t, noAPI(t), "monitors", "create", "--input", "")
	if dir == ":4" {
		t.Fatalf("--input should keep file completion, got NoFileComp")
	}
}

func TestCompleteFilterFieldFlag(t *testing.T) {
	// --eq value completes to "<field>=" with NoSpace|NoFileComp (6).
	h := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/logs":
			_, _ = w.Write([]byte(`{"logs":[{"log":"app","collection":"prod","log_id":"11111111-1111-1111-1111-111111111111"}]}`))
		case "/top-keys":
			_, _ = w.Write([]byte(`{"log-a":{"$model":{"type":"STRING","field_type":"ATTRIBUTE"},"$status":{"type":"NUMBER","field_type":"MESSAGE_KVP"}}}`))
		default:
			t.Errorf("path = %s", r.URL.Path)
		}
	}
	cands, dir := runComplete(t, h, "search", "-d", "app", "--eq", "")
	if dir != ":6" { // NoFileComp | NoSpace
		t.Fatalf("directive = %q", dir)
	}
	joined := strings.Join(cands, " ")
	if !strings.Contains(joined, "$model=") || !strings.Contains(joined, "$status=") {
		t.Fatalf("filter cands = %v", cands)
	}
}
