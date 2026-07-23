// Package bronto is the service layer: request/response models and
// cross-endpoint workflows over the Bronto REST API (spec §4).
package bronto

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/timerange"
)

type SearchRequest struct {
	From            []string
	FromExpr        string
	Time            timerange.Spec
	Where           string
	Select          []string
	Groups          []string
	Limit           int
	Slices          int
	MostRecentFirst *bool
	OrderBy         string
	ExplainOnly     bool
}

func (r SearchRequest) Body() map[string]any {
	b := map[string]any{}
	if len(r.From) > 0 {
		b["from"] = r.From
	}
	if r.FromExpr != "" {
		b["from_expr"] = r.FromExpr
	}
	if r.Time.TimeRange != "" {
		b["time_range"] = r.Time.TimeRange
	} else if r.Time.FromTs != 0 || r.Time.ToTs != 0 {
		b["from_ts"] = r.Time.FromTs
		b["to_ts"] = r.Time.ToTs
	}
	if r.Where != "" {
		b["where"] = r.Where
	}
	if len(r.Select) > 0 {
		b["select"] = r.Select
	}
	if len(r.Groups) > 0 {
		b["groups"] = r.Groups
	}
	if r.Limit > 0 {
		b["limit"] = r.Limit
	}
	if r.Slices > 0 {
		b["num_of_slices"] = r.Slices
	}
	if r.MostRecentFirst != nil {
		b["most_recent_first"] = *r.MostRecentFirst
	}
	if r.OrderBy != "" {
		b["order_by"] = r.OrderBy
	}
	if r.ExplainOnly {
		b["explain_only"] = true
	}
	return b
}

type SearchResponse struct {
	Explain      map[string]any   `json:"explain"`
	Result       []map[string]any `json:"result"`
	Events       []map[string]any `json:"events"`
	Groups       []map[string]any `json:"groups"`
	GroupsSeries []map[string]any `json:"groups_series"`
	Totals       map[string]any   `json:"totals"`
	Pagination   struct {
		NextPageURL string `json:"next_page_url"`
	} `json:"pagination"`
}

// EventRows returns FULL event objects (@raw, message_kvs, metadata…):
// the live API populates BOTH events (raw) and result (the select
// projection) on every search, so this is the right accessor only for
// commands presenting whole events (plain search, tail's raw lines).
func (r *SearchResponse) EventRows() []map[string]any {
	if len(r.Events) > 0 {
		return r.Events
	}
	return r.Result
}

// SelectedRows returns the select PROJECTION. Any consumer that asked for
// specific columns must read these: reading EventRows instead silently
// ignores the select — live traces commands rendered empty span fields
// for exactly that reason (found 2026-07-20).
func (r *SearchResponse) SelectedRows() []map[string]any {
	if len(r.Result) > 0 {
		return r.Result
	}
	return r.Events
}

func (r *SearchResponse) GroupRows() []map[string]any {
	rows := make([]map[string]any, 0, len(r.Groups))
	for _, g := range r.Groups {
		row := map[string]any{}
		for k, v := range g {
			if k == "group" {
				if obj, ok := v.(map[string]any); ok {
					for gk, gv := range obj {
						row[gk] = gv
					}
					continue
				}
				// live shape: a bracketed string "[a, b]" — strip the
				// brackets so tables don't render raw list syntax.
				if s, ok := v.(string); ok {
					row["group"] = strings.Trim(strings.TrimSpace(s), "[]")
					continue
				}
			}
			row[k] = v
		}
		rows = append(rows, row)
	}
	return rows
}

// Flatten converts nested maps to dotted keys: {"a":{"b":1}} -> {"a.b":1}.
func Flatten(m map[string]any) map[string]any {
	out := map[string]any{}
	flattenInto(out, "", m)
	return out
}

func flattenInto(out map[string]any, prefix string, m map[string]any) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if nested, ok := v.(map[string]any); ok {
			flattenInto(out, key, nested)
			continue
		}
		out[key] = v
	}
}

// PriorityEventKeys are the event columns that lead human tables and
// expanded views, in this order.
var PriorityEventKeys = []string{"@time", "@status", "@raw"}

