package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResourcesCRUD exercises create/list/get/update/delete (per resource
// descriptor in internal/cli/resources.go) against every uniform
// management resource kind the generic factory generates subcommands for,
// except datasets — this suite never creates/updates/deletes a dataset
// itself (ingestion auto-creates them; deleting one is exercised only by
// the sweeper, see sweeper.go's RunSweeper, and by resources.go's own
// descriptor conformance test). Each kind runs in its own subtest, in
// parallel (t.Parallel()), each with its own hermetic Runner/config dir.
//
// Monitors and saved-searches both reference a real log/dataset id in
// their create bodies (Monitor.logs, SavedSearch.log_ids —
// api/openapi.yaml), so this fetches one via `bronto datasets list` rather
// than a fabricated UUID: it's genuinely uncertain whether the API
// validates that these ids reference an existing log, and a fabricated-but-
// well-formed UUID risks a 400 we can't fully explain from the spec alone.
// Monitors and saved-searches skip individually when the account has no
// datasets; dashboards and api-keys always run.
func TestResourcesCRUD(t *testing.T) {
	key := skipIfNoCreds(t)

	t.Run("monitors", func(t *testing.T) {
		t.Parallel()
		logID := firstDatasetLogID(t, NewRunner(t, key))
		testMonitorCRUD(t, NewRunner(t, key), logID)
	})
	t.Run("dashboards", func(t *testing.T) {
		t.Parallel()
		testDashboardCRUD(t, NewRunner(t, key))
	})
	t.Run("saved-searches", func(t *testing.T) {
		t.Parallel()
		logID := firstDatasetLogID(t, NewRunner(t, key))
		testSavedSearchCRUD(t, NewRunner(t, key), logID)
	})
	t.Run("api-keys", func(t *testing.T) {
		t.Parallel()
		testAPIKeyCRUD(t, NewRunner(t, key))
	})
}

func testMonitorCRUD(t *testing.T, r *Runner, logID string) {
	name := resourceName("monitor")

	created := mustRunJSONObject(t, r, "monitors", "create", "--input",
		writeBodyFile(t, monitorBody(name, logID, 1_000_000)))
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("monitors create response missing id: %+v", created)
	}
	t.Cleanup(func() { bestEffortDelete(r, "monitors", id) })

	assertListContainsName(t, r, "monitors", name)

	got := mustRunJSONObject(t, r, "monitors", "get", id)
	if gotID, _ := got["id"].(string); gotID != id {
		t.Fatalf("monitors get id = %q, want %q", gotID, id)
	}

	// Monitors' update verb is PUT (see resourceRegistry), so resend a
	// full, valid body rather than a partial patch.
	mustRunJSONObject(t, r, "monitors", "update", id, "--input",
		writeBodyFile(t, monitorBody(name, logID, 2_000_000)))

	mustExitZero(t, r, "monitors", "events", id)
	mustExitZero(t, r, "monitors", "mute", id)

	mustExitZero(t, r, "monitors", "delete", id, "--yes")
}

func testDashboardCRUD(t *testing.T, r *Runner) {
	name := resourceName("dashboard")

	created := mustRunJSONObject(t, r, "dashboards", "create", "--input",
		writeBodyFile(t, map[string]any{"name": name}))
	id, _ := created["dashboard_id"].(string)
	if id == "" {
		t.Fatalf("dashboards create response missing dashboard_id: %+v", created)
	}
	t.Cleanup(func() { bestEffortDelete(r, "dashboards", id) })

	assertListContainsName(t, r, "dashboards", name)

	got := mustRunJSONObject(t, r, "dashboards", "get", id)
	if gotID, _ := got["dashboard_id"].(string); gotID != id {
		t.Fatalf("dashboards get dashboard_id = %q, want %q", gotID, id)
	}

	mustRunJSONObject(t, r, "dashboards", "update", id, "--input",
		writeBodyFile(t, map[string]any{"name": name + "-updated"}))

	mustExitZero(t, r, "dashboards", "delete", id, "--yes")
}

func testSavedSearchCRUD(t *testing.T, r *Runner, logID string) {
	name := resourceName("saved-search")

	created := mustRunJSONObject(t, r, "saved-searches", "create", "--input",
		writeBodyFile(t, savedSearchBody(name, logID)))
	id, _ := created["saved_search_id"].(string)
	if id == "" {
		t.Fatalf("saved-searches create response missing saved_search_id: %+v", created)
	}
	t.Cleanup(func() { bestEffortDelete(r, "saved-searches", id) })

	assertListContainsName(t, r, "saved-searches", name)

	got := mustRunJSONObject(t, r, "saved-searches", "get", id)
	if gotID, _ := got["saved_search_id"].(string); gotID != id {
		t.Fatalf("saved-searches get saved_search_id = %q, want %q", gotID, id)
	}

	// UpdateSavedSearchRequest requires log_ids/name/search_details even
	// for an update (no partial-patch shape in the spec), so resend a full
	// body with a changed name.
	updated := savedSearchBody(name+"-updated", logID)
	mustRunJSONObject(t, r, "saved-searches", "update", id, "--input", writeBodyFile(t, updated))

	mustExitZero(t, r, "saved-searches", "delete", id, "--yes")
}

func testAPIKeyCRUD(t *testing.T, r *Runner) {
	name := resourceName("api-key")

	created := mustRunJSONObject(t, r, "api-keys", "create", "--input",
		writeBodyFile(t, apiKeyBody(name)))
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("api-keys create response missing id: %+v", redactSecrets(created))
	}
	t.Cleanup(func() { bestEffortDelete(r, "api-keys", id) })

	// api-keys has NoGet set (resourceRegistry): no `bronto api-keys get`.
	assertListContainsName(t, r, "api-keys", name)

	mustRunJSONObject(t, r, "api-keys", "update", id, "--input",
		writeBodyFile(t, map[string]any{"name": name + "-updated"}))

	mustExitZero(t, r, "api-keys", "delete", id, "--yes")
}

