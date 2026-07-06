package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
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

func TestConfigSetRejectsAPIKey(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"config", "set", "api_key", "sekret"})
	err := root.Execute()
	if err == nil {
		t.Fatal("want rejection of api_key")
	}
	if clierr.ExitCode(err) != 2 {
		t.Fatalf("exit = %d, want 2", clierr.ExitCode(err))
	}
}

func TestConfigSetThenGetRoundTrip(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())

	root := NewRootCmd()
	var errBuf bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errBuf)
	root.SetArgs([]string{"config", "set", "default_dataset", "ds-42"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errBuf.String(), "default_dataset") {
		t.Fatalf("confirmation on stderr missing: %q", errBuf.String())
	}

	root2 := NewRootCmd()
	var out bytes.Buffer
	root2.SetOut(&out)
	root2.SetErr(&bytes.Buffer{})
	root2.SetArgs([]string{"config", "get", "default_dataset"})
	if err := root2.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "ds-42" {
		t.Fatalf("get output = %q, want ds-42", out.String())
	}
}
