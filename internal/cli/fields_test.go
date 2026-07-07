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

func TestFieldsRejectsCompoundSince(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "--since", "1h30m", "--api-key", "k"})
	if err := root.Execute(); err == nil {
		t.Fatal("compound since must error")
	}
}
