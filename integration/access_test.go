package integration

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestAccessListSmoke covers the #122 read-only resources live: exit 0 and
// a parseable JSON array. The CI account has no metrics yet, so metrics is
// checked for shape only.
func TestAccessListSmoke(t *testing.T) {
	key := skipIfNoCreds(t)
	for _, kind := range []string{"roles", "permissions", "metrics"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			mustRunJSONArray(t, NewRunner(t, key), kind, "list", "-o", "json")
		})
	}
}

// TestRolesCRUD: create -> list -> get (by id and display_name) -> full
// update -> refused partial update -> delete. roles update is a full
// replacement, so the CLI rejects a body without display_name/permissions
// before calling the API (live, a partial PUT blanks fields or 500s).
func TestRolesCRUD(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)
	name := resourceName("role")
	body := func(desc string) string {
		return writeBodyFile(t, map[string]any{
			"display_name": name, "description": desc, "role_type": "USER",
			"permissions": []string{"logs_read"},
		})
	}

	created := mustRunJSONObject(t, r, "roles", "create", "--input", body("created"))
	id := resourceID(created, "role_id")
	if id == "" {
		t.Fatalf("roles create response missing role_id: %+v", created)
	}
	t.Cleanup(func() { bestEffortDelete(r, "roles", id) })

	rows := mustRunJSONArray(t, r, "roles", "list", "-o", "json")
	if !slices.ContainsFunc(rows, func(row map[string]any) bool { return row["display_name"] == name }) {
		t.Fatalf("roles list does not contain %q", name)
	}
	for _, ref := range []string{id, name} {
		if got := resourceID(mustRunJSONObject(t, r, "roles", "get", ref), "role_id"); got != id {
			t.Fatalf("roles get %q role_id = %q, want %q", ref, got, id)
		}
	}

	updated := mustRunJSONObject(t, r, "roles", "update", id, "--input", body("updated"))
	if d, _ := updated["description"].(string); d != "updated" {
		t.Fatalf("roles update description = %q, want %q", d, "updated")
	}

	res, err := r.Run(t.Context(), "", "roles", "update", id, "-f", "description=partial")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 2 || !strings.Contains(res.Stderr, "usage_missing_fields") {
		t.Fatalf("partial roles update: exit %d, want 2 with usage_missing_fields\nstderr: %s", res.ExitCode, res.Stderr)
	}

	mustExitZero(t, r, "roles", "delete", id, "--yes")
}

// TestGroupMembershipLive: add a user to a fresh group, see it from both
// sides (groups members, users groups), remove it again. Group reads lag
// writes on the live API (see TestGroupsCRUD), so every read polls.
func TestGroupMembershipLive(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)

	users := mustRunJSONArray(t, r, "users", "list", "-o", "json")
	if len(users) == 0 {
		t.Skip("CI account has no users to add")
	}
	user, _ := users[0]["id"].(string)

	created := mustRunJSONObject(t, r, "groups", "create", "-f", "name="+resourceName("members"))
	group := resourceID(created, "group_id")
	if group == "" {
		t.Fatalf("groups create response missing group_id: %+v", created)
	}
	t.Cleanup(func() { bestEffortDelete(r, "groups", group) })

	hasMember := func() (bool, error) {
		res, err := r.Run(t.Context(), "", "groups", "members", group, "-o", "json")
		if err != nil || res.ExitCode != 0 {
			return false, fmt.Errorf("groups members: exit %d err %v\nstderr: %s", res.ExitCode, err, res.Stderr)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
			return false, err
		}
		return slices.ContainsFunc(rows, func(m map[string]any) bool { return m["member_id"] == user }), nil
	}
	poll := func(what string, want bool, check func() (bool, error)) {
		t.Helper()
		PollUntil(t, 30*time.Second, 2*time.Second, func() (bool, error) {
			got, err := check()
			if err != nil {
				return false, err
			}
			if got != want {
				return false, fmt.Errorf("%s = %v, want %v", what, got, want)
			}
			return true, nil
		})
	}

	// The group itself may not be readable yet; retry the add until it is.
	PollUntil(t, 30*time.Second, 2*time.Second, func() (bool, error) {
		res, err := r.Run(t.Context(), "", "groups", "add-members", group, "--users", user)
		if err != nil || res.ExitCode != 0 {
			return false, fmt.Errorf("add-members: exit %d err %v\nstderr: %s", res.ExitCode, err, res.Stderr)
		}
		return true, nil
	})
	poll("group has member", true, hasMember)
	poll("user lists group", true, func() (bool, error) {
		res, err := r.Run(t.Context(), "", "users", "groups", user, "-o", "json")
		if err != nil || res.ExitCode != 0 {
			return false, fmt.Errorf("users groups: exit %d err %v\nstderr: %s", res.ExitCode, err, res.Stderr)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
			return false, err
		}
		return slices.ContainsFunc(rows, func(m map[string]any) bool { return m["group_id"] == group }), nil
	})

	mustExitZero(t, r, "groups", "remove-members", group, "--users", user)
	poll("group has member", false, hasMember)
}

// TestDatasetParserLive assigns and removes a parser on a throwaway
// bronto-ci-* dataset (the sweeper deletes leaked ones). The suite
// otherwise never creates datasets; this one is empty and unseeded, so a
// parser change can't disturb the search tests' seed data.
func TestDatasetParserLive(t *testing.T) {
	key := skipIfNoCreds(t)
	r := NewRunner(t, key)
	ref := "default/" + resourceName("parser-ds")

	mustExitZero(t, r, "datasets", "create", "-f", "dataset="+strings.TrimPrefix(ref, "default/"), "-f", "collection=default")
	t.Cleanup(func() { bestEffortDelete(r, "datasets", ref) })

	parsers := mustRunJSONArray(t, r, "parsers", "list", "-o", "json")
	if len(parsers) == 0 {
		t.Skip("CI account has no parsers")
	}
	parser, _ := parsers[0]["name"].(string)

	// A new dataset can take a moment to resolve by name.
	PollUntil(t, 30*time.Second, 2*time.Second, func() (bool, error) {
		res, err := r.Run(t.Context(), "", "datasets", "parser", "set", ref, parser)
		if err != nil || res.ExitCode != 0 {
			return false, fmt.Errorf("parser set: exit %d err %v\nstderr: %s", res.ExitCode, err, res.Stderr)
		}
		return true, nil
	})
	got := mustRunJSONObject(t, r, "datasets", "parser", "get", ref)
	if n, _ := got["parser_name"].(string); n != parser {
		t.Fatalf("parser get parser_name = %q, want %q", n, parser)
	}

	mustExitZero(t, r, "datasets", "parser", "unset", ref)
	res, err := r.Run(t.Context(), "", "datasets", "parser", "get", ref)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 4 {
		t.Fatalf("parser get after unset: exit %d, want 4 (no parser assigned)\nstderr: %s", res.ExitCode, res.Stderr)
	}

	mustExitZero(t, r, "datasets", "delete", ref, "--yes")
}
