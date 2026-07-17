package integration

import (
	"testing"
	"time"
)

func TestCIResourceAge(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	cases := []struct {
		name    string
		wantOK  bool
		wantAge time.Duration
	}{
		{"bronto-ci-1000000-abcd1234-monitor", true, now.Sub(time.Unix(1_000_000, 0))},
		{"bronto-ci-2000000-abcd1234-monitor", true, 0},
		{"not-a-ci-resource", false, 0},
		{"bronto-ci-notanumber-abcd1234-monitor", false, 0},
		{"production-dashboard", false, 0},
		{"", false, 0},
	}
	for _, c := range cases {
		age, ok := ciResourceAge(c.name, now)
		if ok != c.wantOK {
			t.Errorf("ciResourceAge(%q): ok = %v, want %v", c.name, ok, c.wantOK)
			continue
		}
		if ok && age != c.wantAge {
			t.Errorf("ciResourceAge(%q): age = %v, want %v", c.name, age, c.wantAge)
		}
	}
}

func TestIsStaleCIResource(t *testing.T) {
	now := time.Unix(1_000_000_000, 0)
	maxAge := time.Hour

	twoHoursAgo := now.Add(-2 * time.Hour).Unix()
	tenMinutesAgo := now.Add(-10 * time.Minute).Unix()

	cases := []struct {
		name string
		want bool
	}{
		{fmtCIName(twoHoursAgo, "old-monitor"), true},
		{fmtCIName(tenMinutesAgo, "fresh-monitor"), false},
		{"not-bronto-ci-named", false},
	}
	for _, c := range cases {
		if got := isStaleCIResource(c.name, now, maxAge); got != c.want {
			t.Errorf("isStaleCIResource(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestStaleResourceIDs(t *testing.T) {
	now := time.Unix(1_000_000_000, 0)
	maxAge := time.Hour
	old := now.Add(-2 * time.Hour).Unix()
	fresh := now.Add(-time.Minute).Unix()

	rows := []map[string]any{
		{"id": "old-1", "name": fmtCIName(old, "monitor")},
		{"id": "fresh-1", "name": fmtCIName(fresh, "monitor")},
		{"id": "unnamed-1"},                           // no name key at all
		{"id": "not-ci", "name": "hand-made-monitor"}, // not a bronto-ci-* name
		{"name": fmtCIName(old, "no-id")},             // stale but no id: must be skipped, not zero-valued
	}

	got := staleResourceIDs(rows, "id", "name", now, maxAge)
	if len(got) != 1 || got[0] != "old-1" {
		t.Fatalf("staleResourceIDs = %v, want [old-1]", got)
	}
}

func TestStaleResourceIDs_NonDefaultNameKey(t *testing.T) {
	now := time.Unix(1_000_000_000, 0)
	maxAge := time.Hour
	old := now.Add(-2 * time.Hour).Unix()

	rows := []map[string]any{
		{"log_id": "old-1", "log": fmtCIName(old, "dataset")},
		{"log_id": "no-match", "name": fmtCIName(old, "dataset")}, // wrong key: must be ignored
	}
	got := staleResourceIDs(rows, "log_id", "log", now, maxAge)
	if len(got) != 1 || got[0] != "old-1" {
		t.Fatalf("staleResourceIDs = %v, want [old-1]", got)
	}
}

func TestResourceIDKeyCoversEverySweptKind(t *testing.T) {
	for _, kind := range ciResourceKinds {
		if _, ok := resourceIDKey[kind]; !ok {
			t.Errorf("resourceIDKey has no entry for swept kind %q", kind)
		}
	}
}

func TestNameKeyForDefaultsToName(t *testing.T) {
	if got := nameKeyFor("monitors"); got != "name" {
		t.Errorf("nameKeyFor(%q) = %q, want %q", "monitors", got, "name")
	}
	if got := nameKeyFor("datasets"); got != "log" {
		t.Errorf("nameKeyFor(%q) = %q, want %q", "datasets", got, "log")
	}
}

func fmtCIName(unixTS int64, suffix string) string {
	return "bronto-ci-" + timeItoa(unixTS) + "-deadbeef-" + suffix
}

func timeItoa(n int64) string {
	// Deliberately independent of resourceName/strconv usage elsewhere, so
	// this test doesn't just echo the implementation it's exercising.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
