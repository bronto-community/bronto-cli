package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeOrg is an in-memory Bronto account with the endpoints dashboards
// apply/export/validate use, behaving like the live API did on
// 2026-10-21: create bodies drop tags (tags go through /tags), a formula
// comes back with aggregation {reduce_to: avg}, definitions have no
// update, deleting a widget detaches it, and the dashboards list carries
// an empty widget_ids.
type fakeOrg struct {
	t          *testing.T
	mu         sync.Mutex
	next       int
	dashboards map[string]map[string]any
	widgets    map[string]map[string]any
	defs       map[string]map[string]any
	datasets   []map[string]any
	writes     []string // "METHOD path" of every non-GET request
	series     float64  // the value /timeseries/search returns
	searchErr  bool
}

func newFakeOrg(t *testing.T) *fakeOrg {
	return &fakeOrg{t: t, dashboards: map[string]map[string]any{}, widgets: map[string]map[string]any{},
		defs: map[string]map[string]any{}, series: 3,
		datasets: []map[string]any{
			{"log_id": "aaaaaaaa-aaaa-aaaa-aaaa-00000000d501", "log": "checkout-api", "collection": ".traces"},
			{"log_id": "aaaaaaaa-aaaa-aaaa-aaaa-00000000d502", "log": "payments", "collection": ".traces"},
			{"log_id": "aaaaaaaa-aaaa-aaaa-aaaa-00000000d503", "log": "payments", "collection": "prod"},
		}}
}

func (f *fakeOrg) id() string {
	f.next++
	return fmt.Sprintf("aaaaaaaa-aaaa-aaaa-aaaa-%012d", f.next)
}

func (f *fakeOrg) resetWrites() { f.mu.Lock(); f.writes = nil; f.mu.Unlock() }

func (f *fakeOrg) writeCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.writes) }