// --- request body builders -------------------------------------------------

// monitorBody builds a minimal CreateMonitorRequest (api/openapi.yaml):
// required fields are name, filter, stat, comparison_operator, threshold,
// window, logs, actions. threshold is set absurdly high so the monitor
// never actually fires during the test's lifetime.
func monitorBody(name, logID string, threshold int) map[string]any {
	return map[string]any{
		"name": name,
		// A real query expression: bare "*" is not valid query syntax.
		"filter":              "status >= 0",
		"stat":                "COUNT",
		"comparison_operator": "ABOVE",
		"threshold":           threshold,
		// Format per the documented pattern-monitor example ("Last N minutes").
		"window": "Last 10 minutes",
		"logs":   []string{logID},
		"actions": []map[string]any{
			{"type": "EMAIL", "email": "bronto-ci@example.com"},
		},
	}
}

// savedSearchBody builds a minimal CreateSavedSearchRequest: required
// fields are name, created_by, log_ids, search_details.
func savedSearchBody(name, logID string) map[string]any {
	return map[string]any{
		"name":       name,
		"created_by": "bronto-ci",
		"log_ids":    []string{logID},
		// SearchDetails' oneOf variants all require from + where + a time
		// field (same contract the exports command satisfies): from is a
		// colon-separated log-id string, where always present (may be empty).
		"search_details": map[string]any{
			"from":       logID,
			"time_range": "Last 15 minutes",
			"where":      "status >= 0",
		},
	}
}

// apiKeyBody builds a minimal CreateApiKeyRequest: required fields are
// name, roles.
//
// UNCERTAIN: openapi.yaml types `roles` as a bare `object` with no
// `properties`, but every example in the spec (CreateApiKeyRequest,
// UpdateApiKeyRequest, ApiKey) shows a JSON array of role-id strings, e.g.
// ["IngestionApi"]. Using the array form here per those examples; if a
// live 400 says otherwise, this is the first place to look.
func apiKeyBody(name string) map[string]any {
	return map[string]any{
		"name":  name,
		"roles": []string{"SearchApi"},
	}
}

// --- shared CRUD helpers ----------------------------------------------------

// firstDatasetLogID fetches the log_id of the first dataset in the account
// (via `bronto datasets list`, which datasets.Base "/logs" - see
// resourceRegistry - responds to with Log objects keyed by log_id). Skips
// the calling test if the account has no datasets configured.
func firstDatasetLogID(t *testing.T, r *Runner) string {
	t.Helper()
	rows := mustRunJSONArray(t, r, "datasets", "list", "-o", "json")
	if len(rows) == 0 {
		t.Skip("no datasets in this account; monitors/saved-searches CRUD needs at least one existing log")
	}
	id, _ := rows[0]["log_id"].(string)
	if id == "" {
		t.Fatalf("dataset row missing log_id: %+v", rows[0])
	}
	return id
}

func writeBodyFile(t *testing.T, body map[string]any) string {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshaling request body: %v", err)
	}
	path := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("writing request body file: %v", err)
	}
	return path
}

func mustExitZero(t *testing.T, r *Runner, args ...string) Result {
	t.Helper()
	res, err := r.Run(t.Context(), "", args...)
	if err != nil {
		t.Fatalf("running %v: %v", args, err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("%v exited %d\nstdout: %s\nstderr: %s", args, res.ExitCode, res.Stdout, res.Stderr)
	}
	return res
}

func mustRunJSONObject(t *testing.T, r *Runner, args ...string) map[string]any {
	t.Helper()
	res := mustExitZero(t, r, args...)
	var obj map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &obj); err != nil {
		t.Fatalf("parsing JSON object from %v: %v\noutput: %s", args, err, res.Stdout)
	}
	return obj
}

func mustRunJSONArray(t *testing.T, r *Runner, args ...string) []map[string]any {
	t.Helper()
	res := mustExitZero(t, r, args...)
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("parsing JSON array from %v: %v\noutput: %s", args, err, res.Stdout)
	}
	return rows
}

func assertListContainsName(t *testing.T, r *Runner, kind, name string) {
	t.Helper()
	rows := mustRunJSONArray(t, r, kind, "list", "-o", "json")
	for _, row := range rows {
		if n, _ := row["name"].(string); n == name {
			return
		}
	}
	t.Fatalf("%s list does not contain %q", kind, name)
}

// bestEffortDelete is used from t.Cleanup: the resource may already be
// deleted by the test body itself, in which case this 404s harmlessly.
func bestEffortDelete(r *Runner, kind, id string) {
	_, _ = r.Run(context.Background(), "", kind, "delete", id, "--yes")
}

// redactSecrets returns a shallow copy of m with every key/token-like field
// (any key containing "key" or "token", case-insensitive) replaced by a
// fixed placeholder, so failure output can safely embed the rest of a
// response with %+v. The api-keys create response in particular carries an
// "api_key" field (ApiKey.api_key, api/openapi.yaml — only the key's first
// 8 characters, but still credential material, not something to echo into
// test logs or CI output on a whim).
func redactSecrets(m map[string]any) map[string]any {
	redacted := make(map[string]any, len(m))
	for k, v := range m {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "key") || strings.Contains(lk, "token") {
			redacted[k] = "[REDACTED]"
			continue
		}
		redacted[k] = v
	}
	return redacted
}
