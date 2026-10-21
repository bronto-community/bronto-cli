package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const (
	mDef    = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e1"
	mDef2   = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e2"
	mWidget = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e3"
)

// liveTimeseries is a trimmed GET /timeseries/{id} response in the live
// shape (EU, 2026-10-21): a grouped p95 query with a time unit_config, an
// ungrouped count whose series values are strings, and a formula.
const liveTimeseries = `{
 "requests": {"name": "requests",
   "queries": [
     {"name": "requests", "select": ["count(*)"], "where": "", "from_expr": "x"},
     {"name": "latency", "select": ["p95($span.duration_nano)"], "groups": ["$span.name"],
      "unit_config": {"type": "time", "input": "nanoseconds", "output": "auto"}}],
   "formulas": [{"name": "rate", "expression": "requests / 60"}],
   "aggregates": [{"function": "COUNT", "value": 204}, {"function": "COUNT", "value": 204.0}],
   "series": [{"@timestamp": "1", "value": "136.0"}, {"@timestamp": "2", "value": "0.0"}, {"@timestamp": "3", "value": "68.0"}],
   "groups_series": []},
 "rate": {"name": "rate", "queries": [], "formulas": [],
   "aggregates": [{"function": "COUNT", "value": 3}, {"function": "AVG", "value": 1.5}],
   "series": [{"value": "1.0"}, {"value": "2.0"}]},
 "latency": {"name": "latency", "queries": [], "formulas": [],
   "aggregates": [{"function": "COUNT", "value": 102}, {"function": "P95", "value": 412763220.4}],
   "series": [],
   "groups_series": [
     {"key": "$span.name", "name": "GET /logs", "stat": "P95", "value": 412763220.4,
      "timeseries": [{"value": 1.0}, {"value": 4.0}]},
     {"key": "$span.name", "name": "POST /pay", "stat": "P95", "value": 80000000.0,
      "timeseries": [{"value": 2.0}, {"value": 2.0}]}]}
}`

func TestTimeseriesRowsFollowDefinitionOrder(t *testing.T) {
	var payload any
	if err := json.Unmarshal([]byte(liveTimeseries), &payload); err != nil {
		t.Fatal(err)
	}
	// Only one entry carries queries/formulas, and map iteration order is
	// random: repeat so a first-entry-only scan can't pass by luck.
	var rows []map[string]any
	want := []string{"requests//count", "latency/GET /logs/p95", "latency/POST /pay/p95", "rate//avg"}
	for range 50 {
		rows = timeseriesRows(payload, "")
		var got []string
		for _, r := range rows {
			got = append(got, r["series"].(string)+"/"+r["group"].(string)+"/"+r["stat"].(string))
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("rows = %v, want %v", got, want)
		}
	}
	if rows[0]["value"] != 204.0 || rows[0]["trend"] != "█▁▄" {
		t.Fatalf("requests row = %v", rows[0])
	}
	if rows[1]["unit"] == nil || rows[0]["unit"] != nil {
		t.Fatalf("unit_config should attach to latency only: %v / %v", rows[0], rows[1])
	}
}

// A formula's own unit_config applies to its series, like a query's.
func TestTimeseriesRowsFormulaUnit(t *testing.T) {
	src := strings.Replace(liveTimeseries,
		`{"name": "rate", "expression": "requests / 60"}`,
		`{"name": "rate", "expression": "requests / 60", "unit_config": {"type": "time", "input": "nanoseconds"}}`, 1)
	var payload any
	if err := json.Unmarshal([]byte(src), &payload); err != nil {
		t.Fatal(err)
	}
	for _, r := range timeseriesRows(payload, "") {
		if r["series"] == "rate" && r["unit"] == nil {
			t.Fatalf("formula unit_config not applied: %v", r)
		}
	}
}

