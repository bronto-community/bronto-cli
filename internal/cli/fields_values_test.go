package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// liveFieldValues is a count(*) search grouped by $error.type over
// .traces/payments-gateway (EU, 2026-10-22): most frequent first, and the
// events without the field grouped as "null".
const liveFieldValues = `{"groups":[
 {"group":"[null]","stat":"count(*)","value":879,"count":879,"counts":[879],"values":[879]},
 {"group":"[502]","stat":"count(*)","value":25,"count":25,"counts":[25],"values":[25]}]}`

func runFieldValuesCmd(t *testing.T, respond string, args ...string) (string, string, map[string]any, error) {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/logs" {
			_, _ = w.Write([]byte(`{"logs":[{"log":"checkout-api","collection":".traces","log_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1"},` +
				`{"log":"web","collection":"prod","log_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa2"}]}`))
			return
		}
		if r.URL.Path != "/search" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(respond))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	if len(args) == 0 || args[0] != "--all" {
		args = append([]string{"-d", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}, args...)
	} else {
		args = args[1:]
	}
	root.SetArgs(append([]string{"fields", "--base-url", srv.URL, "--api-key", "k"}, args...))
	err := root.Execute()
	return out.String(), errb.String(), body, err
}

func TestFieldsValuesGroupedCount(t *testing.T) {
	out, _, body, err := runFieldValuesCmd(t, liveFieldValues, "--values", "$error.type", "--since", "24h", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	// One grouped count search over every event in the range.
	if g, _ := body["groups"].([]any); len(g) != 1 || g[0] != "$error.type" {
		t.Fatalf("groups = %v", body["groups"])
	}
	if s, _ := body["select"].([]any); len(s) != 1 || s[0] != "count(*)" {
		t.Fatalf("select = %v", body["select"])
	}
	if body["limit"] != float64(defaultValuesLimit) || body["time_range"] != "Last 24 hours" {
		t.Fatalf("limit/time = %v / %v", body["limit"], body["time_range"])
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not a JSON array: %v\n%s", err, out)
	}
	if len(rows) != 2 || rows[0]["value"] != nil || rows[0]["count"] != 879.0 ||
		rows[1]["value"] != "502" || rows[1]["count"] != 25.0 {
		t.Fatalf("rows = %v", rows)
	}

	out, _, body, err = runFieldValuesCmd(t, liveFieldValues, "--values", "$error.type", "-n", "5", "-o", "table")
	if err != nil {
		t.Fatal(err)
	}
	if body["limit"] != 5.0 {
		t.Fatalf("-n: limit = %v", body["limit"])
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "VALUE COUNT" ||
		strings.Join(strings.Fields(lines[1]), " ") != "(none) 879" ||
		strings.Join(strings.Fields(lines[2]), " ") != "502 25" {
		t.Fatalf("table:\n%s", out)
	}
}

// Only a "null" group: no event has the field, most likely a typo.
func TestFieldsValuesWarnsWhenFieldMissing(t *testing.T) {
	_, stderr, _, err := runFieldValuesCmd(t, `{"groups":[{"group":"null","value":904,"count":904}]}`,
		"--values", "$nope", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "No event in the range has $nope") {
		t.Fatalf("stderr = %q", stderr)
	}
	// With -n 1 the missing group can fill the only slot although values
	// exist: no conclusion then.
	_, stderr, _, err = runFieldValuesCmd(t, `{"groups":[{"group":"[null]","value":879}]}`,
		"--values", "$error.type", "-n", "1", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "No event") {
		t.Fatalf("-n 1 must not conclude the field is missing: %q", stderr)
	}
}

// Without -d, --values covers every dataset, like fields without -d.
func TestFieldsValuesAllDatasets(t *testing.T) {
	_, _, body, err := runFieldValuesCmd(t, liveFieldValues, "--all", "--values", "$error.type", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	from, _ := body["from"].([]any)
	if len(from) != 2 || from[0] != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1" || from[1] != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa2" {
		t.Fatalf("from = %v, want every dataset", body["from"])
	}
}

// Group values keep their literal content: only the one pair of brackets
// the API wraps a group in is removed, and an object-shaped group yields
// its field's value.
func TestFieldValueRowsKeepLiteralValues(t *testing.T) {
	rows := fieldValueRows([]map[string]any{
		{"group": "[[warn]]", "value": 3.0},
		{"group": "[ padded ]", "value": 2.0},
		{"group": map[string]any{"level": "OK"}, "value": 1.0},
		{"group": "[null]", "count": 4.0},
	}, "level")
	want := []any{"[warn]", " padded ", "OK", nil}
	for i, w := range want {
		if rows[i]["value"] != w {
			t.Errorf("row %d value = %#v, want %#v", i, rows[i]["value"], w)
		}
	}
	if rows[3]["count"] != 4.0 {
		t.Errorf("count falls back to count: %v", rows[3])
	}
}

func TestFieldsValuesRejectsBadUsage(t *testing.T) {
	for _, args := range [][]string{
		{"--values", "$span.status_code", "status"},
		{"--values", ""},
		{"--values", "$x", "-n", "-1"},
	} {
		_, _, _, err := runFieldValuesCmd(t, `{}`, args...)
		if code := codeOf(err); !strings.HasPrefix(code, "usage_") {
			t.Errorf("%v: code = %q (%v)", args, code, err)
		}
	}
}
