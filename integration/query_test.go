package integration

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestQuery_SearchWhereMarkerJSON asserts the core query path over the
// seeded data: search "ci_marker = '<marker>'" -o json returns rows,
// and every row carries the marker.
func TestQuery_SearchWhereMarkerJSON(t *testing.T) {
	key := skipIfNoCreds(t)
	dataset, marker := seededData(t)
	r := NewRunner(t, key)
	logID := logIDForDataset(t, r, dataset)

	res, err := r.Run(t.Context(), "", searchMarkerArgs(logID, marker, "-o", "json", "-n", "50")...)
	if err != nil {
		t.Fatalf("running search: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("search exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("search -o json did not parse: %v\noutput: %s", err, res.Stdout)
	}
	if len(rows) == 0 {
		t.Fatal("search returned no rows for the seeded marker")
	}
	for _, row := range rows {
		if m, _ := eventField(row, "ci_marker").(string); m != marker {
			t.Fatalf("row ci_marker = %q, want %q: %+v", m, marker, row)
		}
	}
}

// TestQuery_FieldsFlagFiltersColumns asserts the global --fields flag
// (comma-separated field selection on printed output) narrows json rows to
// exactly the requested keys.
func TestQuery_FieldsFlagFiltersColumns(t *testing.T) {
	key := skipIfNoCreds(t)
	dataset, marker := seededData(t)
	r := NewRunner(t, key)
	logID := logIDForDataset(t, r, dataset)

	// searchMarkerArgs passes --select, so rows are the PROJECTION with
	// bare keys (SelectedRows) — --fields names them directly.
	res, err := r.Run(t.Context(), "", searchMarkerArgs(logID, marker,
		"--fields", "ci_marker,level", "-o", "json", "-n", "5")...)
	if err != nil {
		t.Fatalf("running search: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("search exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("search -o json did not parse: %v\noutput: %s", err, res.Stdout)
	}
	if len(rows) == 0 {
		t.Fatal("search returned no rows for the seeded marker")
	}
	sawMarker := false
	for _, row := range rows {
		for k := range row {
			if k != "ci_marker" && k != "level" {
				t.Fatalf("--fields leaked extra key %q: %+v", k, row)
			}
		}
		if m, _ := row["ci_marker"].(string); m == marker {
			sawMarker = true
		}
	}
	// Guard against a vacuous pass (rows with zero matching keys would
	// trivially satisfy the leak check above).
	if !sawMarker {
		t.Fatalf("no filtered row carried ci_marker=%s: %+v", marker, rows)
	}
}

// TestQuery_DatasetByName asserts -d accepts a dataset NAME (resolved to
// its log id via /logs, see internal/cli/dataset.go) — the first-run UX:
// nobody should need to copy a UUID for an interactive query.
func TestQuery_DatasetByName(t *testing.T) {
	key := skipIfNoCreds(t)
	dataset, marker := seededData(t)
	r := NewRunner(t, key)

	res, err := r.Run(t.Context(), "", "search", fmt.Sprintf("ci_marker = '%s'", marker),
		"-d", dataset, "--since", "1h", "-o", "json", "-n", "1")
	if err != nil {
		t.Fatalf("running search by dataset name: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("search -d <name> exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("search -o json did not parse: %v\noutput: %s", err, res.Stdout)
	}
	if len(rows) == 0 {
		t.Fatal("search by dataset name returned no rows for the seeded marker")
	}
}

// TestQuery_JQExpression asserts --jq applies to json output.
func TestQuery_JQExpression(t *testing.T) {
	key := skipIfNoCreds(t)
	dataset, marker := seededData(t)
	r := NewRunner(t, key)
	logID := logIDForDataset(t, r, dataset)

	// searchMarkerArgs passes --select: projected rows carry bare keys.
	jqExpr := `.ci_marker`
	res, err := r.Run(t.Context(), "", searchMarkerArgs(logID, marker, "--jq", jqExpr, "-o", "json", "-n", "5")...)
	if err != nil {
		t.Fatalf("running search: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("search exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, marker) {
		t.Fatalf("--jq %q output does not contain the marker: %s", jqExpr, res.Stdout)
	}
}

// TestQuery_JSONLLineParses asserts -o jsonl emits one JSON object per line.
func TestQuery_JSONLLineParses(t *testing.T) {
	key := skipIfNoCreds(t)
	dataset, marker := seededData(t)
	r := NewRunner(t, key)
	logID := logIDForDataset(t, r, dataset)

	res, err := r.Run(t.Context(), "", searchMarkerArgs(logID, marker, "-o", "jsonl", "-n", "50")...)
	if err != nil {
		t.Fatalf("running search: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("search exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	trimmed := strings.TrimRight(res.Stdout, "\n")
	if trimmed == "" {
		t.Fatal("jsonl output had no lines")
	}
	lines := strings.Split(trimmed, "\n")
	for _, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("jsonl line did not parse as JSON: %v\nline: %s", err, line)
		}
	}
}

// TestQuery_FieldsCommandListsMarkerKey asserts `bronto fields` (top-key
// discovery) surfaces the ci_marker field name, since every seeded event
// carries it. The /top-keys index lags behind search visibility (the first
// live run returned [] right after the seed poll passed), so this polls
// with the same budget the seed itself uses instead of asserting once.
func TestQuery_FieldsCommandListsMarkerKey(t *testing.T) {
	key := skipIfNoCreds(t)
	dataset, _ := seededData(t)
	r := NewRunner(t, key)
	logID := logIDForDataset(t, r, dataset)

	PollUntil(t, seedPollBudget(), seedPollInterval, func() (bool, error) {
		res, err := r.Run(t.Context(), "", "fields", "-d", logID, "--since", "1h", "-o", "json")
		if err != nil {
			return false, err
		}
		if res.ExitCode != 0 {
			return false, fmt.Errorf("fields exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
			return false, fmt.Errorf("fields -o json did not parse: %w\noutput: %s", err, res.Stdout)
		}
		for _, row := range rows {
			if k, _ := row["key"].(string); k == "ci_marker" || k == "message_kvs.ci_marker" {
				return true, nil
			}
		}
		return false, fmt.Errorf("fields output does not list the ci_marker key yet: %s", res.Stdout)
	})
}

// TestQuery_ContextAroundSeededEvent exercises `bronto context`: it first
// fetches @sequence/@time for one seeded row via search, then asks for the
// events around it.
//
// @sequence/@time's exact response shape is genuinely uncertain until the
// first live run: the vendored spec's '@time' example
// ("2024-03-27 10:25:40.632 UTC") is a non-standard human string, not
// RFC3339, and it's unconfirmed whether the live API actually returns that
// literal format. This test tries a small set of layouts and, per the
// plan, tolerates the whole shape being unrecognized by skipping the
// context sub-assert with a note rather than failing the suite over an
// output-format guess.
func TestQuery_ContextAroundSeededEvent(t *testing.T) {
	key := skipIfNoCreds(t)
	dataset, marker := seededData(t)
	r := NewRunner(t, key)
	logID := logIDForDataset(t, r, dataset)

	res, err := r.Run(t.Context(), "",
		searchArgs(logID, fmt.Sprintf("ci_marker = '%s'", marker),
			"--select", "@time", "--select", "@sequence", "-o", "json", "-n", "1")...)
	if err != nil {
		t.Fatalf("running search: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("search exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("search -o json did not parse: %v\noutput: %s", err, res.Stdout)
	}
	if len(rows) == 0 {
		t.Fatal("search returned no rows for the seeded marker")
	}
	row := rows[0]

	seq, seqOK := parseSequence(row["@sequence"])
	ts, tsOK := parseEventTime(row["@time"])
	if !seqOK || !tsOK {
		t.Skipf("search row lacks a parseable @sequence/@time (row: %+v); "+
			"skipping the context sub-assert until the live response shape is confirmed", row)
	}

	ctxRes, err := r.Run(t.Context(), "",
		"context",
		"--sequence", strconv.FormatInt(seq, 10),
		"--dataset", logID,
		"--timestamp", strconv.FormatInt(ts, 10),
		"--direction", "both",
		"-o", "json",
	)
	if err != nil {
		t.Fatalf("running context: %v", err)
	}
	if ctxRes.ExitCode != 0 {
		t.Fatalf("context exited %d\nstdout: %s\nstderr: %s", ctxRes.ExitCode, ctxRes.Stdout, ctxRes.Stderr)
	}
	var ctxRows []map[string]any
	if err := json.Unmarshal([]byte(ctxRes.Stdout), &ctxRows); err != nil {
		t.Fatalf("context -o json did not parse as an array: %v\noutput: %s", err, ctxRes.Stdout)
	}
}

// parseSequence accepts @sequence as either a JSON number or a numeric
// string (the spec documents it as a string; being liberal here costs
// nothing).
func parseSequence(v any) (int64, bool) {
	switch s := v.(type) {
	case float64:
		return int64(s), true
	case string:
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil
	}
	return 0, false
}

// parseEventTime accepts @time as a unix-ms number, or a string in one of a
// few candidate layouts (RFC3339 variants, plus the spec's own non-standard
// example format), returning unix milliseconds.
func parseEventTime(v any) (int64, bool) {
	switch tv := v.(type) {
	case float64:
		return int64(tv), true
	case string:
		layouts := []string{
			"2006-01-02 15:04:05.000 UTC",
			time.RFC3339Nano,
			time.RFC3339,
		}
		for _, layout := range layouts {
			if parsed, err := time.Parse(layout, tv); err == nil {
				return parsed.UnixMilli(), true
			}
		}
	}
	return 0, false
}
