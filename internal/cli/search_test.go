package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func searchServer(t *testing.T, respond string, capture *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if capture != nil {
			_ = json.Unmarshal(b, capture)
		}
		_, _ = w.Write([]byte(respond))
	}))
}

func TestSearchEventsJSONLWhenPiped(t *testing.T) {
	var body map[string]any
	srv := searchServer(t, `{"events":[{"@raw":"e1","@time":"t1"},{"@raw":"e2","@time":"t2"}]}`, &body)
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "status >= 500", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 jsonl lines, got %d: %q", len(lines), out.String())
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil || ev["@raw"] != "e1" {
		t.Fatalf("line0 = %q", lines[0])
	}
	// request body assertions
	if body["where"] != "status >= 500" || body["time_range"] != "Last 15 minutes" {
		t.Fatalf("body = %v", body)
	}
	sel, _ := body["select"].([]any)
	// "*" pulls the parsed KVs into events; "@raw" stays explicit because
	// a bare "*" nulls it out server-side.
	if len(sel) != 3 || sel[0] != "@time" || sel[1] != "@raw" || sel[2] != "*" {
		t.Fatalf("default select = %v", sel)
	}
	from, _ := body["from"].([]any)
	if len(from) != 1 {
		t.Fatalf("from = %v", body["from"])
	}
}

func TestSearchGroupsRenderAsRows(t *testing.T) {
	srv := searchServer(t, `{"groups":[{"group":{"host":"web-1"},"count":3}]}`, nil)
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "-d", "11111111-1111-1111-1111-111111111111",
		"--select", "count()", "-g", "host",
		"--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || rows[0]["host"] != "web-1" {
		t.Fatalf("out = %q err=%v", out.String(), err)
	}
}

func TestSearchGroupsSeriesRenderAsRows(t *testing.T) {
	srv := searchServer(t, `{"groups_series":[{"host":"web-1","count":3,"time":"t1"},{"host":"web-2","count":5,"time":"t1"}]}`, nil)
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "-d", "11111111-1111-1111-1111-111111111111",
		"--select", "count()", "-g", "host", "--slices", "5",
		"--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("out = %q err=%v", out.String(), err)
	}
	if rows[0]["host"] != "web-1" || rows[1]["host"] != "web-2" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestSearchTotalsOnlyRendersSingleRow(t *testing.T) {
	srv := searchServer(t, `{"totals":{"count":42}}`, nil)
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "-d", "11111111-1111-1111-1111-111111111111",
		"--select", "count()",
		"--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("out = %q err=%v", out.String(), err)
	}
	if got, ok := rows[0]["count"].(float64); !ok || got != 42 {
		t.Fatalf("rows[0] = %+v", rows[0])
	}
}

