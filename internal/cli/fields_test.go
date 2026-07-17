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
		if q.Get("time_range") != "Last 1 hour" || q.Get("log_id") != "ds-1" || q.Get("limit") != "10" {
			t.Errorf("query = %v", q)
		}
		_, _ = w.Write([]byte(`{"top_keys":[{"key":"status","count":42},{"key":"host","count":7}]}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "-d", "ds-1", "--since", "1h", "-n", "10",
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

func TestFieldsRejectsCompoundSince(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "--since", "1h30m", "--api-key", "k"})
	if err := root.Execute(); err == nil {
		t.Fatal("compound since must error")
	}
}
