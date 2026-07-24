package timerange

import (
	"testing"
	"time"
)

// FuzzResolveSince fuzzes --since parsing. The load-bearing invariant: a
// resolved absolute range must never be inverted (FromTs > ToTs). That is
// exactly the silent int64-overflow class fixed in #66 — fuzzing pins that
// the guard holds for ALL accepted inputs, not just the tested examples.
// Resolve must also never panic. (Also satisfies Scorecard's Fuzzing check.)
func FuzzResolveSince(f *testing.F) {
	for _, s := range []string{
		"1h", "30s", "1h30m", "2d", "1w", "007m", "0s",
		"300000000000h1m", "9999999999w1d", "99999999999999999999h",
		"1h2h3h", "", "abc", "1", "1x",
	} {
		f.Add(s)
	}
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	f.Fuzz(func(t *testing.T, since string) {
		spec, err := Resolve(since, "", "", now)
		if err != nil {
			return // rejected input is fine
		}
		if spec.TimeRange == "" && spec.FromTs > spec.ToTs {
			t.Fatalf("Resolve(%q) produced an inverted range: FromTs %d > ToTs %d",
				since, spec.FromTs, spec.ToTs)
		}
	})
}
