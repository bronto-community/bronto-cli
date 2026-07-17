// Package bronto is the service layer: request/response models and
// cross-endpoint workflows over the Bronto REST API (spec §4).
package bronto

import (
	"sort"

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

func (r *SearchResponse) EventRows() []map[string]any {
	if len(r.Events) > 0 {
		return r.Events
	}
	return r.Result
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

var priorityColumns = []string{"@time", "@status", "@raw"}

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
