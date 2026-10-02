package mock

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// tailConfig controls how `bronto tail` sees the frozen world: each poll
// reveals PerPoll more matching events from Start onward, up to MaxEvents,
// then nothing new arrives. That makes a follow-mode recording look live
// while staying deterministic: run long enough and the output is always
// the same MaxEvents lines. Override it with docs/testdata/mock/tail.json.
type tailConfig struct {
	Start     time.Time `json:"start"`
	PerPoll   int       `json:"per_poll"`
	MaxEvents int       `json:"max_events"`
}

var defaultTail = tailConfig{
	Start:     time.Date(2026, 7, 19, 9, 20, 30, 0, time.UTC),
	PerPoll:   3,
	MaxEvents: 12,
}

// isTailPoll recognizes the poll request `bronto tail` sends: oldest
// first, projecting exactly the fields its line renderer needs.
func isTailPoll(req searchReq) bool {
	if req.MostRecentFirst == nil || *req.MostRecentFirst {
		return false
	}
	has := map[string]bool{}
	for _, s := range req.Select {
		has[s] = true
	}
	return has["@sequence"] && has["@origin"] && has["@raw"]
}

// tailBatch returns the events the next poll of this tail (identified by
// its scope and query) should see.
func (s *Server) tailBatch(req searchReq, matched []map[string]any) []map[string]any {
	key := strings.Join(req.From, ",") + "|" + req.FromExpr + "|" + req.Where
	s.mu.Lock()
	s.tailPolls[key]++
	poll := s.tailPolls[key]
	s.mu.Unlock()

	sorted := append([]map[string]any(nil), matched...)
	sort.SliceStable(sorted, func(i, j int) bool { return eventMillis(sorted[i]) < eventMillis(sorted[j]) })
	start := s.tail.Start.UnixMilli()
	var stream []map[string]any
	for _, r := range sorted {
		if eventMillis(r) >= start {
			stream = append(stream, r)
		}
	}
	if s.tail.MaxEvents > 0 && len(stream) > s.tail.MaxEvents {
		stream = stream[:s.tail.MaxEvents]
	}
	n := poll * s.tail.PerPoll
	if n > len(stream) {
		n = len(stream)
	}
	// A sliding window over the stream: the previous poll's events come
	// back too, as they would within a real --window, and tail's dedup
	// drops them.
	from := n - 2*s.tail.PerPoll
	if from < 0 {
		from = 0
	}
	return stream[from:n]
}

// serveContext answers GET /context: the events around an anchor
// (from=<log id>, sequence=<n>) in that dataset.
func (s *Server) serveContext(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	logID := q.Get("from")
	events, ok := s.events[logID]
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown log id \""+logID+"\"")
		return
	}
	seq := q.Get("sequence")
	anchor := -1
	for i, ev := range events {
		if meta, ok := ev["metadata"].(map[string]any); ok {
			if n, ok := meta["sequence"].(json.Number); ok && n.String() == seq {
				anchor = i
				break
			}
		}
	}
	if anchor < 0 {
		writeError(w, http.StatusNotFound, "no event with sequence "+seq+" in log "+logID)
		return
	}
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 {
		limit = 50
	}
	lo, hi := max(0, anchor-limit), min(len(events), anchor+1+limit)
	switch q.Get("direction") {
	case "before":
		hi = anchor + 1
	case "after":
		lo = anchor
	}
	result := make([]map[string]any, 0, hi-lo)
	for _, ev := range events[lo:hi] {
		row := map[string]any{}
		for k, v := range ev {
			row[k] = v
		}
		if meta, ok := ev["metadata"].(map[string]any); ok {
			row["@sequence"] = meta["sequence"]
		}
		result = append(result, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"explain": explain, "result": result})
}

// serveTopKeys answers GET /top-keys in the live nested shape
// ({log_id: {key: {type, field_type, rank, values}}}), computed from the
// message_kvs of the fixture events. rank is the number of events
// carrying the key; values is a sample of up to 8 distinct values.
func (s *Server) serveTopKeys(w http.ResponseWriter, r *http.Request) {
	logID := r.URL.Query().Get("log_id")
	var ids []string
	if logID != "" {
		if _, ok := s.events[logID]; !ok {
			writeError(w, http.StatusBadRequest, "unknown log id \""+logID+"\"")
			return
		}
		ids = []string{logID}
	} else {
		for _, d := range s.datasets {
			if id, _ := d["log_id"].(string); id != "" {
				ids = append(ids, id)
			}
		}
	}
	out := map[string]any{}
	for _, id := range ids {
		keys := map[string]map[string]any{}
		for _, ev := range s.events[id] {
			kvs, _ := ev["message_kvs"].(map[string]any)
			for k, v := range kvs {
				meta := keys[k]
				if meta == nil {
					typ := "STRING"
					if _, ok := v.(json.Number); ok {
						typ = "NUMBER"
					}
					meta = map[string]any{"type": typ, "field_type": "MESSAGE_KVP", "rank": 0, "values": map[string]any{}}
					keys[k] = meta
				}
				meta["rank"] = meta["rank"].(int) + 1
				vals := meta["values"].(map[string]any)
				sv := valueText(v)
				if _, seen := vals[sv]; !seen && len(vals) < 8 {
					vals[sv] = map[string]any{"rank": 0}
				}
			}
		}
		perLog := map[string]any{}
		for k, meta := range keys {
			perLog[k] = meta
		}
		out[id] = perLog
	}
	writeJSON(w, http.StatusOK, out)
}

func valueText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}
