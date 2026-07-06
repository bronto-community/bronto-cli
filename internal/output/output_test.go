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