func TestHumanNanos(t *testing.T) {
	for in, want := range map[float64]string{
		412763220.4: "413 ms",
		80000000:    "80.0 ms",
		1250000000:  "1.25 s",
		1500:        "1.50 µs",
		12:          "12.0 ns",
		0:           "0 ns",
	} {
		if got := humanNanos(in); got != want {
			t.Errorf("humanNanos(%v) = %q, want %q", in, got, want)
		}
	}
	if got := formatSeriesValue(5, map[string]any{"type": "time", "input": "seconds"}); got != "5.00 s" {
		t.Errorf("seconds input = %q", got)
	}
	if got := formatSeriesValue(5, map[string]any{"type": "bytes"}); got != "5" {
		t.Errorf("non-time unit = %q", got)
	}
}

// timeseriesServer serves the definitions list, one widget and
// /timeseries for the ids in series, recording each timeseries query.
type timeseriesServer struct {
	series  map[string]string
	queries []url.Values
	paths   []string
}

func (s *timeseriesServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/metrics/definitions":
			_, _ = w.Write([]byte(`{"definitions":[{"id":"` + mDef + `","name":"checkout requests","queries":[{},{}],"formulas":[]},` +
				`{"id":"` + mDef2 + `","name":"errors","queries":[{}]}]}`))
		case r.URL.Path == "/widgets/"+mWidget:
			_, _ = w.Write([]byte(`{"id":"` + mWidget + `","name":"w","metric_ids":["` + mDef + `","` + mDef2 + `"]}`))
		case strings.HasPrefix(r.URL.Path, "/timeseries/"):
			s.queries = append(s.queries, r.URL.Query())
			body, ok := s.series[strings.TrimPrefix(r.URL.Path, "/timeseries/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":404,"details":"Metric definition not found"}`))
				return
			}
			_, _ = w.Write([]byte(body))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestDefinitionQueryTable(t *testing.T) {
	s := &timeseriesServer{series: map[string]string{mDef: liveTimeseries}}
	out, stderr, err := runResource(t, s.handler(t), "", "metrics", "definitions", "query", "checkout requests", "-o", "table")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.queries) != 1 {
		t.Fatalf("timeseries calls = %d", len(s.queries))
	}
	q := s.queries[0]
	if q.Get("time_range") != "Last 24 hours" || q.Get("num_of_slices") != "24" || q.Has("limit") {
		t.Fatalf("default params = %v", q)
	}
	for _, want := range []string{"SERIES", "TREND", "requests", "204", "█▁▄", "GET /logs", "413 ms", "80.0 ms", "rate", "1.50"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(stderr, "zero") {
		t.Errorf("unexpected zero warning: %q", stderr)
	}
}

func TestDefinitionQueryJSONIsRaw(t *testing.T) {
	s := &timeseriesServer{series: map[string]string{mDef: liveTimeseries}}
	out, _, err := runResource(t, s.handler(t), "", "metrics", "definitions", "query", mDef, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	latency, _ := got["latency"].(map[string]any)
	if _, ok := latency["groups_series"]; !ok {
		t.Fatalf("-o json is not the raw response: %s", out)
	}
	// A UUID needs no name lookup: only the timeseries call.
	if len(s.paths) != 1 {
		t.Fatalf("requests = %v", s.paths)
	}
}

func TestDefinitionQueryTimeFlags(t *testing.T) {
	cases := []struct {
		args []string
		want map[string]string
	}{
		{[]string{"--since", "7d", "--slices", "48", "--limit", "5"},
			map[string]string{"time_range": "Last 7 days", "num_of_slices": "48", "limit": "5"}},
		{[]string{"--time-range", "Last 2 weeks"}, map[string]string{"time_range": "Last 2 weeks"}},
		{[]string{"--from", "2026-10-01T00:00:00Z", "--to", "2026-10-02T00:00:00Z"},
			map[string]string{"from_ts": "1790812800000", "to_ts": "1790899200000"}},
	}
	for _, c := range cases {
		s := &timeseriesServer{series: map[string]string{mDef: liveTimeseries}}
		args := append([]string{"metrics", "definitions", "query", mDef, "-o", "json"}, c.args...)
		if _, _, err := runResource(t, s.handler(t), "", args...); err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		for k, v := range c.want {
			if got := s.queries[0].Get(k); got != v {
				t.Errorf("%v: %s = %q, want %q", c.args, k, got, v)
			}
		}
	}
}

func TestDefinitionQueryRejectsBadFlags(t *testing.T) {
	for args, code := range map[string]string{
		"--time-range|Last 1 hour|--since|1h": "usage_conflicting_time_flags",
		"--limit|1001":                        "usage_invalid_limit",
		"--slices|0":                          "usage_invalid_flags",
	} {
		_, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
			t.Errorf("%s: no request expected", args)
		}, "", append([]string{"metrics", "definitions", "query", mDef}, strings.Split(args, "|")...)...)
		if got := codeOf(err); got != code {
			t.Errorf("%s: code = %q, want %q", args, got, code)
		}
	}
}

func TestDefinitionQueryWarnsWhenAllZero(t *testing.T) {
	zero := `{"q":{"name":"q","queries":[{"name":"q"}],"aggregates":[{"function":"COUNT","value":0}],` +
		`"series":[{"value":"0.0"},{"value":"0.0"}],"groups_series":[]}}`
	s := &timeseriesServer{series: map[string]string{mDef: zero}}
	_, stderr, err := runResource(t, s.handler(t), "", "metrics", "definitions", "query", mDef, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "every series is zero over Last 24 hours") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestDefinitionQueryMissingIsNotFound(t *testing.T) {
	s := &timeseriesServer{series: map[string]string{}}
	_, _, err := runResource(t, s.handler(t), "", "metrics", "definitions", "query", mDef)
	if code := codeOf(err); code != "resource_not_found" {
		t.Fatalf("code = %q (%v)", code, err)
	}
}

func TestWidgetQueryRunsEveryDefinition(t *testing.T) {
	s := &timeseriesServer{series: map[string]string{mDef: liveTimeseries, mDef2: liveTimeseries}}
	out, _, err := runResource(t, s.handler(t), "", "widgets", "query", mWidget, "-o", "table")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.queries) != 2 {
		t.Fatalf("timeseries calls = %d, want one per metric id", len(s.queries))
	}
	if !strings.Contains(out, "DEFINITION") || !strings.Contains(out, "checkout requests") || !strings.Contains(out, "errors") {
		t.Fatalf("table:\n%s", out)
	}

	s = &timeseriesServer{series: map[string]string{mDef: liveTimeseries, mDef2: liveTimeseries}}
	out, _, err = runResource(t, s.handler(t), "", "widgets", "query", mWidget, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || got[mDef] == nil || got[mDef2] == nil {
		t.Fatalf("-o json should map each definition id to its raw response: %v\n%s", err, out)
	}
}

func TestWidgetQueryErrors(t *testing.T) {
	_, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"` + mWidget + `","name":"w","metric_ids":[]}`))
	}, "", "widgets", "query", mWidget)
	if code := codeOf(err); code != "resource_not_found" || !strings.Contains(err.Error(), "no metric definitions") {
		t.Fatalf("empty metric_ids: %v", err)
	}

	// The widget references a definition that no longer exists.
	s := &timeseriesServer{series: map[string]string{mDef: liveTimeseries}}
	_, _, err = runResource(t, s.handler(t), "", "widgets", "query", mWidget)
	if code := codeOf(err); code != "resource_not_found" || !strings.Contains(err.Error(), mDef2) {
		t.Fatalf("dangling metric id: %v", err)
	}
}

