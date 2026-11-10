package traces

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListSpansBuildsWhereAndRows(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"result":[{"@time":"t1","Span:trace_id":"tr","Span:span_id":"sp",
			"Span:name":"GET /x","Resource:service.name":"web","Span:duration_nano":2000000,
			"Span:status_code":"STATUS_CODE_OK"}]}`))
	}))
	defer srv.Close()

	rows, err := newAgg(srv).ListSpans(context.Background(), ListOptions{
		Service: "web", Operation: "GET /x", MinDurationMS: 1.5, ErrorsOnly: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	where, _ := body["where"].(string)
	want := "Resource:service.name = 'web' AND Span:name = 'GET /x' AND Span:duration_nano >= 1500000 AND Span:status_code = 'STATUS_CODE_ERROR'"
	if where != want {
		t.Fatalf("where = %q\nwant    %q", where, want)
	}
	if body["most_recent_first"] != true {
		t.Fatal("most_recent_first must be true")
	}
	if rows[0]["duration"] != "2.00ms" || rows[0]["status"] != "OK" || rows[0]["trace_id"] != "tr" {
		t.Fatalf("row = %v", rows[0])
	}
}

func TestFetchTraceSpansBatchesOrChains(t *testing.T) {
	var wheres []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		wh, _ := body["where"].(string)
		wheres = append(wheres, wh)
		if body["limit"] != float64(5000) {
			t.Errorf("limit = %v, want 5000", body["limit"])
		}
		_, _ = w.Write([]byte(`{"result":[{"Span:trace_id":"x","Span:span_id":"s1"}]}`))
	}))
	defer srv.Close()

	ids := make([]string, 17) // forces 2 batches (15 + 2)
	for i := range ids {
		ids[i] = fmt.Sprintf("id%02d", i)
	}
	spans, err := newAgg(srv).FetchTraceSpans(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(wheres) != 2 {
		t.Fatalf("batches = %d, want 2", len(wheres))
	}
	if strings.Count(wheres[0], " OR ") != 14 || strings.Count(wheres[1], " OR ") != 1 {
		t.Fatalf("OR chains wrong: %q / %q", wheres[0], wheres[1])
	}
	if !strings.Contains(wheres[0], "Span:trace_id = 'id00'") {
		t.Fatalf("clause form: %q", wheres[0])
	}
	if len(spans) != 2 {
		t.Fatalf("spans = %d", len(spans))
	}
}

func TestFindSampleTraceIDsDedupsAndStops(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"result":[
			{"Span:trace_id":"a"},{"Span:trace_id":"b"},{"Span:trace_id":"a"},
			{"Span:trace_id":"c"},{"Span:trace_id":"d"}]}`))
	}))
	defer srv.Close()

	ids, err := newAgg(srv).FindSampleTraceIDs(context.Background(), "x = 1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != "a" || ids[1] != "b" || ids[2] != "c" {
		t.Fatalf("ids = %v", ids)
	}
	if body["limit"] != float64(30) { // max(3*3, 30)
		t.Fatalf("limit = %v", body["limit"])
	}
	if body["most_recent_first"] != true {
		t.Fatal("sampling is most-recent-first")
	}
}
