package traces

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/svrnm/bronto-cli/internal/bronto"
	"github.com/svrnm/bronto-cli/internal/timerange"
)

const (
	aggCount     = "count(*)"
	aggAvg       = "avg($span.duration_nano)"
	aggMax       = "max($span.duration_nano)"
	errorsClause = "$span.status_code = 'STATUS_CODE_ERROR'"
)

type Aggregator struct {
	Client *bronto.Client
	Time   timerange.Spec
}

type aggEntry struct {
	Vals []string
	V    float64
}

func (a *Aggregator) groupAggregate(ctx context.Context, aggregate string, groups []string, where string, limit int) (map[string]aggEntry, error) {
	resp, err := a.Client.Search(ctx, bronto.SearchRequest{
		FromExpr: FromExpr, Time: a.Time,
		Select: []string{aggregate}, Groups: groups,
		Where: where, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	rows := resp.Groups
	if len(rows) == 0 {
		rows = resp.Result
	}
	out := make(map[string]aggEntry, len(rows))
	for _, row := range rows {
		vals := parseGroup(row["group"])
		key := strings.Join(vals, "\x1f")
		out[key] = aggEntry{Vals: vals, V: toFloat(row[aggregate])}
	}
	return out, nil
}

// parseGroup normalizes the API's "group" field: list, map (sorted keys
// for determinism), bracketed string "[a, b]", or scalar.
func parseGroup(v any) []string {
	switch g := v.(type) {
	case []any:
		vals := make([]string, len(g))
		for i, item := range g {
			if item == nil {
				vals[i] = ""
				continue
			}
			vals[i] = fmt.Sprint(item)
		}
		return vals
	case map[string]any:
		keys := make([]string, 0, len(g))
		for k := range g {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		vals := make([]string, len(keys))
		for i, k := range keys {
			vals[i] = fmt.Sprint(g[k])
		}
		return vals
	case string:
		s := strings.TrimSpace(g)
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			inner := strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
			if inner == "" {
				return nil
			}
			parts := strings.Split(inner, ", ")
			return parts
		}
		return []string{s}
	case nil:
		return nil
	default:
		return []string{fmt.Sprint(g)}
	}
}

func (a *Aggregator) Services(ctx context.Context, errorsOnly bool, limit int) ([]map[string]any, error) {
	where := ""
	if errorsOnly {
		where = errorsClause
	}
	return a.threeWayRows(ctx, []string{"$service.name"}, where, limit,
		func(vals []string) map[string]any {
			return map[string]any{"service": at(vals, 0)}
		})
}

func (a *Aggregator) Operations(ctx context.Context, service string, errorsOnly bool, limit int) ([]map[string]any, error) {
	var svcClause, errClause string
	if service != "" {
		svcClause = "$service.name = " + Quote(service)
	}
	if errorsOnly {
		errClause = errorsClause
	}
	return a.threeWayRows(ctx, []string{"$service.name", "$span.name"},
		AndJoin(svcClause, errClause), limit,
		func(vals []string) map[string]any {
			return map[string]any{"service": at(vals, 0), "operation": at(vals, 1)}
		})
}

// threeWayRows runs count/avg/max over the same grouping, unions the keys
// (count is the ranking ground truth; missing entries default to 0), and
// returns rows sorted by span count descending.
func (a *Aggregator) threeWayRows(ctx context.Context, groups []string, where string, limit int, keyCols func([]string) map[string]any) ([]map[string]any, error) {
	counts, err := a.groupAggregate(ctx, aggCount, groups, where, limit)
	if err != nil {
		return nil, err
	}
	avgs, err := a.groupAggregate(ctx, aggAvg, groups, where, limit)
	if err != nil {
		return nil, err
	}
	maxes, err := a.groupAggregate(ctx, aggMax, groups, where, limit)
	if err != nil {
		return nil, err
	}
	keys := unionKeys(counts, avgs, maxes)
	rows := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		vals := entryVals(k, counts, avgs, maxes)
		row := keyCols(vals)
		row["spans"] = int64(counts[k].V)
		row["avg_ns"] = int64(avgs[k].V)
		row["max_ns"] = int64(maxes[k].V)
		row["avg"] = FormatDurationNS(int64(avgs[k].V))
		row["max"] = FormatDurationNS(int64(maxes[k].V))
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i]["spans"].(int64) > rows[j]["spans"].(int64)
	})
	return rows, nil
}