func clone(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func strs(v any) []string {
	arr, _ := v.([]any)
	out := []string{}
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (f *fakeOrg) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		if err := json.Unmarshal(b, &body); err != nil {
			f.t.Errorf("%s %s: body isn't JSON: %s", r.Method, r.URL.Path, b)
		}
	}
	if r.Method != http.MethodGet {
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
	}
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/logs":
		reply(200, map[string]any{"logs": f.datasets})
	case r.Method == http.MethodGet && r.URL.Path == "/dashboards":
		var rows []any
		for _, id := range sortedMapKeys(f.dashboards) {
			d := clone(f.dashboards[id])
			d["widget_ids"] = []any{} // like the live list
			rows = append(rows, d)
		}
		reply(200, map[string]any{"dashboards": rows})
	case r.Method == http.MethodPost && r.URL.Path == "/dashboards":
		id := f.id()
		f.dashboards[id] = map[string]any{"id": id, "name": body["name"], "description": orStr(body["description"]),
			"widget_ids": []any{}, "layout": map[string]any{"widget_layouts": []any{}}, "tags": map[string]any{}}
		reply(201, f.dashboards[id])
	case len(parts) == 2 && parts[0] == "dashboards":
		d := f.dashboards[parts[1]]
		if d == nil {
			reply(404, map[string]any{"message": "not found"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			reply(200, d)
		case http.MethodPatch:
			if _, ok := body["layout"]; ok && body["name"] == nil {
				d["layout"] = body["layout"]
				reply(200, d)
				return
			}
			reply(400, map[string]any{"message": "No schema matches"})
		default:
			reply(405, nil)
		}
	case len(parts) == 3 && parts[0] == "dashboards" && parts[2] == "widgets" && r.Method == http.MethodPost:
		d := f.dashboards[parts[1]]
		ids := strs(d["widget_ids"])
		for _, wid := range strs(body["widget_ids"]) {
			if f.widgets[wid] == nil {
				reply(404, nil)
				return
			}
			ids = append(ids, wid)
		}
		d["widget_ids"] = toAny(ids)
		reply(204, nil)
	case len(parts) == 4 && parts[0] == "dashboards" && parts[2] == "widgets" && r.Method == http.MethodDelete:
		d := f.dashboards[parts[1]]
		d["widget_ids"] = toAny(without(strs(d["widget_ids"]), parts[3]))
		reply(204, nil)
	case r.Method == http.MethodGet && r.URL.Path == "/widgets":
		var rows []any
		for _, id := range sortedMapKeys(f.widgets) {
			rows = append(rows, f.widgets[id])
		}
		reply(200, map[string]any{"widgets": rows})
	case r.Method == http.MethodPost && r.URL.Path == "/widgets":
		if body["name"] == nil || body["type"] == nil {
			reply(400, map[string]any{"message": "No schema matches"})
			return
		}
		id := f.id()
		wd := map[string]any{"id": id, "tags": map[string]any{}, "aux": map[string]any{}, "widget_ids": []any{}}
		f.fillWidget(wd, body)
		f.widgets[id] = wd
		reply(201, wd)
	case len(parts) == 2 && parts[0] == "widgets":
		wd := f.widgets[parts[1]]
		if wd == nil {
			reply(404, nil)
			return
		}
		switch r.Method {
		case http.MethodGet:
			reply(200, wd)
		case http.MethodPut:
			f.fillWidget(wd, body)
			reply(204, nil)
		case http.MethodDelete:
			delete(f.widgets, parts[1])
			for _, d := range f.dashboards {
				d["widget_ids"] = toAny(without(strs(d["widget_ids"]), parts[1]))
			}
			reply(204, nil)
		}
	case r.Method == http.MethodGet && r.URL.Path == "/metrics/definitions":
		var rows []any
		for _, id := range sortedMapKeys(f.defs) {
			rows = append(rows, f.defs[id])
		}
		reply(200, map[string]any{"definitions": rows})
	case r.Method == http.MethodPost && r.URL.Path == "/metrics/definitions":
		id := f.id()
		d := clone(body)
		d["id"] = id
		delete(d, "tags")
		if d["description"] == nil {
			d["description"] = ""
		}
		formulas, _ := d["formulas"].([]any)
		for _, fm := range formulas {
			m := fm.(map[string]any)
			if m["aggregation"] == nil {
				m["aggregation"] = map[string]any{"reduce_to": "avg"}
			}
		}
		if formulas == nil {
			d["formulas"] = []any{}
		}
		queries, _ := d["queries"].([]any)
		for i, q := range queries {
			m := q.(map[string]any)
			m["id"] = fmt.Sprintf("q-%s-%d", id, i)
			if _, ok := m["where"]; !ok {
				reply(400, map[string]any{"message": "No schema matches"})
				return
			}
		}
		f.defs[id] = d
		reply(201, d)
	case len(parts) == 3 && parts[0] == "metrics" && parts[1] == "definitions":
		switch r.Method {
		case http.MethodDelete:
			delete(f.defs, parts[2])
			reply(204, nil)
		case http.MethodGet:
			if d := f.defs[parts[2]]; d != nil {
				reply(200, d)
				return
			}
			reply(500, nil)
		default:
			reply(405, nil)
		}
	case r.URL.Path == "/tags":
		ent, _ := body["entity"].(map[string]any)
		id, _ := ent["id"].(string)
		var target map[string]any
		switch ent["type"] {
		case "dashboard":
			target = f.dashboards[id]
		case "widget":
			target = f.widgets[id]
		default:
			reply(400, map[string]any{"message": "Input doesn't match one of allowed values of enum"})
			return
		}
		if target == nil {
			reply(404, nil)
			return
		}
		tags, _ := target["tags"].(map[string]any)
		name, _ := body["name"].(string)
		switch r.Method {
		case http.MethodPost, http.MethodPut:
			tags[name] = body["value"]
			reply(200, body)
		case http.MethodDelete:
			delete(tags, name)
			reply(204, nil)
		}
	case r.Method == http.MethodPost && r.URL.Path == "/timeseries/search":
		if f.searchErr {
			reply(400, map[string]any{"message": "Invalid query."})
			return
		}
		out := map[string]any{}
		queries, _ := body["queries"].([]any)
		for _, q := range queries {
			name := q.(map[string]any)["name"].(string)
			out[name] = map[string]any{"name": name,
				"aggregates": []any{map[string]any{"function": "COUNT", "value": f.series}},
				"series":     []any{map[string]any{"value": fmt.Sprint(f.series)}}}
		}
		reply(200, out)
	default:
		f.t.Errorf("fake org: unexpected %s %s", r.Method, r.URL.Path)
		reply(404, nil)
	}
}

// fillWidget applies a create/PUT body: a full replacement of the fields
// the API takes, keeping id, tags and widget_ids.
func (f *fakeOrg) fillWidget(wd, body map[string]any) {
	wd["name"], wd["type"] = body["name"], body["type"]
	wd["description"] = orStr(body["description"])
	if aux, ok := body["aux"]; ok {
		wd["aux"] = aux
	} else {
		wd["aux"] = map[string]any{}
	}
	ids := toAny(strs(body["metric_ids"]))
	wd["metric_ids"] = ids
	var sources []any
	for _, id := range ids {
		sources = append(sources, map[string]any{"type": "metric", "id": id})
	}
	wd["sources"] = sources
}

func orStr(v any) string { s, _ := v.(string); return s }

func toAny(ss []string) []any {
	out := make([]any, 0, len(ss))
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}

func without(ss []string, drop string) []string {
	var out []string
	for _, s := range ss {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}

func sortedMapKeys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// dashboardByName returns the fake's dashboard with this name, or nil.
func (f *fakeOrg) dashboardByName(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.dashboards {
		if d["name"] == name {
			return d
		}
	}
	return nil
}

func (f *fakeOrg) widgetByName(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.widgets {
		if w["name"] == name {
			return w
		}
	}
	return nil
}