func TestSearchExplainOnly(t *testing.T) {
	srv := searchServer(t, `{"explain":{"Execution time (millis)":"7"}}`, nil)
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "--explain-only", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["Execution time (millis)"] != "7" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestSearchQueryFromStdin(t *testing.T) {
	var body map[string]any
	srv := searchServer(t, `{"events":[]}`, &body)
	defer srv.Close()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetIn(strings.NewReader("level = 'error'\n"))
	root.SetArgs([]string{"search", "-", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if body["where"] != "level = 'error'" {
		t.Fatalf("where = %v", body["where"])
	}
}

func TestSearchMissingDatasetIsUsageError(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	// With nothing selected and several datasets in the account, search
	// must fail usage-style, naming the datasets (see resolveDataset).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/logs" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"logs":[{"log":"web","log_id":"11111111-1111-1111-1111-111111111111"},{"log":"app","log_id":"22222222-2222-2222-2222-222222222222"}]}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "x", "--api-key", "k", "--base-url", srv.URL})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v (%d)", err, clierr.ExitCode(err))
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || !strings.Contains(ce.Hint, "web") {
		t.Fatalf("hint must list the account's datasets: %v", err)
	}
}

func TestSearchLimitValidatedClientSide(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	for _, n := range []string{"0", "10001"} {
		root := NewRootCmd()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"search", "x", "-d", "11111111-1111-1111-1111-111111111111",
			"--api-key", "k", "-n", n})
		err := root.Execute()
		var ce *clierr.Error
		if err == nil || !errors.As(err, &ce) || ce.Code != "usage_invalid_limit" {
			t.Fatalf("-n %s: want usage_invalid_limit before any network call, got %v", n, err)
		}
	}
}

func TestEventTableColumnsExcludePlumbing(t *testing.T) {
	rows := []map[string]any{{
		"@time": "t", "@status": "info", "@raw": "r",
		"links": []any{"x"}, "metadata.sequence": 1, "metadata.context": "c",
		"message_kvs.level": "info",
	}}
	cols := eventTableColumns(rows)
	for _, c := range cols {
		if c == "links" || strings.HasPrefix(c, "metadata.") {
			t.Fatalf("plumbing column %q leaked into the table: %v", c, cols)
		}
	}
	found := false
	for _, c := range cols {
		if c == "message_kvs.level" {
			found = true
		}
	}
	if !found {
		t.Fatalf("real field missing from table columns: %v", cols)
	}
}

// TestSearchSavedRunsStoredQuery pins --saved: the stored where/from/
// time_range become the request, and explicit flags override.
func TestSearchSavedRunsStoredQuery(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/saved-searches" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-000000000009","name":"oncall-500s"}]`))
		case r.URL.Path == "/saved-searches/aaaaaaaa-aaaa-aaaa-aaaa-000000000009":
			_, _ = w.Write([]byte(`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-000000000009","search_details":{"where":"status >= 500","from":"11111111-1111-1111-1111-111111111111:22222222-2222-2222-2222-222222222222","time_range":"Last 15 minutes"}}`))
		case r.URL.Path == "/search":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			_, _ = w.Write([]byte(`{"events":[]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "--saved", "oncall-500s", "--api-key", "k", "--base-url", srv.URL, "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotBody["where"] != "status >= 500" || gotBody["time_range"] != "Last 15 minutes" {
		t.Fatalf("body = %v", gotBody)
	}
	from, _ := gotBody["from"].([]any)
	if len(from) != 2 {
		t.Fatalf("from = %v, want the stored colon-split log ids", gotBody["from"])
	}

	// explicit query + --since override the stored values
	root = NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "level = 'warn'", "--saved", "oncall-500s", "--since", "2h",
		"--api-key", "k", "--base-url", srv.URL, "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotBody["where"] != "level = 'warn'" || gotBody["time_range"] != "Last 2 hours" {
		t.Fatalf("override body = %v", gotBody)
	}
}

// TestSearchURLFlag pins --url: prints the web link, never runs the query.
// TestSearchURLFlag pins the deep-link format against the real UI route
// (verified 2026-07-23): /org/<id>/search with camelCase timeRange,
// plural logIds, select=*,@raw, and list-view display defaults.
func TestSearchURLFlag(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	t.Setenv("BRONTO_ORG_ID", "org-123") // skip the /organizations lookup for the shape assertions
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			t.Error("--url must not run the search")
		}
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "status >= 500", "-d", "11111111-1111-1111-1111-111111111111",
		"--since", "1h", "--url", "--api-key", "k", "--base-url", srv.URL})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	u := strings.TrimSpace(out.String())
	if !strings.HasPrefix(u, "https://app.eu.bronto.io/org/org-123/search?") {
		t.Fatalf("url = %q", u)
	}
	for _, want := range []string{
		"where=status+%3E%3D+500",
		"timeRange=Last+1+hour",
		"logIds=11111111-1111-1111-1111-111111111111",
		"select=%2A%2C%40raw", // *,@raw
		"display=list",
		"order=newest",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("url missing %q: %q", want, u)
		}
	}

	// app_url override wins (host swapped, /org/<id>/search path preserved)
	t.Setenv("BRONTO_APP_URL", "https://staging.ui.example/")
	root = NewRootCmd()
	out.Reset()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "x", "-d", "11111111-1111-1111-1111-111111111111",
		"--since", "1h", "--url", "--api-key", "k", "--base-url", srv.URL})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "https://staging.ui.example/org/org-123/search?") {
		t.Fatalf("app_url override ignored: %q", out.String())
	}
}

// TestSearchURLResolvesActiveOrg pins the org-id fallback: with no
// org_id configured, the link uses the active org from GET /organizations.
func TestSearchURLResolvesActiveOrg(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			t.Error("--url must not run the search")
		case "/organizations":
			_, _ = w.Write([]byte(`{"organisations":[{"id":"inactive-1","active":false},{"id":"active-9","active":true}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "x", "-d", "11111111-1111-1111-1111-111111111111",
		"--since", "1h", "--url", "--api-key", "k", "--base-url", srv.URL})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "/org/active-9/search?") {
		t.Fatalf("should pick the active org: %q", out.String())
	}
}

