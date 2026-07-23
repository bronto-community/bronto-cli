package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// ciResourceKinds lists the resource kinds the sweeper (and the CRUD suite
// in resources_crud_test.go) operate over.
//
// datasets ARE swept (unlike monitors/dashboards/saved-searches/api-keys,
// this isn't also exercised by resources_crud_test.go — see its own doc
// comment): the same anchored ^bronto-ci-(\d+)- name + 1h-age rule applies,
// and deletion is safe by construction for run-scoped names — a name only
// matches at all if this harness's own resourceName minted it, and only
// gets deleted once it's stale by an hour, comfortably past this suite's
// own wall-clock budget. The earlier blanket "datasets are riskier, so
// cleanup stays manual" exclusion is superseded by that reasoning.
//
// parsers are excluded: they're account-wide configuration (there's no
// per-run "bronto-ci-*"-named parser this harness ever creates), not a
// throwaway resource this suite leaves behind, so there's nothing for the
// sweeper to find or clean up.
// groups and webhooks joined with the wave-3 resource expansion — their
// CRUD tests mint bronto-ci-* names like every other kind.
var ciResourceKinds = []string{"monitors", "dashboards", "saved-searches", "api-keys", "datasets", "groups", "webhooks"}

// resourceIDKey maps each swept resource kind to the JSON key its API uses
// for the resource's identifier. These differ across kinds (api/openapi.yaml):
// monitors and api-keys respond with "id"; dashboards and saved-searches use
// resource-specific id keys; datasets (`bronto datasets list`, which hits
// GET /logs) respond with Log objects keyed by "log_id".
//
// The live API has been observed returning a plain "id" where the vendored
// spec documents a per-kind key (see resources_crud_test.go's resourceID),
// so staleResourceIDs treats these as the preferred key with "id" as the
// fallback — the sweeper must find stale resources in either shape.
var resourceIDKey = map[string]string{
	"monitors":       "id",
	"groups":         "id",
	"webhooks":       "id",
	"dashboards":     "dashboard_id",
	"saved-searches": "saved_search_id",
	"api-keys":       "id",
	"datasets":       "log_id",
}

// resourceNameKey maps each swept resource kind to the JSON key its list
// response uses for the human-assigned name staleResourceIDs matches the
// bronto-ci-* pattern against. Every kind but datasets uses a plain "name";
// datasets (Log objects) use "log" (api/openapi.yaml's Log schema, verified
// against seed_test.go's logIDForDataset, which resolves the same field).
var resourceNameKey = map[string]string{
	"datasets": "log",
}

// nameKeyFor returns the list-response name field for kind, defaulting to
// "name" for every kind not listed in resourceNameKey.
func nameKeyFor(kind string) string {
	if k, ok := resourceNameKey[kind]; ok {
		return k
	}
	return "name"
}

// sweepMaxAge is how old a bronto-ci-* resource must be before the sweeper
// deletes it. One hour comfortably exceeds this suite's own wall-clock
// budget, so it never touches a resource still in active use by the run
// that created it — only ones left behind by a crashed prior run.
const sweepMaxAge = time.Hour

var ciNamePattern = regexp.MustCompile(`^bronto-ci-(\d+)-`)

// ciResourceAge reports how old name is, if it matches the
// bronto-ci-<unixts>-... naming convention (resourceName in harness.go).
// ok is false for names that don't match at all (e.g. pre-existing
// resources never created by this harness).
func ciResourceAge(name string, now time.Time) (age time.Duration, ok bool) {
	m := ciNamePattern.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	ts, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return now.Sub(time.Unix(ts, 0)), true
}

// isStaleCIResource reports whether name is a bronto-ci-* resource older
// than maxAge as of now.
func isStaleCIResource(name string, now time.Time, maxAge time.Duration) bool {
	age, ok := ciResourceAge(name, now)
	return ok && age > maxAge
}

// staleResourceIDs returns the identifiers of every stale bronto-ci-*
// resource in rows, as decoded from a `bronto <kind> list -o json`
// response. nameKey selects which field holds the bronto-ci-* name to
// match against (see resourceNameKey/nameKeyFor: "name" for every kind
// except datasets, which uses "log"). The id is looked up via idKey first
// (the kind-specific key some kinds genuinely use, e.g. datasets' log_id),
// falling back to plain "id" — the shape the live API actually returns for
// dashboards and saved-searches. Without the fallback those kinds were
// silently never swept: idKey missed, the row was skipped, and best-effort
// semantics hid it forever (2026-07-23 audit).
func staleResourceIDs(rows []map[string]any, idKey, nameKey string, now time.Time, maxAge time.Duration) []string {
	var ids []string
	for _, row := range rows {
		name, _ := row[nameKey].(string)
		if name == "" || !isStaleCIResource(name, now, maxAge) {
			continue
		}
		id, _ := row[idKey].(string)
		if id == "" {
			id, _ = row["id"].(string)
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// RunSweeper lists every CI resource kind and deletes stale bronto-ci-*
// resources older than sweepMaxAge. TestMain runs this once at the start of
// a credentialed run, so a prior run that crashed mid-suite (before its own
// t.Cleanup deletes ran) self-heals on the next run instead of
// accumulating throwaway resources forever.
//
// Every kind is swept even if an earlier one errors: one kind's list/delete
// trouble (e.g. a transient 5xx) must not hide a leak in another kind, and
// this is opportunistic best-effort cleanup, not the assertion under test —
// errors.Join reports every kind's failure together rather than stopping at
// the first.
func RunSweeper(ctx context.Context, r *Runner) error {
	var errs []error
	for _, kind := range ciResourceKinds {
		if err := sweepKind(ctx, r, kind); err != nil {
			errs = append(errs, fmt.Errorf("sweeping %s: %w", kind, err))
		}
	}
	return errors.Join(errs...)
}

func sweepKind(ctx context.Context, r *Runner, kind string) error {
	res, err := r.Run(ctx, "", kind, "list", "-o", "json")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s list exited %d: %s", kind, res.ExitCode, res.Stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		return fmt.Errorf("parsing %s list output: %w", kind, err)
	}
	for _, id := range staleResourceIDs(rows, resourceIDKey[kind], nameKeyFor(kind), time.Now(), sweepMaxAge) {
		// Best-effort: one stale resource failing to delete (e.g. a race
		// with another concurrent CI run's own cleanup, or a 404 because
		// something else already removed it) must not fail the whole
		// sweep — this is opportunistic cleanup, not the assertion under
		// test.
		_, _ = r.Run(ctx, "", kind, "delete", id, "--yes")
	}
	return nil
}
