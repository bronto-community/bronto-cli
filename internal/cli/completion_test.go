package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// cobraBuiltin reports whether a command is one cobra adds itself (help,
// completion, the hidden __complete pair) — not part of bronto's surface and
// not swept by applyCompletions.
func cobraBuiltin(name string) bool {
	switch name {
	case "help", "completion", "__complete", "__completeNoDesc":
		return true
	}
	return false
}

// walkCommands visits every command in the tree except cobra builtins.
func walkCommands(root *cobra.Command, fn func(*cobra.Command)) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if cobraBuiltin(c.Name()) {
			return
		}
		fn(c)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// TestEveryLeafHasArgCompletion is the tripwire behind the applyCompletions
// sweep: every runnable leaf command must set a ValidArgsFunction, so a
// positional never silently falls back to cobra's file completion. A new
// command that forgets one fails here instead of showing files in the wild.
func TestEveryLeafHasArgCompletion(t *testing.T) {
	walkCommands(NewRootCmd(), func(c *cobra.Command) {
		if c.Runnable() && !c.HasSubCommands() && c.ValidArgsFunction == nil {
			t.Errorf("%q is a runnable leaf with no ValidArgsFunction — it will fall back to file completion", c.CommandPath())
		}
	})
}

// TestIntendedFlagsHaveCompletion pins that every flag we mean to complete
// actually has a completion func registered (on the command that owns it), so
// a renamed flag or a missed registration is caught in CI.
func TestIntendedFlagsHaveCompletion(t *testing.T) {
	shouldComplete := map[string]bool{
		"dataset": true, "select": true, "group-by": true, "saved": true,
		"since": true, "window": true, "collection": true,
		"output": true, "region": true, "profile": true, "fields": true,
		"direction": true,
		"eq":        true, "ne": true, "gt": true, "ge": true, "lt": true, "le": true, "match": true, "nmatch": true,
	}
	walkCommands(NewRootCmd(), func(c *cobra.Command) {
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if !shouldComplete[f.Name] {
				return
			}
			if _, ok := c.GetFlagCompletionFunc(f.Name); !ok {
				t.Errorf("%s: --%s should have a completion func but none is registered", c.CommandPath(), f.Name)
			}
		})
	})
}

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
	// The query positional must NOT fall back to file completion. It now
	// offers flag hints too (KeepOrder), so assert the NoFileComp bit is set
	// rather than an exact directive (see TestFlagHintsOnEmptyPositional).
	_, dir := runComplete(t, noAPI(t), "search", "")
	if !strings.HasPrefix(dir, ":") {
		t.Fatalf("no directive: %q", dir)
	}
	var n int
	if _, err := fmt.Sscanf(dir, ":%d", &n); err != nil {
		t.Fatalf("bad directive %q: %v", dir, err)
	}
	if n&4 == 0 { // ShellCompDirectiveNoFileComp
		t.Fatalf("NoFileComp not set in directive %q", dir)
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

func TestFlagHintsOnEmptyPositional(t *testing.T) {
	// "bronto search <tab>" hints the command's flags, most-useful first,
	// with --dataset starred (no default_dataset configured) and no files.
	cands, dir := runComplete(t, noAPI(t), "search", "")
	if dir != ":36" { // NoFileComp|KeepOrder
		t.Fatalf("directive = %q", dir)
	}
	if len(cands) == 0 || !strings.HasPrefix(cands[0], "--dataset\t") {
		t.Fatalf("first hint = %v", cands)
	}
	if !strings.Contains(cands[0], "★") || !strings.Contains(cands[0], "no default dataset") {
		t.Fatalf("dataset hint not starred: %q", cands[0])
	}
	// help must not appear as a hint
	for _, c := range cands {
		if strings.HasPrefix(c, "--help") {
			t.Fatalf("--help leaked into hints: %v", cands)
		}
	}
}

func TestCompleteSince(t *testing.T) {
	cands, dir := runComplete(t, noAPI(t), "search", "--since", "")
	if dir != ":36" { // NoFileComp|KeepOrder
		t.Fatalf("directive = %q", dir)
	}
	if len(cands) == 0 || !strings.HasPrefix(cands[0], "15m\t") {
		t.Fatalf("since cands = %v", cands)
	}
}

func TestCompleteConfigKeys(t *testing.T) {
	cands, _ := runComplete(t, noAPI(t), "config", "get", "")
	joined := strings.Join(cands, " ")
	for _, k := range []string{"region", "output", "default_dataset", "ask_url"} {
		if !strings.Contains(joined, k) {
			t.Errorf("missing config key %q in %v", k, cands)
		}
	}
}

func TestCompleteDatasetsSmallAccountFlat(t *testing.T) {
	h := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"logs":[
			{"log":"api","collection":"prod","log_id":"11111111-1111-1111-1111-111111111111"},
			{"log":"web","collection":"prod","log_id":"22222222-2222-2222-2222-222222222222"}]}`))
	}
	cands, _ := runComplete(t, h, "search", "-d", "")
	// small account: flat names, not "collection/"
	if len(cands) != 2 || !strings.HasPrefix(cands[0], "api\t") {
		t.Fatalf("flat cands = %v", cands)
	}
}

func TestCompleteDatasetsLargeAccountCollectionsFirst(t *testing.T) {
	// > threshold datasets across two collections -> collections first.
	h := func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString(`{"logs":[`)
		for i := 0; i < datasetCompletionThreshold+5; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			coll := "alpha"
			if i%2 == 0 {
				coll = "beta"
			}
			fmt.Fprintf(&b, `{"log":"ds%d","collection":"%s","log_id":"1111%04d-1111-1111-1111-111111111111"}`, i, coll, i)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	}
	// no slash: collections, NoSpace so the next tab drills in
	cands, dir := runComplete(t, h, "search", "-d", "")
	if dir != ":6" { // NoFileComp|NoSpace
		t.Fatalf("directive = %q", dir)
	}
	if len(cands) != 2 || cands[0] != "alpha/\tcollection" || cands[1] != "beta/\tcollection" {
		t.Fatalf("collection cands = %v", cands)
	}
	// with a collection prefix: that collection's datasets, qualified
	sub, _ := runComplete(t, h, "search", "-d", "alpha/")
	if len(sub) == 0 || !strings.HasPrefix(sub[0], "alpha/ds") {
		t.Fatalf("drill-in cands = %v", sub)
	}
}

func TestCompleteDatasetsGetUsesDatasetCompleter(t *testing.T) {
	// `datasets get <tab>` must complete real datasets (rows key name/id as
	// log/log_id), not the generic name/id path which finds nothing.
	h := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/logs" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"logs":[
			{"log":"api","collection":"prod","log_id":"11111111-1111-1111-1111-111111111111"},
			{"log":"web","collection":"prod","log_id":"22222222-2222-2222-2222-222222222222"}]}`))
	}
	cands, dir := runComplete(t, h, "datasets", "get", "")
	if dir != ":4" {
		t.Fatalf("directive = %q", dir)
	}
	if len(cands) != 2 || !strings.HasPrefix(cands[0], "api\t") {
		t.Fatalf("datasets-get cands = %v", cands)
	}
}

