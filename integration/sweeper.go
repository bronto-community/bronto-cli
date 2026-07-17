package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// ciResourceKinds lists the resource kinds the sweeper (and the CRUD suite
// in resources_crud_test.go) operate over.
//
// datasets are deliberately excluded: deleting a log stream is riskier and
// harder to undo than deleting a monitor/dashboard/saved-search/api-key
// (it can carry retained data, other resources may reference it by id), so
// dataset cleanup stays manual for now rather than being swept
// automatically by CI.
var ciResourceKinds = []string{"monitors", "dashboards", "saved-searches", "api-keys"}

// resourceIDKey maps each swept resource kind to the JSON key its API uses
// for the resource's identifier. These differ across kinds (api/openapi.yaml):
// monitors and api-keys respond with "id"; dashboards and saved-searches use
// resource-specific id keys.
var resourceIDKey = map[string]string{
	"monitors":       "id",
	"dashboards":     "dashboard_id",
	"saved-searches": "saved_search_id",
	"api-keys":       "id",
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

// staleResourceIDs returns the identifiers (looked up via idKey) of every
// stale bronto-ci-* resource in rows, as decoded from a `bronto <kind> list
// -o json` response.
func staleResourceIDs(rows []map[string]any, idKey string, now time.Time, maxAge time.Duration) []string {
	var ids []string
	for _, row := range rows {
		name, _ := row["name"].(string)
		if name == "" || !isStaleCIResource(name, now, maxAge) {
			continue
		}
		if id, ok := row[idKey].(string); ok && id != "" {
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
func RunSweeper(ctx context.Context, r *Runner) error {
	for _, kind := range ciResourceKinds {
		if err := sweepKind(ctx, r, kind); err != nil {
			return fmt.Errorf("sweeping %s: %w", kind, err)
		}
	}
	return nil
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
	for _, id := range staleResourceIDs(rows, resourceIDKey[kind], time.Now(), sweepMaxAge) {
		// Best-effort: one stale resource failing to delete (e.g. a race
		// with another concurrent CI run's own cleanup, or a 404 because
		// something else already removed it) must not fail the whole
		// sweep — this is opportunistic cleanup, not the assertion under
		// test.
		_, _ = r.Run(ctx, "", kind, "delete", id, "--yes")
	}
	return nil
}
