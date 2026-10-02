package mock

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bronto-community/bronto-cli/internal/query"
)

// searchReq is the subset of the /search parameters the mock honours.
// The time range (time_range, from_ts, to_ts) is accepted and ignored on
// purpose: the docs world is frozen in time, so --since 1h always returns
// the same data.
type searchReq struct {
	From            []string
	FromExpr        string
	Where           string
	Select          []string
	Groups          []string
	Limit           int
	Slices          int
	MostRecentFirst *bool
	OrderBy         string
	ExplainOnly     bool
}

func parseSearchReq(r *http.Request) (searchReq, error) {
	var req searchReq
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		req.From = q["from"]
		req.FromExpr = q.Get("from_expr")
		req.Where = q.Get("where")
		req.Select = q["select"]
		req.Groups = q["groups"]
		req.OrderBy = q.Get("order_by")
		req.Limit, _ = strconv.Atoi(q.Get("limit"))
		req.Slices, _ = strconv.Atoi(q.Get("num_of_slices"))
		if v := q.Get("most_recent_first"); v != "" {
			b := v == "true"
			req.MostRecentFirst = &b
		}
		req.ExplainOnly = q.Get("explain_only") == "true"
		return req, nil
	}
	var body struct {
		From            any      `json:"from"`
		FromExpr        string   `json:"from_expr"`
		Where           string   `json:"where"`
		Select          []string `json:"select"`
		Groups          []string `json:"groups"`
		Limit           int      `json:"limit"`
		Slices          int      `json:"num_of_slices"`
		MostRecentFirst *bool    `json:"most_recent_first"`
		OrderBy         string   `json:"order_by"`
		ExplainOnly     bool     `json:"explain_only"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return req, fmt.Errorf("invalid request body: %w", err)
	}
	switch f := body.From.(type) {
	case string:
		req.From = strings.Split(f, ",")
	case []any:
		for _, v := range f {
			req.From = append(req.From, fmt.Sprint(v))
		}
	}
	req.FromExpr, req.Where, req.Select, req.Groups = body.FromExpr, body.Where, body.Select, body.Groups
	req.Limit, req.Slices, req.MostRecentFirst = body.Limit, body.Slices, body.MostRecentFirst
	req.OrderBy, req.ExplainOnly = body.OrderBy, body.ExplainOnly
	return req, nil
}

// explain is the constant query-plan block every search answers with.
var explain = map[string]any{"Execution time (millis)": "12", "Events scanned": "2440", "Bytes scanned": "1.3 MB"}

func (s *Server) serveSearch(w http.ResponseWriter, r *http.Request) {
	req, err := parseSearchReq(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.sourceRows(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	lo, hi := window(rows)
	match, err := compileWhere(req.Where)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var matched []map[string]any
	for _, row := range rows {
		if match(view(row)) {
			matched = append(matched, row)
		}
	}
	if req.ExplainOnly {
		writeJSON(w, http.StatusOK, map[string]any{"explain": explain})
		return
	}
	aggs, err := parseAggs(req.Select)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(aggs) > 0 || len(req.Groups) > 0 {
		if len(aggs) == 0 {
			aggs = []agg{{expr: "count(*)", fn: "count"}}
		}
		writeJSON(w, http.StatusOK, aggregate(matched, aggs, req, lo, hi))
		return
	}
	if isTailPoll(req) {
		matched = s.tailBatch(req, matched)
	}
	mrf := true
	if req.MostRecentFirst != nil {
		mrf = *req.MostRecentFirst
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if mrf {
			return eventMillis(matched[i]) > eventMillis(matched[j])
		}
		return eventMillis(matched[i]) < eventMillis(matched[j])
	})
	if req.OrderBy != "" {
		orderBy(matched, req.OrderBy)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	if len(matched) > limit {
		matched = matched[:limit]
	}
	result := make([]map[string]any, 0, len(matched))
	for _, row := range matched {
		result = append(result, project(row, req.Select))
	}
	events := matched
	if events == nil {
		events = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"explain": explain, "events": events, "result": result})
}

// sourceRows resolves from / from_expr to the rows to search: dataset
// events, or the spans of the .traces logset.
func (s *Server) sourceRows(req searchReq) ([]map[string]any, error) {
	var ids []string
	traces := false
	switch {
	case req.FromExpr != "":
		match, err := compileWhere(req.FromExpr)
		if err != nil {
			return nil, fmt.Errorf("invalid from_expr: %w", err)
		}
		if match(map[string]any{"logset": ".traces"}) {
			traces = true
		}
		for _, d := range s.datasets {
			v := map[string]any{"log_id": d["log_id"], "log": d["log"], "dataset": d["log"],
				"collection": d["collection"], "logset": ""}
			if match(v) {
				ids = append(ids, fmt.Sprint(d["log_id"]))
			}
		}
	case len(req.From) > 0:
		for _, id := range req.From {
			if _, ok := s.events[id]; !ok {
				return nil, fmt.Errorf("unknown log id %q", id)
			}
			ids = append(ids, id)
		}
	default:
		return nil, fmt.Errorf("one of from or from_expr must be specified")
	}
	var rows []map[string]any
	for _, id := range ids {
		rows = append(rows, s.events[id]...)
	}
	if traces {
		rows = append(rows, s.spans...)
	}
	return rows, nil
}

// existsRe rewrites "EXISTS field" (used by the traces commands) into a
// comparison the client-side matcher understands: a regex that matches
// any present value.
var existsRe = regexp.MustCompile(`(?i)\bEXISTS\s+([@$]?[A-Za-z0-9_][A-Za-z0-9_.-]*)`)

// compileWhere builds a row predicate from a Bronto WHERE expression,
// reusing the CLI's own parser and matcher (internal/query). An empty
// expression matches everything.
func compileWhere(where string) (func(map[string]any) bool, error) {
	if strings.TrimSpace(where) == "" {
		return func(map[string]any) bool { return true }, nil
	}
	expr := existsRe.ReplaceAllString(where, "$1 ~ '.*'")
	node, err := query.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("Invalid query %q: %w", where, err) //nolint:staticcheck // mirrors the live error text
	}
	m, err := query.NewMatcher(node)
	if err != nil {
		return nil, fmt.Errorf("Invalid query %q: %w", where, err) //nolint:staticcheck // mirrors the live error text
	}
	return m.Match, nil
}

// view flattens a row and adds the aliases the live query language
// resolves: bare field names for message_kvs.* ("status" for
// message_kvs.status), "$name" for the same, and @sequence / @origin /
// @timestamp for the metadata block.
func view(row map[string]any) map[string]any {
	v := map[string]any{}
	flatten(v, "", row)
	for k, val := range v {
		if rest, ok := strings.CutPrefix(k, "message_kvs."); ok {
			if _, exists := v[rest]; !exists {
				v[rest] = val
			}
			v["$"+rest] = val
		}
	}
	for alias, src := range map[string]string{"@sequence": "metadata.sequence", "@origin": "metadata.origin", "@timestamp": "metadata.timestamp"} {
		if val, ok := v[src]; ok {
			v[alias] = val
		}
	}
	if _, ok := v["@timestamp"]; !ok {
		v["@timestamp"] = json.Number(strconv.FormatInt(eventMillis(row), 10))
	}
	return v
}

func flatten(out map[string]any, prefix string, m map[string]any) {
	for k, val := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if nested, ok := val.(map[string]any); ok {
			flatten(out, key, nested)
			continue
		}
		out[key] = val
	}
}

// project builds a "result" row: the select projection of row. "*" adds
// the whole (unflattened) event.
func project(row map[string]any, selects []string) map[string]any {
	if len(selects) == 0 {
		return row
	}
	v := view(row)
	out := map[string]any{}
	for _, sel := range selects {
		if sel == "*" {
			for k, val := range row {
				if _, set := out[k]; !set {
					out[k] = val
				}
			}
			continue
		}
		if val, ok := v[sel]; ok {
			out[sel] = val
		} else {
			out[sel] = nil
		}
	}
	return out
}

// eventMillis is a row's timestamp in epoch milliseconds: metadata.timestamp
// for log events, @time for spans.
func eventMillis(row map[string]any) int64 {
	if meta, ok := row["metadata"].(map[string]any); ok {
		if n, ok := meta["timestamp"].(json.Number); ok {
			if i, err := n.Int64(); err == nil {
				return i
			}
		}
	}
	if s, ok := row["@time"].(string); ok {
		if t, err := time.Parse("2006-01-02 15:04:05.000 MST", s); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

// window is the minute-aligned time span the rows cover: the range
// histograms and sliced aggregates bucket over (the requested range is
// ignored, see searchReq).
func window(rows []map[string]any) (time.Time, time.Time) {
	var lo, hi int64
	for i, r := range rows {
		ms := eventMillis(r)
		if i == 0 || ms < lo {
			lo = ms
		}
		if ms > hi {
			hi = ms
		}
	}
	l := time.UnixMilli(lo).UTC().Truncate(time.Minute)
	h := time.UnixMilli(hi).UTC().Truncate(time.Minute).Add(time.Minute)
	return l, h
}

func orderBy(rows []map[string]any, spec string) {
	parts := strings.Fields(spec)
	if len(parts) == 0 {
		return
	}
	field := parts[0]
	desc := len(parts) > 1 && strings.EqualFold(parts[1], "desc")
	sort.SliceStable(rows, func(i, j int) bool {
		a, aok := number(view(rows[i])[field])
		b, bok := number(view(rows[j])[field])
		if !aok || !bok {
			return false
		}
		if desc {
			return a > b
		}
		return a < b
	})
}

type agg struct {
	expr  string // as requested, e.g. "count(*)"; result rows are keyed by it
	fn    string // count, sum, avg, min, max, pNN
	field string
	pct   float64
}

var aggRe = regexp.MustCompile(`^(?i)(count|sum|avg|min|max|p[0-9]{1,2}|percentile)\(\s*([^,)]*?)\s*(?:,\s*([0-9.]+)\s*)?\)$`)

// parseAggs returns the aggregate selects. Mixing aggregates with plain
// columns is rejected, as the live API does.
func parseAggs(selects []string) ([]agg, error) {
	var aggs []agg
	plain := 0
	for _, sel := range selects {
		m := aggRe.FindStringSubmatch(strings.TrimSpace(sel))
		if m == nil {
			plain++
			continue
		}
		a := agg{expr: sel, fn: strings.ToLower(m[1]), field: m[2]}
		switch {
		case a.fn == "percentile":
			p, err := strconv.ParseFloat(m[3], 64)
			if err != nil {
				return nil, fmt.Errorf("percentile needs a value: percentile(field, 95)")
			}
			a.pct = p
		case strings.HasPrefix(a.fn, "p"):
			p, _ := strconv.ParseFloat(a.fn[1:], 64)
			a.pct = p
		}
		if a.fn != "count" && (a.field == "" || a.field == "*") {
			return nil, fmt.Errorf("%s needs a field", a.fn)
		}
		aggs = append(aggs, a)
	}
	if len(aggs) > 0 && plain > 0 {
		return nil, fmt.Errorf("cannot mix aggregate functions and plain columns in select")
	}
	return aggs, nil
}

func (a agg) compute(rows []map[string]any) float64 {
	if a.fn == "count" {
		if a.field == "" || a.field == "*" {
			return float64(len(rows))
		}
		n := 0
		for _, r := range rows {
			if v := view(r)[a.field]; v != nil {
				n++
			}
		}
		return float64(n)
	}
	var vals []float64
	for _, r := range rows {
		if f, ok := number(view(r)[a.field]); ok {
			vals = append(vals, f)
		}
	}
	if len(vals) == 0 {
		return 0
	}
	switch a.fn {
	case "sum", "avg":
		var sum float64
		for _, v := range vals {
			sum += v
		}
		if a.fn == "avg" {
			return math.Round(sum/float64(len(vals))*100) / 100
		}
		return sum
	case "min", "max":
		sort.Float64s(vals)
		if a.fn == "min" {
			return vals[0]
		}
		return vals[len(vals)-1]
	default: // percentile, nearest-rank
		sort.Float64s(vals)
		rank := int(math.Ceil(a.pct / 100 * float64(len(vals))))
		if rank < 1 {
			rank = 1
		}
		return vals[rank-1]
	}
}

// aggregate answers aggregate queries in the live shapes:
//   - with groups: {"groups":[{"group":"[a, b]","count":N,"stat":"count(*)","value":V}]}
//     (one row per group and aggregate, sorted by count descending)
//   - with num_of_slices: {"result":[{"@time":..., "@timestamp":"ms", "<agg>":V}]} per bucket
//   - otherwise: {"result":[{"<agg>":V}], "totals":{"<agg>":V}}
func aggregate(rows []map[string]any, aggs []agg, req searchReq, lo, hi time.Time) map[string]any {
	if len(req.Groups) > 0 {
		type bucket struct {
			key  string
			rows []map[string]any
		}
		idx := map[string]*bucket{}
		var order []*bucket
		for _, r := range rows {
			v := view(r)
			vals := make([]string, len(req.Groups))
			for i, g := range req.Groups {
				if val, ok := v[g]; ok && val != nil {
					vals[i] = fmt.Sprint(val)
				} else {
					vals[i] = "null"
				}
			}
			key := "[" + strings.Join(vals, ", ") + "]"
			b := idx[key]
			if b == nil {
				b = &bucket{key: key}
				idx[key] = b
				order = append(order, b)
			}
			b.rows = append(b.rows, r)
		}
		sort.SliceStable(order, func(i, j int) bool {
			if len(order[i].rows) != len(order[j].rows) {
				return len(order[i].rows) > len(order[j].rows)
			}
			return order[i].key < order[j].key
		})
		limit := req.Limit
		if limit > 0 && len(order) > limit {
			order = order[:limit]
		}
		groups := make([]map[string]any, 0, len(order)*len(aggs))
		for _, b := range order {
			for _, a := range aggs {
				groups = append(groups, map[string]any{
					"group": b.key, "count": len(b.rows), "stat": a.expr, "value": a.compute(b.rows),
				})
			}
		}
		return map[string]any{"explain": explain, "groups": groups}
	}
	if req.Slices > 0 {
		step := hi.Sub(lo) / time.Duration(req.Slices)
		if step < time.Second {
			step = time.Second
		}
		result := []map[string]any{}
		for i := 0; i < req.Slices; i++ {
			from := lo.Add(time.Duration(i) * step)
			to := from.Add(step)
			var in []map[string]any
			for _, r := range rows {
				ms := eventMillis(r)
				if ms >= from.UnixMilli() && ms < to.UnixMilli() {
					in = append(in, r)
				}
			}
			row := map[string]any{
				"@time":      from.Format("Mon Jan 2 15:04:05 MST 2006"),
				"@timestamp": strconv.FormatInt(from.UnixMilli(), 10),
			}
			for _, a := range aggs {
				row[a.expr] = a.compute(in)
			}
			result = append(result, row)
		}
		return map[string]any{"explain": explain, "result": result}
	}
	row := map[string]any{}
	for _, a := range aggs {
		row[a.expr] = a.compute(rows)
	}
	return map[string]any{"explain": explain, "result": []map[string]any{row}, "totals": row}
}

func number(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}
