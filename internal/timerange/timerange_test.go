package timerange

import (
	"testing"
	"time"
)

var testNow = func() time.Time {
	return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
}

func TestSinceSingleUnit(t *testing.T) {
	cases := []struct{ in, want string }{
		{"15m", "Last 15 minutes"},
		{"1h", "Last 1 hour"},
		{"90s", "Last 90 seconds"},
		{"2d", "Last 2 days"},
		{"1w", "Last 1 week"},
	}
	for _, c := range cases {
		got, err := Resolve(c.in, "", "", testNow)
		if err != nil || got.TimeRange != c.want || got.FromTs != 0 {
			t.Errorf("Resolve(%q) = %+v, %v; want TimeRange=%q", c.in, got, err, c.want)
		}
	}
}

func TestSinceCompoundBecomesAbsolute(t *testing.T) {
	got, err := Resolve("1h30m", "", "", testNow)
	if err != nil {
		t.Fatal(err)
	}
	wantTo := testNow().UnixMilli()
	wantFrom := testNow().Add(-90 * time.Minute).UnixMilli()
	if got.TimeRange != "" || got.FromTs != wantFrom || got.ToTs != wantTo {
		t.Fatalf("got %+v, want from=%d to=%d", got, wantFrom, wantTo)
	}
}

func TestAbsoluteFromTo(t *testing.T) {
	got, err := Resolve("", "2026-07-07T10:00:00Z", "2026-07-07T11:00:00Z", testNow)
	if err != nil {
		t.Fatal(err)
	}
	from, _ := time.Parse(time.RFC3339, "2026-07-07T10:00:00Z")
	to, _ := time.Parse(time.RFC3339, "2026-07-07T11:00:00Z")
	if got.FromTs != from.UnixMilli() || got.ToTs != to.UnixMilli() || got.TimeRange != "" {
		t.Fatalf("got %+v", got)
	}
	// from alone -> to = now
	got2, err := Resolve("", "2026-07-07T10:00:00Z", "", testNow)
	if err != nil || got2.ToTs != testNow().UnixMilli() {
		t.Fatalf("from-alone: %+v, %v", got2, err)
	}
}

func TestNilNowDefaults(t *testing.T) {
	got, err := Resolve("1h30m", "", "", nil)
	if err != nil || got.ToTs == 0 {
		t.Fatalf("nil now must default to time.Now: %+v, %v", got, err)
	}
}

func TestErrors(t *testing.T) {
	if _, err := Resolve("15m", "2026-07-07T10:00:00Z", "", testNow); err == nil {
		t.Error("since+from must conflict")
	}
	if _, err := Resolve("", "", "2026-07-07T10:00:00Z", testNow); err == nil {
		t.Error("to alone must error")
	}
	for _, bad := range []string{"xyz", "m5", "5x", "", "h"} {
		if bad == "" {
			continue
		}
		if _, err := Resolve(bad, "", "", testNow); err == nil {
			t.Errorf("Resolve(%q) must error", bad)
		}
	}
	if _, err := Resolve("", "not-a-date", "", testNow); err == nil {
		t.Error("bad RFC3339 must error")
	}
}

func TestZeroSpec(t *testing.T) {
	got, err := Resolve("", "", "", testNow)
	if err != nil || !got.IsZero() {
		t.Fatalf("got %+v, %v; want zero", got, err)
	}
}

func TestSinceZeroPaddedNumerals(t *testing.T) {
	got, err := Resolve("01h", "", "", testNow)
	if err != nil || got.TimeRange != "Last 1 hour" {
		t.Fatalf("got %+v, %v; want Last 1 hour", got, err)
	}
	got2, err := Resolve("007m", "", "", testNow)
	if err != nil || got2.TimeRange != "Last 7 minutes" {
		t.Fatalf("got %+v, %v; want Last 7 minutes", got2, err)
	}
}

func TestSinceOverflowErrors(t *testing.T) {
	if _, err := Resolve("99999999999999999999h", "", "", testNow); err == nil {
		t.Fatal("overflow must error")
	}
	if _, err := Resolve("1h99999999999999999999m", "", "", testNow); err == nil {
		t.Fatal("compound overflow must error")
	}
}
