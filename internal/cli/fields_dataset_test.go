package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// `bronto fields <dataset>` without -d used to print [] (the positional is
// a field-name filter); naming a dataset there is now a usage error.
func TestFieldsRefusesDatasetAsFilter(t *testing.T) {
	var topKeysCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/logs":
			_, _ = w.Write([]byte(`{"logs":[{"log":"checkout-api","collection":"prod","log_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}]}`))
		case "/top-keys":
			topKeysCalls++
			_, _ = w.Write([]byte(`{"top_keys":[{"key":"status","count":1}]}`))
		}
	}))
	defer srv.Close()
	for _, arg := range []string{"checkout-api", "prod/checkout-api", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"} {
		root := NewRootCmd()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"fields", arg, "--base-url", srv.URL, "--api-key", "k", "-o", "json"})
		err := root.Execute()
		if code := codeOf(err); code != "usage_dataset_as_filter" {
			t.Fatalf("%s: code = %q (err %v)", arg, code, err)
		}
	}
	if topKeysCalls != 0 {
		t.Errorf("top-keys called %d times; must refuse before querying", topKeysCalls)
	}
	// A real field filter still works.
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fields", "stat", "--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"status"`)) {
		t.Fatalf("out = %q", out.String())
	}
}