func TestDefinitionGetMaps500ToNotFound(t *testing.T) {
	handler := func(listed bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/metrics/definitions/" + mDef:
				// Live (2026-10-21): a missing id answers 500.
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"code":500,"details":"Internal Server Error"}`))
			case "/metrics/definitions":
				if listed {
					_, _ = w.Write([]byte(`{"definitions":[{"id":"` + mDef + `","name":"d"}]}`))
				} else {
					_, _ = w.Write([]byte(`{"definitions":[]}`))
				}
			}
		}
	}
	_, _, err := runResource(t, handler(false), "", "metrics", "definitions", "get", mDef)
	if code := codeOf(err); code != "resource_not_found" {
		t.Fatalf("missing definition: code = %q (%v)", code, err)
	}
	// A definition that exists keeps the real server error.
	_, _, err = runResource(t, handler(true), "", "metrics", "definitions", "get", mDef)
	if code := codeOf(err); code != "api_server_error" {
		t.Fatalf("existing definition: code = %q (%v)", code, err)
	}
}

func TestDefinitionCreateChecksBody(t *testing.T) {
	for body, want := range map[string]string{
		`{"name":"d"}`: "at least one query",
		`{"name":"d","queries":[{"name":"q","select":["count(*)"],"from_expr":"x"}]}`: `query "q" is missing where`,
		`{"name":"d","queries":[{"name":"q","select":["count(*)"],"where":""}]}`:      "from_expr (or from)",
		`{"name":"d","queries":[{},{},{},{},{},{},{}]}`:                               "at most 6 queries",
	} {
		_, _, err := runResource(t, func(w http.ResponseWriter, _ *http.Request) {
			t.Errorf("no request expected for %s", body)
		}, body, "metrics", "definitions", "create", "--input", "-")
		if codeOf(err) != "usage_invalid_body" || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want usage_invalid_body containing %q", body, err, want)
		}
	}

	var gotPath, gotBody string
	ok := `{"name":"d","queries":[{"name":"q","select":["count(*)"],"where":"","from_expr":"x"}]}`
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"id":"` + mDef + `","name":"d"}`))
	}, ok, "metrics", "definitions", "create", "--input", "-", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "POST /metrics/definitions" || !strings.Contains(gotBody, `"from_expr":"x"`) {
		t.Fatalf("%s %s", gotPath, gotBody)
	}
}

