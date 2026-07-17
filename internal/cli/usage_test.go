package cli

import (
	"net/http"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

func TestUsageBuildsTimeRangeParam(t *testing.T) {
	var gotPath string
	var gotQuery map[string][]string
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"usage":[{"day":"2026-07-01","bytes":100}]}`))
	}, "", "usage", "--since", "7d", "-o", "json")
	if err != nil {
		t.Fatalf("err = %v, out = %q", err, out)
	}
	if gotPath != "/usage" {
		t.Fatalf("path = %q, want /usage", gotPath)
	}
	if got := gotQuery["time_range"]; len(got) != 1 || got[0] != "Last 7 days" {
		t.Fatalf("time_range = %v", got)
	}
	if _, ok := gotQuery["log_id"]; ok {
		t.Fatalf("log_id must not be present, got %v", gotQuery["log_id"])
	}
}

func TestUsageDefaultsSince(t *testing.T) {
	var gotQuery map[string][]string
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"usage":[]}`))
	}, "", "usage")
	if err != nil {
		t.Fatal(err)
	}
	if got := gotQuery["time_range"]; len(got) != 1 || got[0] != "Last 7 days" {
		t.Fatalf("time_range = %v, want default Last 7 days", got)
	}
	if _, ok := gotQuery["log_id"]; ok {
		t.Fatalf("log_id must not be present, got %v", gotQuery["log_id"])
	}
}

func TestUsageRejectsCompoundSince(t *testing.T) {
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be contacted")
	}, "", "usage", "--since", "1h30m")
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage error exit 2, got %v", err)
	}
}
