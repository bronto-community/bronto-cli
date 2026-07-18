package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	old := stdoutIsTTY
	stdoutIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY = old })

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "bronto ") {
		t.Fatalf("human version output = %q", out.String())
	}

	out.Reset()
	root = NewRootCmd()
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"version", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v["version"] == "" {
		t.Fatalf("json version output = %q err=%v", out.String(), err)
	}
}