func TestSearchPatterns(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		if body["limit"] != 2000.0 {
			t.Errorf("limit = %v, want the raised patterns default", body["limit"])
		}
		_, _ = w.Write([]byte(`{"events":[` +
			`{"@raw":"GET /a/1 200 5ms"},{"@raw":"GET /a/2 200 8ms"},{"@raw":"GET /a/3 200 3ms"},` +
			`{"@raw":"pool exhausted worker=1"}]}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "x", "-d", "11111111-1111-1111-1111-111111111111",
		"--patterns", "--api-key", "k", "--base-url", srv.URL, "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("rows = %q err=%v", out.String(), err)
	}
	if rows[0]["count"] != 3.0 || !strings.Contains(rows[0]["pattern"].(string), "<num>") {
		t.Fatalf("top pattern = %v", rows[0])
	}
	if rows[0]["example"] == "" {
		t.Fatal("machine rows must carry an example line")
	}

	// conflicts rejected
	root = NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "x", "-d", "11111111-1111-1111-1111-111111111111",
		"--patterns", "-g", "level", "--api-key", "k", "--base-url", srv.URL})
	err := root.Execute()
	var ce *clierr.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "usage_invalid_flags" {
		t.Fatalf("want usage_invalid_flags, got %v", err)
	}
}

func TestSearchHistogramConflicts(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "x", "-d", "11111111-1111-1111-1111-111111111111",
		"--histogram", "-g", "level", "--api-key", "k"})
	err := root.Execute()
	var ce *clierr.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "usage_invalid_flags" {
		t.Fatalf("want usage_invalid_flags, got %v", err)
	}
}

func TestSearchHistogramMachineRows(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path %s", r.URL.Path)
		}
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		if sel, _ := body["select"].([]any); len(sel) != 1 || sel[0] != "count(*)" {
			t.Errorf("select = %v", body["select"])
		}
		if body["num_of_slices"] != 8.0 {
			t.Errorf("num_of_slices = %v", body["num_of_slices"])
		}
		_, _ = w.Write([]byte(`{"result":[{"@time":"Wed Jul 22 05:42:55 UTC 2026","@timestamp":"1784698975896","count(*)":"3.0"}]}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "x", "-d", "11111111-1111-1111-1111-111111111111",
		"--histogram", "--slices", "8", "--api-key", "k", "--base-url", srv.URL, "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0]["count"] != 3.0 {
		t.Fatalf("rows = %q err=%v", out.String(), err)
	}
}

func searchTTY(t *testing.T, tty bool) {
	t.Helper()
	old := stdoutIsTTY
	stdoutIsTTY = func() bool { return tty }
	t.Cleanup(func() { stdoutIsTTY = old })
}

const searchKVPayload = `{"events":[
	{"@time":"t1","@status":"info","@raw":"r1","message_kvs":{"eventName":"page_view","path":"/a","session":"s1"}},
	{"@time":"t2","@status":"info","@raw":"r2","message_kvs":{"eventName":"link_click","path":"/b","session":"s1"}}
]}`

func TestSearchTablePromotesFrequentFields(t *testing.T) {
	searchTTY(t, true)
	srv := searchServer(t, searchKVPayload, nil)
	defer srv.Close()
	root := NewRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"search", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	header := strings.SplitN(out.String(), "\n", 2)[0]
	for _, col := range []string{"@TIME", "@STATUS", "MESSAGE_KVS.EVENTNAME", "MESSAGE_KVS.PATH", "MESSAGE_KVS.SESSION"} {
		if !strings.Contains(header, col) {
			t.Fatalf("header missing %s: %q", col, header)
		}
	}
	// 3 promoted keys reach the drop threshold: the blob column goes away.
	if strings.Contains(header, "@RAW") {
		t.Fatalf("@RAW should be dropped with >=3 promoted columns: %q", header)
	}
}

func TestSearchTeachingFooter(t *testing.T) {
	run := func(t *testing.T, tty bool, extra ...string) string {
		t.Helper()
		searchTTY(t, tty)
		srv := searchServer(t, searchKVPayload, nil)
		defer srv.Close()
		root := NewRootCmd()
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		// a UUID ref skips /logs name resolution; the footer echoes it as typed.
		args := append([]string{"search", "-d", "11111111-1111-1111-1111-111111111111",
			"--base-url", srv.URL, "--api-key", "k"}, extra...)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		return errb.String()
	}

	t.Run("shown on tty table", func(t *testing.T) {
		stderr := run(t, true)
		want := "2 results. 6 fields available — 'bronto fields -d 11111111-1111-1111-1111-111111111111' lists them; '--select <field,...>' picks columns; '-x' expands a row."
		if !strings.Contains(stderr, want) {
			t.Fatalf("footer missing.\nwant: %s\ngot stderr: %q", want, stderr)
		}
	})
	t.Run("suppressed when piped", func(t *testing.T) {
		stderr := run(t, false)
		if strings.Contains(stderr, "fields available") {
			t.Fatalf("footer should not appear when piped: %q", stderr)
		}
	})
	t.Run("suppressed with quiet", func(t *testing.T) {
		stderr := run(t, true, "--quiet")
		if strings.Contains(stderr, "fields available") {
			t.Fatalf("footer should not appear with --quiet: %q", stderr)
		}
	})
	t.Run("suppressed with select", func(t *testing.T) {
		stderr := run(t, true, "--select", "eventName")
		if strings.Contains(stderr, "fields available") {
			t.Fatalf("footer should not appear with --select: %q", stderr)
		}
	})
	t.Run("suppressed with machine format", func(t *testing.T) {
		stderr := run(t, true, "-o", "json")
		if strings.Contains(stderr, "fields available") {
			t.Fatalf("footer should not appear with -o json: %q", stderr)
		}
	})
	t.Run("suppressed with expand", func(t *testing.T) {
		stderr := run(t, true, "-x")
		if strings.Contains(stderr, "fields available") {
			t.Fatalf("footer should not appear with -x: %q", stderr)
		}
	})
	t.Run("suppressed on zero results", func(t *testing.T) {
		searchTTY(t, true)
		srv := searchServer(t, `{"events":[]}`, nil)
		defer srv.Close()
		root := NewRootCmd()
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetArgs([]string{"search", "-d", "11111111-1111-1111-1111-111111111111",
			"--base-url", srv.URL, "--api-key", "k"})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(errb.String(), "fields available") {
			t.Fatalf("footer should not appear on empty results: %q", errb.String())
		}
		if !strings.Contains(errb.String(), "No results.") {
			t.Fatalf("empty notice missing: %q", errb.String())
		}
	})
}

func TestSearchExpandRendersBlocks(t *testing.T) {
	searchTTY(t, true)
	long := strings.Repeat("z", 300)
	srv := searchServer(t, `{"events":[{"@time":"t1","@raw":"`+long+`","message_kvs":{"path":"/a"},"metadata":{"sequence":42}}]}`, nil)
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"search", "-x", "-d", "11111111-1111-1111-1111-111111111111",
		"--base-url", srv.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "─ event 1 ") {
		t.Fatalf("missing block header:\n%s", got)
	}
	if !strings.Contains(got, long) {
		t.Fatalf("expanded value truncated:\n%s", got)
	}
	// the detail view keeps the plumbing the table drops
	if !strings.Contains(got, "metadata.sequence") {
		t.Fatalf("metadata.* missing from detail view:\n%s", got)
	}
	if !strings.HasPrefix(got, "─ event 1 ") {
		t.Fatalf("expected @time first after header:\n%s", got)
	}
}

func TestSearchExpandRejectsMachineFormatsAndGroups(t *testing.T) {
	for _, args := range [][]string{
		{"search", "-x", "-o", "json"},
		{"search", "-x", "-o", "jsonl"},
		{"search", "-x", "-o", "csv"},
		{"search", "-x", "-o", "raw"},
		{"search", "-x", "--select", "count()", "-g", "host"},
		{"search", "-x", "--explain-only"},
	} {
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
		}))
		root := NewRootCmd()
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs(append(args, "-d", "11111111-1111-1111-1111-111111111111",
			"--base-url", srv.URL, "--api-key", "k"))
		err := root.Execute()
		srv.Close()
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != "usage_invalid_flags" {
			t.Fatalf("%v: want usage_invalid_flags, got %v", args, err)
		}
		if requests != 0 {
			t.Fatalf("%v: no HTTP request should be made, got %d", args, requests)
		}
	}
}
