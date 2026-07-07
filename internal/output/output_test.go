package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

var rows = []map[string]any{
	{"name": "web", "count": 3},
	{"name": "db", "count": 1},
}

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		flag      string
		tty       bool
		streaming bool
		want      Format
	}{
		{"", true, false, FormatTable},
		{"", false, false, FormatJSON},
		{"", false, true, FormatJSONL},
		{"csv", false, false, FormatCSV},
		{"table", false, true, FormatTable},
	}
	for _, c := range cases {
		got, err := DetectFormat(c.flag, c.tty, c.streaming)
		if err != nil || got != c.want {
			t.Errorf("DetectFormat(%q,%v,%v) = %v,%v want %v", c.flag, c.tty, c.streaming, got, err, c.want)
		}
	}
	if _, err := DetectFormat("yamlish", true, false); err == nil {
		t.Error("unknown format must error")
	}
}

func TestJSONOutputIsArray(t *testing.T) {
	var buf bytes.Buffer
	if err := NewPrinter(&buf, FormatJSON).PrintRows([]string{"name", "count"}, rows); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not a JSON array: %v", err)
	}
	if len(got) != 2 || got[0]["name"] != "web" {
		t.Fatalf("got %+v", got)
	}
}

func TestJSONLOneObjectPerLine(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatJSONL)
	for _, r := range rows {
		if err := p.PrintRow([]string{"name", "count"}, r); err != nil {
			t.Fatal(err)
		}
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), buf.String())
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %q not JSON: %v", l, err)
		}
	}
}

func TestTableColumnsOrdered(t *testing.T) {
	var buf bytes.Buffer
	if err := NewPrinter(&buf, FormatTable).PrintRows([]string{"name", "count"}, rows); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "web") {
		t.Fatalf("table output missing headers/values: %q", out)
	}
	if strings.Index(out, "NAME") > strings.Index(out, "COUNT") {
		t.Fatal("column order not preserved")
	}
}

func TestCSV(t *testing.T) {
	var buf bytes.Buffer
	if err := NewPrinter(&buf, FormatCSV).PrintRows([]string{"name", "count"}, rows); err != nil {
		t.Fatal(err)
	}
	want := "name,count\nweb,3\ndb,1\n"
	if buf.String() != want {
		t.Fatalf("got %q want %q", buf.String(), want)
	}
}

func TestRawPrintsRawField(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatRaw)
	if err := p.PrintRow(nil, map[string]any{"@raw": "hello world", "x": 1}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello world\n" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestMissingColumnValuesRenderEmpty(t *testing.T) {
	rows := []map[string]any{{"name": "web"}} // no "count" key
	var tbl, csvBuf bytes.Buffer
	if err := NewPrinter(&tbl, FormatTable).PrintRows([]string{"name", "count"}, rows); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tbl.String(), "<nil>") {
		t.Fatalf("table renders <nil>: %q", tbl.String())
	}
	if err := NewPrinter(&csvBuf, FormatCSV).PrintRows([]string{"name", "count"}, rows); err != nil {
		t.Fatal(err)
	}
	if csvBuf.String() != "name,count\nweb,\n" {
		t.Fatalf("csv = %q", csvBuf.String())
	}
}

func TestJSONEmptyRowsIsEmptyArray(t *testing.T) {
	var buf bytes.Buffer
	if err := NewPrinter(&buf, FormatJSON).PrintRows([]string{"a"}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "[]" {
		t.Fatalf("got %q, want []", buf.String())
	}
}

func TestJQOnPrintRowJSONL(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatJSONL)
	code, err := CompileJQ(".name")
	if err != nil {
		t.Fatal(err)
	}
	p.SetJQ(code)
	if err := p.PrintRow(nil, map[string]any{"name": "web", "x": 1}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "\"web\"\n" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestJQOnPrintRowsJSONMultipleResultsPerRow(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatJSON)
	code, err := CompileJQ(".name")
	if err != nil {
		t.Fatal(err)
	}
	p.SetJQ(code)
	if err := p.PrintRows([]string{"name", "count"}, rows); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || lines[0] != `"web"` || lines[1] != `"db"` {
		t.Fatalf("got %q", buf.String())
	}
}

func TestJQSkipsErroringValuesAndExitsClean(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatJSONL)
	code, err := CompileJQ(".name.x") // .name is a string; indexing it with .x is a jq runtime error
	if err != nil {
		t.Fatal(err)
	}
	p.SetJQ(code)
	for _, r := range rows {
		if err := p.PrintRow(nil, r); err != nil {
			t.Fatalf("jq runtime error must not surface: %v", err)
		}
	}
}