func TestCompleteResourceIDFallback(t *testing.T) {
	// limits have no unique name; NameKeys=category. Rows complete as
	// category<TAB>id, and a row with neither falls back to the bare id.
	h := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"category":"INGESTION","id":"aaaa1111-0000-0000-0000-000000000001"},
			{"id":"aaaa1111-0000-0000-0000-000000000002"}]`))
	}
	cands, dir := runComplete(t, h, "limits", "get", "")
	if dir != ":4" {
		t.Fatalf("directive = %q", dir)
	}
	joined := strings.Join(cands, " ")
	if !strings.Contains(joined, "INGESTION\taaaa1111-0000-0000-0000-000000000001") {
		t.Fatalf("no category completion: %v", cands)
	}
	if !strings.Contains(joined, "aaaa1111-0000-0000-0000-000000000002") {
		t.Fatalf("no id fallback: %v", cands)
	}
}

func TestCompleteFieldsPositionalFallsBackToHints(t *testing.T) {
	// `bronto fields <tab>` with no -d must hint flags (not go silent).
	cands, dir := runComplete(t, noAPI(t), "fields", "")
	if dir != ":36" { // NoFileComp|KeepOrder (flag hints)
		t.Fatalf("directive = %q", dir)
	}
	if len(cands) == 0 || !strings.HasPrefix(cands[0], "--dataset\t") {
		t.Fatalf("fields hints = %v", cands)
	}
}

func TestCompleteAPIMethodThenPath(t *testing.T) {
	// `bronto api <tab>` → HTTP methods (GET first); `api GET <tab>` → paths.
	methods, dir := runComplete(t, noAPI(t), "api", "")
	if dir != ":36" { // NoFileComp|KeepOrder
		t.Fatalf("method directive = %q", dir)
	}
	if len(methods) == 0 || methods[0] != "GET" {
		t.Fatalf("methods = %v", methods)
	}
	paths, dir := runComplete(t, noAPI(t), "api", "GET", "")
	if dir != ":4" {
		t.Fatalf("path directive = %q", dir)
	}
	joined := strings.Join(paths, " ")
	if !strings.Contains(joined, "/monitors") || !strings.Contains(joined, "/logs") {
		t.Fatalf("path hints = %v", paths)
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
