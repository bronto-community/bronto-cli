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
	_, marker := seededData(t)
	r := NewRunner(t, key)
	logID := seededLogID(t)

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
	_, marker := seededData(t)
	r := NewRunner(t, key)
	logID := seededLogID(t)

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
	_, marker := seededData(t)
	r := NewRunner(t, key)
	logID := seededLogID(t)

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
	_, marker := seededData(t)
	r := NewRunner(t, key)
	logID := seededLogID(t)

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
	r := NewRunner(t, key)
	logID := seededLogID(t)

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

// TestQuery_FieldValuesCountsMarker asserts `bronto fields --values`
// counts a field's values over every event (one grouped count search,
// unlike the sampled /top-keys): every seeded event carries the run's
// ci_marker, so the marker is listed with at least the seeded count. The
// dataset is a UUID, so this is a single request.
func TestQuery_FieldValuesCountsMarker(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)
	_, marker := seededData(t)
	res := mustExitZero(t, r, "fields", "-d", seededLogID(t), "--since", "1h", "--values", "ci_marker", "-o", "json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("fields --values -o json did not parse as an array: %v\noutput: %s", err, res.Stdout)
	}
	for _, row := range rows {
		if row["value"] == marker {
			if n, _ := row["count"].(float64); n < float64(seedTotalEvents) {
				t.Fatalf("marker count = %v, want at least %d", row["count"], seedTotalEvents)
			}
			return
		}
	}
	t.Fatalf("fields --values ci_marker does not list the marker %s: %s", marker, res.Stdout)
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
	_, marker := seededData(t)
	r := NewRunner(t, key)
	logID := seededLogID(t)

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