type AttrOptions struct {
	By           []string
	RootOnly     bool
	Service      string
	Kind         string
	Where        string
	ErrorsOnly   bool
	IncludeEmpty bool
	Limit        int
}

func (a *Aggregator) Attributes(ctx context.Context, opts AttrOptions) ([]map[string]any, []string, int, error) {
	groupKeys := make([]string, len(opts.By))
	for i, attr := range opts.By {
		norm, err := NormalizeAttr(attr)
		if err != nil {
			return nil, nil, 0, err
		}
		groupKeys[i] = norm
	}
	var clauses []string
	if opts.RootOnly {
		clauses = append(clauses, RootOnlyClause)
	}
	if opts.Service != "" {
		clauses = append(clauses, "$service.name = "+Quote(opts.Service))
	}
	if opts.Kind != "" {
		clauses = append(clauses, KindClause(opts.Kind))
	}
	if opts.ErrorsOnly {
		clauses = append(clauses, errorsClause)
	}
	if opts.Where != "" {
		clauses = append(clauses, "("+opts.Where+")")
	}
	where := AndJoin(clauses...)

	fetchLimit := opts.Limit * 5
	if fetchLimit < 200 {
		fetchLimit = 200
	}
	counts, err := a.groupAggregate(ctx, aggCount, groupKeys, where, fetchLimit)
	if err != nil {
		return nil, nil, 0, err
	}
	avgs, err := a.groupAggregate(ctx, aggAvg, groupKeys, where, fetchLimit)
	if err != nil {
		return nil, nil, 0, err
	}
	maxes, err := a.groupAggregate(ctx, aggMax, groupKeys, where, fetchLimit)
	if err != nil {
		return nil, nil, 0, err
	}
	var errCounts map[string]aggEntry
	if !opts.ErrorsOnly {
		errCounts, err = a.groupAggregate(ctx, aggCount, groupKeys,
			AndJoin(where, errorsClause), fetchLimit)
		if err != nil {
			return nil, nil, 0, err
		}
	}

	attrNames := make([]string, len(groupKeys))
	for i, g := range groupKeys {
		attrNames[i] = strings.TrimPrefix(g, "$")
	}

	countKeys := make([]string, 0, len(counts))
	for k := range counts {
		countKeys = append(countKeys, k)
	}
	sort.Strings(countKeys)

	dropped := 0
	rows := make([]map[string]any, 0, len(counts))
	for _, key := range countKeys {
		entry := counts[key]
		vals := entry.Vals
		for len(vals) < len(groupKeys) {
			vals = append(vals, "")
		}
		if !opts.IncludeEmpty && hasMissing(vals[:len(groupKeys)]) {
			dropped++
			continue
		}
		row := map[string]any{}
		for i, name := range attrNames {
			row[name] = labelGroupValue(vals[i])
		}
		n := int64(entry.V)
		row["spans"] = n
		if errCounts != nil {
			errN := int64(errCounts[key].V)
			row["errors"] = errN
			if n > 0 {
				row["err%"] = fmt.Sprintf("%.1f", float64(errN)/float64(n)*100)
			} else {
				row["err%"] = ""
			}
		}
		row["avg"] = FormatDurationNS(int64(avgs[key].V))
		row["max"] = FormatDurationNS(int64(maxes[key].V))
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i]["spans"].(int64) > rows[j]["spans"].(int64)
	})
	if len(rows) > opts.Limit {
		rows = rows[:opts.Limit]
	}
	columns := append([]string{}, attrNames...)
	columns = append(columns, "spans")
	if errCounts != nil {
		columns = append(columns, "errors", "err%")
	}
	columns = append(columns, "avg", "max")
	return rows, columns, dropped, nil
}

func hasMissing(vals []string) bool {
	for _, v := range vals {
		if v == "" || v == "null" || v == "None" {
			return true
		}
	}
	return false
}

func labelGroupValue(v string) string {
	if v == "" || v == "null" || v == "None" {
		return "<missing>"
	}
	return v
}

func unionKeys(maps ...map[string]aggEntry) []string {
	seen := map[string]bool{}
	var keys []string
	for _, m := range maps {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys) // deterministic pre-sort; final order set by span count
	return keys
}

func entryVals(key string, maps ...map[string]aggEntry) []string {
	for _, m := range maps {
		if e, ok := m[key]; ok && len(e.Vals) > 0 {
			return e.Vals
		}
	}
	if key == "" {
		return nil
	}
	return strings.Split(key, "\x1f")
}

func at(vals []string, i int) string {
	if i < len(vals) {
		return vals[i]
	}
	return ""
}
