package traces

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/svrnm/bronto-cli/internal/bronto"
	"github.com/svrnm/bronto-cli/internal/timerange"
)

// aggServer answers /search per the aggregate in select[0].
func aggServer(t *testing.T, responses map[string]string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		bodies = append(bodies, body)
		sel, _ := body["select"].([]any)
		key := ""
		if len(sel) > 0 {
			key = sel[0].(string)
		}
		resp, ok := responses[key]
		if !ok {
			resp = `{"groups":[]}`
		}
		_, _ = w.Write([]byte(resp))
	}))
	return srv, &bodies
}

func newAgg(srv *httptest.Server) *Aggregator {
	return &Aggregator{
		Client: bronto.NewClient(srv.Client(), srv.URL),
		Time:   timerange.Spec{TimeRange: "Last 15 minutes"},
	}
}

func TestServicesMergesThreeAggregates(t *testing.T) {
	srv, bodies := aggServer(t, map[string]string{
		"count(*)":                 `{"groups":[{"group":["cart"],"count(*)":30},{"group":["web"],"count(*)":10}]}`,
		"avg($span.duration_nano)": `{"groups":[{"group":["cart"],"avg($span.duration_nano)":2000000}]}`,
		"max($span.duration_nano)": `{"groups":[{"group":["web"],"max($span.duration_nano)":9000000}]}`,
	})
	defer srv.Close()
	rows, err := newAgg(srv).Services(context.Background(), false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["service"] != "cart" || rows[0]["spans"] != int64(30) {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0]["avg"] != "2.00ms" || rows[1]["max"] != "9.00ms" {
		t.Fatalf("formatted: %v", rows)
	}
	if rows[1]["avg"] != "—" { // missing entry defaults to 0 -> em dash
		t.Fatalf("missing avg = %v", rows[1]["avg"])
	}
	// every request targeted the traces logset
	for _, b := range *bodies {
		if b["from_expr"] != FromExpr {
			t.Fatalf("from_expr = %v", b["from_expr"])
		}
	}
}

func TestOperationsGroupsByServiceAndName(t *testing.T) {
	srv, bodies := aggServer(t, map[string]string{
		"count(*)": `{"groups":[{"group":["cart","HGET"],"count(*)":5}]}`,
	})
	defer srv.Close()
	rows, err := newAgg(srv).Operations(context.Background(), "cart", true, 25)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0]["operation"] != "HGET" || rows[0]["spans"] != int64(5) {
		t.Fatalf("rows = %v", rows)
	}
	// where must include service filter AND errors filter
	b := (*bodies)[0]
	where, _ := b["where"].(string)
	if where != "$service.name = 'cart' AND $span.status_code = 'STATUS_CODE_ERROR'" {
		t.Fatalf("where = %q", where)
	}
}

func TestAttributesMissingHandlingAndTrim(t *testing.T) {
	srv, bodies := aggServer(t, map[string]string{
		"count(*)": `{"groups":[
			{"group":["/api/a"],"count(*)":50},
			{"group":[""],"count(*)":40},
			{"group":["/api/b"],"count(*)":30}]}`,
		"avg($span.duration_nano)": `{"groups":[{"group":["/api/a"],"avg($span.duration_nano)":1500000}]}`,
		"max($span.duration_nano)": `{"groups":[]}`,
	})
	defer srv.Close()

	rows, cols, dropped, err := newAgg(srv).Attributes(context.Background(), AttrOptions{
		By: []string{"http.route"}, RootOnly: true, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1 (empty group)", dropped)
	}
	if len(rows) != 2 || rows[0]["http.route"] != "/api/a" || rows[1]["http.route"] != "/api/b" {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0]["err%"] == nil {
		t.Fatalf("err%% column missing: %v", rows[0])
	}
	wantCols := []string{"http.route", "spans", "errors", "err%", "avg", "max"}
	if len(cols) != len(wantCols) {
		t.Fatalf("cols = %v", cols)
	}
	// 4 queries ran (count, avg, max, errors-count) with overfetch limit 200
	if len(*bodies) != 4 {
		t.Fatalf("queries = %d, want 4", len(*bodies))
	}
	if (*bodies)[0]["limit"] != float64(200) {
		t.Fatalf("overfetch limit = %v, want 200", (*bodies)[0]["limit"])
	}
	// root-only clause present
	where, _ := (*bodies)[0]["where"].(string)
	if where != RootOnlyClause {
		t.Fatalf("where = %q", where)
	}
}

func TestAttributesIncludeEmptyLabelsMissing(t *testing.T) {
	srv, _ := aggServer(t, map[string]string{
		"count(*)": `{"groups":[{"group":["null"],"count(*)":7}]}`,
	})
	defer srv.Close()
	rows, _, dropped, err := newAgg(srv).Attributes(context.Background(), AttrOptions{
		By: []string{"http.route"}, IncludeEmpty: true, ErrorsOnly: true, Limit: 10,
	})
	if err != nil || dropped != 0 {
		t.Fatalf("err=%v dropped=%d", err, dropped)
	}
	if rows[0]["http.route"] != "<missing>" {
		t.Fatalf("label = %v", rows[0]["http.route"])
	}
	if _, has := rows[0]["errors"]; has { // errorsOnly drops errors columns
		t.Fatalf("errors column must be absent: %v", rows[0])
	}
}

func TestAttributesDeterministicTieOrder(t *testing.T) {
	srv, _ := aggServer(t, map[string]string{
		"count(*)": `{"groups":[
			{"group":["/z"],"count(*)":10},{"group":["/a"],"count(*)":10},
			{"group":["/m"],"count(*)":10}]}`,
	})
	defer srv.Close()
	var first []string
	for i := 0; i < 10; i++ {
		rows, _, _, err := newAgg(srv).Attributes(context.Background(), AttrOptions{
			By: []string{"http.route"}, Limit: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, r := range rows {
			order = append(order, r["http.route"].(string))
		}
		if first == nil {
			first = order
			continue
		}
		for j := range order {
			if order[j] != first[j] {
				t.Fatalf("iteration %d: order %v != %v", i, order, first)
			}
		}
	}
	if first[0] != "/a" || first[1] != "/m" || first[2] != "/z" {
		t.Fatalf("tie order not alphabetical: %v", first)
	}
}

func TestParseGroupForms(t *testing.T) {
	if got := parseGroup([]any{"a", float64(2)}); got[0] != "a" || got[1] != "2" {
		t.Fatalf("list form: %v", got)
	}
	if got := parseGroup("[a, b]"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("bracket form: %v", got)
	}
	if got := parseGroup("single"); len(got) != 1 || got[0] != "single" {
		t.Fatalf("scalar form: %v", got)
	}
	got := parseGroup(map[string]any{"b": "2", "a": "1"}) // sorted keys -> deterministic
	if len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("map form: %v", got)
	}
}