// TestQuery_CheckMatchesServerGrammar holds `bronto query check` to the
// live API: every expression the server accepts must pass the check (CI
// lints queries with it, so a false rejection fails valid pipelines),
// each must return only events that satisfy it, and expressions the
// server rejects must fail the check too.
func TestQuery_CheckMatchesServerGrammar(t *testing.T) {
	key := skipIfNoCreds(t)
	_, marker := seededData(t)
	r := NewRunner(t, key)
	logID := seededLogID(t)

	str := func(row map[string]any, k string) string { return fmt.Sprint(eventField(row, k)) }
	accepted := []struct {
		expr string
		ok   func(row map[string]any) bool // every returned row must satisfy it
	}{
		{"status IN (500, 404)", func(r map[string]any) bool { s := str(r, "status"); return s == "500" || s == "404" }},
		{"status NOT IN (500, 404)", func(r map[string]any) bool { s := str(r, "status"); return s != "500" && s != "404" }},
		{"level in ('error', 'warn')", func(r map[string]any) bool { l := str(r, "level"); return l == "error" || l == "warn" }},
		{"message LIKE '%seed trace%'", func(r map[string]any) bool { return strings.Contains(str(r, "message"), "seed trace") }},
		{"message NOT LIKE '%seed trace%'", func(r map[string]any) bool { return !strings.Contains(str(r, "message"), "seed trace") }},
		{"message ILIKE 'BRONTO-CI SEED EVENT%'", func(r map[string]any) bool { return strings.HasPrefix(str(r, "message"), "bronto-ci seed event") }},
		{"message REGEXP 'seed (event|trace) 1$'", func(r map[string]any) bool { return strings.HasSuffix(str(r, "message"), " 1") }},
		{`"seed trace"`, func(r map[string]any) bool { return strings.Contains(str(r, "message"), "seed trace") }},
		{"'seed trace'", func(r map[string]any) bool { return strings.Contains(str(r, "message"), "seed trace") }},
		{"trace AND NOT event", func(r map[string]any) bool { return strings.Contains(str(r, "message"), "seed trace") }},
		{"status <> 200", func(r map[string]any) bool { return str(r, "status") != "200" }},
		{"LOWER(level) = 'error'", func(r map[string]any) bool { return str(r, "level") == "error" }},
		{"trace_id IS NOT NULL", func(r map[string]any) bool { return eventField(r, "trace_id") != nil }},
		{"trace_id IS NULL AND EXISTS level", func(r map[string]any) bool { return eventField(r, "trace_id") == nil }},
		{`"level" = 'warn' && status >= 400`, func(r map[string]any) bool { return str(r, "level") == "warn" }},
		{"level = 'debug' OR level = 'info' AND status = 500", func(r map[string]any) bool {
			// AND binds tighter: debug rows of any status, or info rows with 500
			return str(r, "level") == "debug" || (str(r, "level") == "info" && str(r, "status") == "500")
		}},
		// literal and parenthesized operands; a literal is a constant
		{"200 = status", func(r map[string]any) bool { return str(r, "status") == "200" }},
		{"(status) = 200", func(r map[string]any) bool { return str(r, "status") == "200" }},
		{"'a' = 'a'", func(map[string]any) bool { return true }},
		{"@raw ~ 'ci_marker'", func(map[string]any) bool { return true }}, // @raw: the one @-field
	}
	for _, c := range accepted {
		t.Run("accepts "+c.expr, func(t *testing.T) {
			check, err := r.Run(t.Context(), "", "query", "check", c.expr, "-o", "json")
			if err != nil {
				t.Fatal(err)
			}
			var verdict struct{ Valid bool }
			if check.ExitCode != 0 || json.Unmarshal([]byte(check.Stdout), &verdict) != nil || !verdict.Valid {
				t.Errorf("query check rejected a server-valid query (exit %d)\nstdout: %s\nstderr: %s",
					check.ExitCode, check.Stdout, check.Stderr)
			}
			res, err := r.Run(t.Context(), "", searchArgs(logID,
				fmt.Sprintf("ci_marker = '%s' AND (%s)", marker, c.expr), "-o", "json", "-n", "200")...)
			if err != nil {
				t.Fatal(err)
			}
			if res.ExitCode != 0 {
				t.Fatalf("server rejected %q (exit %d): %s", c.expr, res.ExitCode, res.Stderr)
			}
			var rows []map[string]any
			if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
				t.Fatalf("search -o json did not parse: %v\n%s", err, res.Stdout)
			}
			if len(rows) == 0 {
				t.Fatalf("%q matched no seeded events", c.expr)
			}
			for _, row := range rows {
				if !c.ok(row) {
					t.Fatalf("%q returned a row that does not satisfy it: %+v", c.expr, row)
				}
			}
		})
	}

	for _, expr := range []string{
		"status IN ()",
		"seed trace", // an unquoted phrase
		"level = 'error' || status >= 500",
		"status NOT = 500",
		"@status = 'info'", // @-fields other than @raw
		"@time > 0",
		"'x' = @foo",
	} {
		t.Run("rejects "+expr, func(t *testing.T) {
			check, err := r.Run(t.Context(), "", "query", "check", expr, "-o", "json")
			if err != nil {
				t.Fatal(err)
			}
			if check.ExitCode != 2 || ParseErrorEnvelope(t, check.Stderr).Error.Code != "usage_invalid_query" {
				t.Errorf("query check accepted %q (exit %d): %s", expr, check.ExitCode, check.Stdout)
			}
			res, err := r.Run(t.Context(), "", searchArgs(logID, expr, "-o", "json", "-n", "1")...)
			if err != nil {
				t.Fatal(err)
			}
			if res.ExitCode == 0 {
				t.Errorf("server now accepts %q; query check must accept it too", expr)
			}
		})
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

// TestQuery_SearchOrdering asserts search returns newest events first by
// default and oldest first with --oldest-first. The live API defaults to
// ascending when most_recent_first is absent, so the CLI must send it.
func TestQuery_SearchOrdering(t *testing.T) {
	key := skipIfNoCreds(t)
	_, marker := seededData(t)
	r := NewRunner(t, key)
	logID := seededLogID(t)

	times := func(extra ...string) []int64 {
		t.Helper()
		args := searchMarkerArgs(logID, marker, append([]string{"-o", "json", "-n", "50"}, extra...)...)
		var out []int64
		for _, row := range mustRunJSONArray(t, r, args...) {
			ms, ok := parseEventTime(row["@time"])
			if !ok {
				t.Fatalf("row without a parseable @time: %+v", row)
			}
			out = append(out, ms)
		}
		if len(out) == 0 {
			t.Fatal("search returned no rows for the seeded marker")
		}
		return out
	}
	// The seed spaces its events one second apart; without at least two
	// distinct times every direction check below would pass vacuously.
	distinct := func(ts []int64) bool {
		for _, v := range ts[1:] {
			if v != ts[0] {
				return true
			}
		}
		return false
	}
	newest := times()
	if !distinct(newest) {
		t.Fatalf("need at least two distinct @time values to check ordering, got %v", newest)
	}
	for i := 1; i < len(newest); i++ {
		if newest[i] > newest[i-1] {
			t.Fatalf("default order is not newest first: %v", newest)
		}
	}
	oldest := times("--oldest-first")
	for i := 1; i < len(oldest); i++ {
		if oldest[i] < oldest[i-1] {
			t.Fatalf("--oldest-first order is not ascending: %v", oldest)
		}
	}
	if !distinct(oldest) {
		t.Fatalf("need at least two distinct @time values to check ordering, got %v", oldest)
	}
	if newest[0] <= oldest[0] {
		t.Fatalf("default first event (%d) is not newer than --oldest-first's (%d)", newest[0], oldest[0])
	}
}

// TestQuery_GroupByWithoutSelect asserts `search -g <field>` works with no
// --select: the API rejects a search without select, so the CLI defaults
// to count(*).
func TestQuery_GroupByWithoutSelect(t *testing.T) {
	key := skipIfNoCreds(t)
	_, marker := seededData(t)
	r := NewRunner(t, key)
	logID := seededLogID(t)

	rows := mustRunJSONArray(t, r, searchArgs(logID, fmt.Sprintf("ci_marker = '%s'", marker), "-g", "level", "-o", "json")...)
	if len(rows) == 0 {
		t.Fatal("grouped search returned no rows for the seeded marker")
	}
}

// TestQuery_ExplainOnly asserts --explain-only returns a query plan; it
// used to send no select and fail with a 400.
func TestQuery_ExplainOnly(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)
	logID := seededLogID(t)

	res := mustExitZero(t, r, searchArgs(logID, "", "--explain-only", "-o", "json")...)
	var plan map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &plan); err != nil || len(plan) == 0 {
		t.Fatalf("explain output is not a non-empty JSON object (err %v): %s", err, res.Stdout)
	}
}
