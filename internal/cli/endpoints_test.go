package cli

import (
	"strings"
	"testing"
)

func TestEndpointInventoryShape(t *testing.T) {
	inv := EndpointInventory()
	byPattern := map[string]string{}
	for _, e := range inv {
		if e.Pattern == "" || e.Command == "" {
			t.Fatalf("inventory entry with empty field: %+v", e)
		}
		if _, dup := byPattern[e.Pattern]; dup {
			t.Fatalf("duplicate pattern %q — commands must merge into one entry", e.Pattern)
		}
		byPattern[e.Pattern] = e.Command
	}

	// Registry expansion: every resource contributes its Base and a per-ID
	// pattern.
	for _, d := range resourceRegistry {
		if byPattern[d.Base] == "" {
			t.Errorf("registry Base %q missing from inventory", d.Base)
		}
		if byPattern[d.idBase()+"/{*}"] == "" {
			t.Errorf("registry per-ID pattern %q missing from inventory", d.idBase()+"/{*}")
		}
	}

	// A pattern shared by several commands merges (e.g. /logs is datasets'
	// Base and ping's health-check endpoint).
	if c := byPattern["/logs"]; !strings.Contains(c, "datasets") || !strings.Contains(c, "ping") {
		t.Errorf("/logs command = %q, want both datasets and ping", c)
	}
}
