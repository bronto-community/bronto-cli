package bronto

import (
	"fmt"
	"regexp"
	"sort"
)

// TailFilter applies client-side include/exclude regexes to raw lines.
type TailFilter struct {
	Include []*regexp.Regexp
	Exclude []*regexp.Regexp
}

func (f TailFilter) Match(raw string) bool {
	for _, re := range f.Include {
		if !re.MatchString(raw) {
			return false
		}
	}
	for _, re := range f.Exclude {
		if re.MatchString(raw) {
			return false
		}
	}
	return true
}

// Dedup remembers event keys across poll cycles with bounded memory:
// at capacity the oldest half (by insertion order) is evicted.
type Dedup struct {
	seen     map[string]struct{}
	order    []string
	capacity int
}

func NewDedup(capacity int) *Dedup {
	return &Dedup{seen: map[string]struct{}{}, capacity: capacity}
}

func (d *Dedup) Key(ev map[string]any) string {
	if seq, ok := ev["@sequence"]; ok {
		return fmt.Sprint(seq)
	}
	return fmt.Sprint(ev["@time"], "|", ev["@raw"])
}

func (d *Dedup) Admit(key string) bool {
	if _, dup := d.seen[key]; dup {
		return false
	}
	if len(d.order) >= d.capacity {
		half := len(d.order) / 2
		for _, old := range d.order[:half] {
			delete(d.seen, old)
		}
		d.order = append([]string(nil), d.order[half:]...)
	}
	d.seen[key] = struct{}{}
	d.order = append(d.order, key)
	return true
}

// SortEvents orders a poll batch by @time, with numeric @sequence as the
// tiebreaker (0 when absent). A single lexicographic key keeps the
// ordering total even when only some events carry a sequence.
func SortEvents(evs []map[string]any) {
	sort.SliceStable(evs, func(i, j int) bool {
		ti, tj := fmt.Sprint(evs[i]["@time"]), fmt.Sprint(evs[j]["@time"])
		if ti != tj {
			return ti < tj
		}
		si, _ := numeric(evs[i]["@sequence"])
		sj, _ := numeric(evs[j]["@sequence"])
		return si < sj
	})
}

func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}
