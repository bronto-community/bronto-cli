package bronto

import (
	"fmt"
	"regexp"
	"testing"
)

func TestTailFilter(t *testing.T) {
	f := TailFilter{
		Include: []*regexp.Regexp{regexp.MustCompile(`error`)},
		Exclude: []*regexp.Regexp{regexp.MustCompile(`healthz`)},
	}
	cases := []struct {
		raw  string
		want bool
	}{
		{"an error occurred", true},
		{"error in healthz probe", false},
		{"all fine", false},
	}
	for _, c := range cases {
		if got := f.Match(c.raw); got != c.want {
			t.Errorf("Match(%q) = %v", c.raw, got)
		}
	}
	if !(TailFilter{}).Match("anything") {
		t.Error("empty filter must pass everything")
	}
}

func TestDedupAdmitOnce(t *testing.T) {
	d := NewDedup(100)
	ev := map[string]any{"@sequence": float64(42), "@raw": "x"}
	k := d.Key(ev)
	if !d.Admit(k) {
		t.Fatal("first admit must succeed")
	}
	if d.Admit(k) {
		t.Fatal("second admit must fail")
	}
	// fallback key without sequence
	k2 := d.Key(map[string]any{"@time": "t1", "@raw": "y"})
	if k2 == "" || k2 == k {
		t.Fatalf("fallback key = %q", k2)
	}
}

func TestDedupEvictsOldestHalfAtCapacity(t *testing.T) {
	d := NewDedup(4)
	for i := 0; i < 4; i++ {
		d.Admit(fmt.Sprint(i))
	}
	d.Admit("4") // triggers eviction of "0","1"
	if !d.Admit("0") {
		t.Error("evicted key 0 must be admittable again")
	}
	if d.Admit("3") {
		t.Error("key 3 must still be remembered")
	}
}

func TestSortEventsBySequenceThenTime(t *testing.T) {
	evs := []map[string]any{
		{"@sequence": float64(3), "@raw": "c"},
		{"@sequence": float64(1), "@raw": "a"},
		{"@sequence": float64(2), "@raw": "b"},
	}
	SortEvents(evs)
	if evs[0]["@raw"] != "a" || evs[2]["@raw"] != "c" {
		t.Fatalf("sorted = %v", evs)
	}
	byTime := []map[string]any{
		{"@time": "2026-07-07T12:00:02Z"},
		{"@time": "2026-07-07T12:00:01Z"},
	}
	SortEvents(byTime)
	if byTime[0]["@time"] != "2026-07-07T12:00:01Z" {
		t.Fatalf("time sort = %v", byTime)
	}
}
