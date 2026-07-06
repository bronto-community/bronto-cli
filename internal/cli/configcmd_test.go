package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestConfigListShowsSources(t *testing.T) {
	t.Setenv("BRONTO_REGION", "us")
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"config", "list", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, out.String())
	}
	found := false
	for _, r := range rows {
		if r["key"] == "region" && r["value"] == "us" && r["source"] == "env" {
			found = true
		}
	}
	if !found {
		t.Fatalf("region/us/env row missing: %v", rows)
	}
}
