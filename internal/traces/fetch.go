package traces

import (
	"context"
	"fmt"
	"strings"

	"github.com/svrnm/bronto-cli/internal/bronto"
)

type ListOptions struct {
	Service       string
	Operation     string
	MinDurationMS float64
	ErrorsOnly    bool
	Limit         int
}

func (a *Aggregator) ListSpans(ctx context.Context, opts ListOptions) ([]map[string]any, error) {
	var clauses []string
	if opts.Service != "" {
		clauses = append(clauses, "$service.name = "+Quote(opts.Service))
	}
	if opts.Operation != "" {
		clauses = append(clauses, "$span.name = "+Quote(opts.Operation))
	}
	if opts.MinDurationMS > 0 {
		clauses = append(clauses, fmt.Sprintf("$span.duration_nano > %d", int64(opts.MinDurationMS*1e6)))
	}
	if opts.ErrorsOnly {
		clauses = append(clauses, ErrorsClause)
	}
	mrf := true
	resp, err := a.Client.Search(ctx, bronto.SearchRequest{
		FromExpr: FromExpr, Time: a.Time,
		Select: append([]string{"@time"}, SpanFields...),
		Where:  AndJoin(clauses...), Limit: opts.Limit, MostRecentFirst: &mrf,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, len(resp.EventRows()))
	for _, r := range resp.EventRows() {
		s := RowToSpan(r)
		rows = append(rows, map[string]any{
			"@time":       r["@time"],
			"service":     s.Service,
			"operation":   s.Name,
			"duration":    FormatDurationNS(s.DurationNS),
			"duration_ns": s.DurationNS,
			"status":      strings.TrimPrefix(s.Status, "STATUS_CODE_"),
			"trace_id":    s.TraceID,
			"span_id":     s.SpanID,
		})
	}
	return rows, nil
}

// FetchTraceSpans fetches every span of the given traces. /search rejects
// IN(...) with a 500, so trace ids go into OR-chains batched at 15 per
// request with a 5000-span ceiling per batch (extraction §4.2).
func (a *Aggregator) FetchTraceSpans(ctx context.Context, traceIDs []string) ([]Span, error) {
	const batchSize = 15
	mrf := false
	var spans []Span
	for start := 0; start < len(traceIDs); start += batchSize {
		end := start + batchSize
		if end > len(traceIDs) {
			end = len(traceIDs)
		}
		clauses := make([]string, 0, end-start)
		for _, id := range traceIDs[start:end] {
			clauses = append(clauses, "$span.trace_id = "+Quote(id))
		}
		resp, err := a.Client.Search(ctx, bronto.SearchRequest{
			FromExpr: FromExpr, Time: a.Time,
			Select: SpanFields, Where: strings.Join(clauses, " OR "),
			Limit: 5000, MostRecentFirst: &mrf,
		})
		if err != nil {
			return nil, err
		}
		for _, row := range resp.EventRows() {
			spans = append(spans, RowToSpan(row))
		}
	}
	return spans, nil
}

// FindSampleTraceIDs returns up to sample distinct trace ids from the most
// recent spans matching where (not a uniform random sample; extraction §5.8).
func (a *Aggregator) FindSampleTraceIDs(ctx context.Context, where string, sample int) ([]string, error) {
	limit := sample * 3
	if limit < 30 {
		limit = 30
	}
	mrf := true
	resp, err := a.Client.Search(ctx, bronto.SearchRequest{
		FromExpr: FromExpr, Time: a.Time,
		Select: []string{"$span.trace_id"}, Where: where,
		Limit: limit, MostRecentFirst: &mrf,
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var ids []string
	for _, row := range resp.EventRows() {
		id := str(row, "$span.trace_id")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		if len(ids) >= sample {
			break
		}
	}
	return ids, nil
}
