package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestExpansionListSmoke covers every wave-3 registry resource's list verb
// live: exit 0 and a parseable JSON array. Reads only — the kinds where a
// create would have side effects beyond the test account (users invites,
// encryption keys, Slack) get no live CRUD.
func TestExpansionListSmoke(t *testing.T) {
	key := skipIfNoCreds(t)
	kinds := [][]string{
		{"collections"},
		{"log-views"},
		{"limits"},
		{"encryption-keys"},
		{"forward-configs"},
		{"webhooks"},
		{"slack"},
		{"monitors", "templates"},
		{"monitors", "downtimes"},
		{"users"},
		{"groups"},
	}
	for _, kind := range kinds {
		t.Run(strings.Join(kind, "_"), func(t *testing.T) {
			t.Parallel()
			r := NewRunner(t, key)
			args := append(append([]string{}, kind...), "list", "-o", "json")
			res := mustExitZero(t, r, args...)
			var rows []any
			if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
				t.Fatalf("%v list -o json did not parse as array: %v\noutput: %s", kind, err, res.Stdout)
			}
		})
	}
}

// TestGroupsCRUD: create -> list -> get -> update (PATCH) -> delete, the
// full generic-factory path over a wave-3 resource with a per-ID GET.
func TestGroupsCRUD(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)
	name := resourceName("group")

	created := mustRunJSONObject(t, r, "groups", "create", "-f", "name="+name)
	id := resourceID(created, "group_id")
	if id == "" {
		t.Fatalf("groups create response missing id: %+v", created)
	}
	t.Cleanup(func() { bestEffortDelete(r, "groups", id) })

	assertListContainsName(t, r, "groups", name)

	// The per-ID read lags creation on the live API (observed in CI: list
	// showed the group while get still 404ed ~2s after create), so poll
	// the get instead of asserting once.
	PollUntil(t, 30*time.Second, 2*time.Second, func() (bool, error) {
		res, err := r.Run(t.Context(), "", "groups", "get", id, "-o", "json")
		if err != nil {
			return false, err
		}
		if res.ExitCode != 0 {
			return false, fmt.Errorf("groups get exited %d\nstderr: %s", res.ExitCode, res.Stderr)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(res.Stdout), &got); err != nil {
			return false, fmt.Errorf("groups get did not parse: %w", err)
		}
		if gotID := resourceID(got, "group_id"); gotID != id {
			return false, fmt.Errorf("groups get id = %q, want %q", gotID, id)
		}
		return true, nil
	})

	mustRunJSONObject(t, r, "groups", "update", id, "-f", "name="+name+"-updated")
	mustExitZero(t, r, "groups", "members", id)
	mustExitZero(t, r, "groups", "delete", id, "--yes")
}

// TestWebhooksCRUD: create -> list -> update (full-body PUT) -> delete on
// a NoGet resource. The webhook URL points at a documentation-reserved
// domain and is never triggered by this test.
func TestWebhooksCRUD(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)
	name := resourceName("webhook")

	created := mustRunJSONObject(t, r, "webhooks", "create",
		"-f", "name="+name, "-f", "url=https://bronto-ci.example.com/hook")
	id := resourceID(created, "webhook_id")
	if id == "" {
		t.Fatalf("webhooks create response missing id: %+v", created)
	}
	t.Cleanup(func() { bestEffortDelete(r, "webhooks", id) })

	assertListContainsName(t, r, "webhooks", name)

	mustRunJSONObject(t, r, "webhooks", "update", id,
		"-f", "name="+name+"-updated", "-f", "url=https://bronto-ci.example.com/hook")

	mustExitZero(t, r, "webhooks", "delete", id, "--yes")
}
