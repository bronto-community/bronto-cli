package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFieldsListsTopKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/top-keys" {
			t.Errorf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("time_range") != "Last 1 hour" || q.Get("log_id") != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" || q.Get("limit") != "10" {
			t.Errorf("query = %v", q)
		}
		_, _ = w.Write([]byte(`{"top_keys":[{"key":"status","count":42},{"key":"host","count":7}]}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "-d", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "--since", "1h", "-n", "10",
		"--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 2 || rows[0]["key"] != "status" {
		t.Fatalf("out = %q err=%v", out.String(), err)
	}
}

func TestFieldsNormalizesNumericMap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":42,"host":7}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("out = %q", out.String())
	}
	for _, r := range rows {
		if r["key"] == "" || r["count"] == nil {
			t.Fatalf("row = %v", r)
		}
	}
}

// TestFieldsNormalizesLiveNestedShape pins the LIVE /top-keys response
// shape observed in CI: keys nested per log id, each with
// TopKeysPerLogOrMetric metadata. Ranks for the same key are summed across
// logs, and rows order by count desc then key asc.
func TestFieldsNormalizesLiveNestedShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"log-a": {
				"status": {"rank": 5, "type": "NUMBER", "field_type": "MESSAGE_KVP"},
				"ci_marker": {"rank": 5, "type": "STRING", "field_type": "MESSAGE_KVP"}
			},
			"log-b": {
				"status": {"rank": 2, "type": "NUMBER", "field_type": "MESSAGE_KVP"},
				"host": {"type": "STRING", "field_type": "ATTRIBUTE"}
			}
		}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 3 {
		t.Fatalf("out = %q err=%v", out.String(), err)
	}
	if rows[0]["key"] != "status" || rows[0]["count"] != 7.0 {
		t.Fatalf("summed rank row = %v", rows[0])
	}
	if rows[1]["key"] != "ci_marker" || rows[2]["key"] != "host" {
		t.Fatalf("tie/zero ordering = %v, %v", rows[1], rows[2])
	}
}

// TestFieldsNormalizesSingleLevelMeta covers the defensive single-nesting
// variant ({key: metadata} without the per-log envelope).
func TestFieldsNormalizesSingleLevelMeta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status": {"rank": 3, "type": "NUMBER"}, "host": {"rank": 9, "type": "STRING"}}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("out = %q err=%v", out.String(), err)
	}
	if rows[0]["key"] != "host" || rows[1]["key"] != "status" {
		t.Fatalf("rows = %v", rows)
	}
}

// TestFieldsCapturesSampleValues pins that the live shape's per-key `values`
// sample flows through to json output as a deduped, sorted array.
func TestFieldsCapturesSampleValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"log-a": {
				"model": {"type": "STRING", "field_type": "ATTRIBUTE",
					"values": {"claude-fable-5": {"rank": -1}, "claude-opus-4-8": {"rank": -1}}},
				"host": {"type": "STRING", "field_type": "ATTRIBUTE"}
			},
			"log-b": {
				"model": {"type": "STRING", "field_type": "ATTRIBUTE",
					"values": {"claude-fable-5": {"rank": -1}, "claude-haiku": {"rank": -1}}}
			}
		}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("out = %q err=%v", out.String(), err)
	}
	var model map[string]any
	for _, r := range rows {
		if r["key"] == "model" {
			model = r
		}
	}
	if model == nil {
		t.Fatalf("no model row in %v", rows)
	}
	vals, ok := model["values"].([]any)
	if !ok {
		t.Fatalf("values type = %T (%v)", model["values"], model["values"])
	}
	// Deduped across logs, sorted: claude-fable-5, claude-haiku, claude-opus-4-8.
	want := []string{"claude-fable-5", "claude-haiku", "claude-opus-4-8"}
	if len(vals) != len(want) {
		t.Fatalf("values = %v, want %v", vals, want)
	}
	for i, v := range want {
		if vals[i] != v {
			t.Fatalf("values[%d] = %v, want %s", i, vals[i], v)
		}
	}
}

// TestFieldsNameFilter narrows the field list to keys containing the
// positional arg (case-insensitive).
func TestFieldsNameFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"top_keys":[
			{"key":"model","count":9},{"key":"model_provider","count":4},{"key":"host","count":7}]}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "MODEL", "--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("out = %q (want 2 model* rows)", out.String())
	}
	for _, r := range rows {
		if r["key"] != "model" && r["key"] != "model_provider" {
			t.Fatalf("unexpected row %v", r)
		}
	}
}

func TestDisplayValues(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{""}, "(empty)"},
		{[]string{"a", "b", "c"}, "a, b, c"},
		{[]string{"a", "b", "c", "d", "e", "f", "g"}, "a, b, c, d, e, …"},
	}
	for _, c := range cases {
		if got := displayValues(c.in); got != c.want {
			t.Errorf("displayValues(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestFieldsValuesTableCapped confirms the table cell is the compact string
// form (comma-joined, ellipsis) rather than a Go slice literal.
func TestFieldsValuesTableCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"log-a":{"model":{"type":"STRING","field_type":"ATTRIBUTE",
			"values":{"a":{"rank":-1},"b":{"rank":-1},"c":{"rank":-1},"d":{"rank":-1},"e":{"rank":-1},"f":{"rank":-1}}}}}`))
	}))
	defer srv.Close()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "--base-url", srv.URL, "--api-key", "k", "-o", "table"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !bytes.Contains(out.Bytes(), []byte("a, b, c, d, e, …")) {
		t.Fatalf("table missing capped values cell:\n%s", s)
	}
	if bytes.Contains(out.Bytes(), []byte("[a b c")) {
		t.Fatalf("table leaked slice literal:\n%s", s)
	}
}

func TestFieldsRejectsCompoundSince(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "--since", "1h30m", "--api-key", "k"})
	if err := root.Execute(); err == nil {
		t.Fatal("compound since must error")
	}
}