func TestDefinitionsListTable(t *testing.T) {
	out, _, err := runResource(t, (&timeseriesServer{}).handler(t), "", "metrics", "definitions", "list", "-o", "table")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "QUERIES") ||
		strings.Join(strings.Fields(lines[1]), " ") != "checkout requests 2 0 "+mDef {
		t.Fatalf("out:\n%s", out)
	}
}

func TestDefinitionsHaveNoUpdateAndCreateExamples(t *testing.T) {
	root := NewRootCmd()
	if c := findSub(root, "metrics", "definitions", "update"); c != nil {
		t.Fatal("metrics definitions update exists, but the API has no update endpoint")
	}
	create := findSub(root, "metrics", "definitions", "create")
	if create == nil {
		t.Fatal("metrics definitions create missing")
	}
	for _, want := range []string{"count(*)", "p95($span.duration_nano)", "errors / total * 100", "from_expr"} {
		if !strings.Contains(create.Example, want) {
			t.Errorf("create example lacks %q", want)
		}
	}
	if def := findSub(root, "metrics", "definitions"); def == nil || !strings.Contains(def.Long, "OpenAPI") {
		t.Error("metrics definitions help should say the endpoint is not in the published spec")
	}
}

// TestMetricNamedDefinitionsStillResolves: `metrics get <name>` takes the
// metric as an argument of get, so a metric literally named "definitions"
// never collides with the `metrics definitions` subcommand.
func TestMetricNamedDefinitionsStillResolves(t *testing.T) {
	const metricID = "aaaaaaaa-aaaa-aaaa-aaaa-0000000000e9"
	var paths []string
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/metrics":
			_, _ = w.Write([]byte(`{"metrics":[{"metric_id":"` + metricID + `","metric_name":"definitions"}]}`))
		case "/metrics/" + metricID:
			_, _ = w.Write([]byte(`{"metric_id":"` + metricID + `","metric_name":"definitions"}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}, "", "metrics", "get", "definitions", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "/metrics,/metrics/"+metricID || !strings.Contains(out, metricID) {
		t.Fatalf("paths = %v, out = %s", paths, out)
	}
}