// TestJQSkipsOneErroringRowAmongThree pins: when the expression errors on
// only one of several values, the others still print — a single bad row
// does not abort the whole command.
func TestJQSkipsOneErroringRowAmongThree(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatJSONL)
	code, err := CompileJQ(".x | ascii_downcase") // errors when x isn't a string
	if err != nil {
		t.Fatal(err)
	}
	p.SetJQ(code)
	input := []map[string]any{
		{"x": "AAA"},
		{"x": 42}, // errors: ascii_downcase requires a string
		{"x": "CCC"},
	}
	for _, r := range input {
		if err := p.PrintRow(nil, r); err != nil {
			t.Fatalf("jq runtime error must not surface: %v", err)
		}
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || lines[0] != `"aaa"` || lines[1] != `"ccc"` {
		t.Fatalf("got %q, want the two non-erroring rows' results only", buf.String())
	}
}

func TestPrintJSONAppliesFieldFilter(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatJSON)
	p.SetFieldFilter([]string{"name"})
	if err := p.PrintJSON(map[string]any{"name": "web", "count": 3}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, buf.String())
	}
	if len(got) != 1 || got["name"] != "web" {
		t.Fatalf("got %+v, want only {name: web}", got)
	}
}

func TestCompileJQInvalidExpressionIsUsageError(t *testing.T) {
	if _, err := CompileJQ("this is not jq {{{"); err == nil {
		t.Fatal("want error for invalid jq expression")
	}
}

func TestFieldFilterOnJSONArray(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatJSON)
	p.SetFieldFilter([]string{"name"})
	if err := p.PrintRows([]string{"name", "count"}, rows); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	for _, r := range got {
		if _, ok := r["count"]; ok {
			t.Fatalf("count should be filtered out: %+v", r)
		}
		if _, ok := r["name"]; !ok {
			t.Fatalf("name should be present: %+v", r)
		}
	}
}

func TestFieldFilterOverridesTableColumns(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatTable)
	p.SetFieldFilter([]string{"name"})
	if err := p.PrintRows([]string{"name", "count"}, rows); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "COUNT") {
		t.Fatalf("count column should be dropped: %q", out)
	}
	if !strings.Contains(out, "NAME") {
		t.Fatalf("name column missing: %q", out)
	}
}

func TestListFieldsPrintsSortedUnionForPrintRows(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatJSON)
	p.SetListFields(true)
	rows := []map[string]any{
		{"b": 1, "a": 2},
		{"c": 3, "a": 2},
	}
	if err := p.PrintRows(nil, rows); err != nil {
		t.Fatal(err)
	}
	want := "a\nb\nc\n"
	if buf.String() != want {
		t.Fatalf("got %q want %q", buf.String(), want)
	}
}

func TestListFieldsPrintsNewKeysForStreamingPrintRow(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatJSONL)
	p.SetListFields(true)
	if err := p.PrintRow(nil, map[string]any{"b": 1, "a": 2}); err != nil {
		t.Fatal(err)
	}
	if err := p.PrintRow(nil, map[string]any{"a": 2, "c": 3}); err != nil {
		t.Fatal(err)
	}
	want := "a\nb\nc\n"
	if buf.String() != want {
		t.Fatalf("got %q want %q", buf.String(), want)
	}
}

func TestPrintRowRejectsNonStreamingFormats(t *testing.T) {
	for _, f := range []Format{FormatTable, FormatJSON, FormatCSV} {
		if err := NewPrinter(&bytes.Buffer{}, f).PrintRow(nil, map[string]any{"x": 1}); err == nil {
			t.Errorf("PrintRow(%s) must error", f)
		}
	}
	for _, f := range []Format{FormatJSONL, FormatRaw} {
		if err := NewPrinter(&bytes.Buffer{}, f).PrintRow(nil, map[string]any{"@raw": "x"}); err != nil {
			t.Errorf("PrintRow(%s) errored: %v", f, err)
		}
	}
}

func TestPrintJSONFieldFilterOnUnmarshaledArray(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(`[{"name":"a","x":1},{"name":"b","x":2}]`), &doc); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatJSON)
	p.SetFieldFilter([]string{"name"})
	if err := p.PrintJSON(doc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `"x"`) {
		t.Fatalf("filter not applied to []any: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"name"`) {
		t.Fatalf("names missing: %s", buf.String())
	}
}
