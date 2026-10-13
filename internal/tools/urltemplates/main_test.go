package main

import (
	"os"
	"testing"
)

// TestURLTemplatesUpToDate fails when api/openapi.yaml changed (a
// re-vendor) without regenerating internal/api/urltemplates_gen.go.
func TestURLTemplatesUpToDate(t *testing.T) {
	spec, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want, err := Generate(spec)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../api/urltemplates_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("internal/api/urltemplates_gen.go is stale; run: go generate ./internal/api")
	}
}