var priorityColumns = PriorityEventKeys

func EventColumns(rows []map[string]any, max int) []string {
	seen := map[string]bool{}
	var discovered []string
	for _, r := range rows {
		keys := make([]string, 0, len(r))
		for k := range r {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic within a row
		for _, k := range keys {
			if !seen[k] {
				seen[k] = true
				discovered = append(discovered, k)
			}
		}
	}
	var cols []string
	for _, p := range priorityColumns {
		if seen[p] {
			cols = append(cols, p)
		}
	}
	for _, k := range discovered {
		isPriority := false
		for _, p := range priorityColumns {
			if k == p {
				isPriority = true
			}
		}
		if !isPriority {
			cols = append(cols, k)
		}
	}
	if max > 0 && len(cols) > max {
		cols = cols[:max]
	}
	return cols
}

// rawDropThreshold is the number of promoted (non-priority) columns at
// which "@raw" is dropped from the table: with that much structure on
// display the 120-rune blob only crowds out real columns, and json/raw
// output and the expanded view still carry it.
const rawDropThreshold = 3

// EventColumnsByFrequency picks human-table columns for full event rows:
// "@time" and "@status" lead, then discovered keys ranked by how many
// rows carry a real (non-null, non-empty) value for them (descending —
// the live API pads absent fields with nulls, so raw key presence says
// nothing), ties broken by how many DISTINCT values they take
// (descending — a column that varies tells you more than one that
// repeats), then alphabetically; capped at max (cap includes the leading
// keys and "@raw" when retained). Keys ending in ".timestamp" (or named
// "timestamp"/"@timestamp") are never promoted — they duplicate "@time".
// "@raw" moves to the LAST column — it is the widest cell — and is
// dropped entirely once rawDropThreshold other keys made the cut.
func EventColumnsByFrequency(rows []map[string]any, max int) []string {
	present := map[string]int{}
	distinct := map[string]map[string]struct{}{}
	for _, r := range rows {
		for k, v := range r {
			s := ""
			if v != nil {
				s = fmt.Sprint(v)
			}
			if s == "" || s == "null" {
				continue // a padded null is not presence
			}
			present[k]++
			if distinct[k] == nil {
				distinct[k] = map[string]struct{}{}
			}
			distinct[k][s] = struct{}{}
		}
	}
	discovered := make([]string, 0, len(present))
	for k := range present {
		if k == "@time" || k == "@status" || k == "@raw" {
			continue
		}
		if seg := k[strings.LastIndex(k, ".")+1:]; seg == "timestamp" || seg == "@timestamp" {
			continue
		}
		discovered = append(discovered, k)
	}
	sort.Slice(discovered, func(i, j int) bool {
		ki, kj := discovered[i], discovered[j]
		if present[ki] != present[kj] {
			return present[ki] > present[kj]
		}
		if len(distinct[ki]) != len(distinct[kj]) {
			return len(distinct[ki]) > len(distinct[kj])
		}
		return ki < kj
	})
	lead := make([]string, 0, 2)
	for _, p := range []string{"@time", "@status"} {
		if present[p] > 0 {
			lead = append(lead, p)
		}
	}
	hasRaw := present["@raw"] > 0
	if max <= 0 {
		cols := make([]string, 0, len(lead)+len(discovered)+1)
		cols = append(cols, lead...)
		cols = append(cols, discovered...)
		if hasRaw && len(discovered) < rawDropThreshold {
			cols = append(cols, "@raw")
		}
		return cols
	}
	room := max - len(lead)
	if room < 0 {
		room = 0
	}
	if hasRaw {
		withRaw := room - 1
		if withRaw < 0 {
			withRaw = 0
		}
		promotable := len(discovered)
		if promotable > withRaw {
			promotable = withRaw
		}
		if promotable >= rawDropThreshold {
			hasRaw = false
		} else {
			room = withRaw
		}
	}
	if room > len(discovered) {
		room = len(discovered)
	}
	cols := make([]string, 0, len(lead)+room+1)
	cols = append(cols, lead...)
	cols = append(cols, discovered[:room]...)
	if hasRaw {
		cols = append(cols, "@raw")
	}
	return cols
}
