package output

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func numbersRow() map[string]any {
	return map[string]any{
		"threshold": json.Number("1000000.0"),
		"ratio":     json.Number("0.50"),
		"big":       json.Number("12345678901234567890.00"),
		"exp":       json.Number("1.0e3"),
		"neg":       json.Number("-0.0"),
		"nested":    map[string]any{"v": json.Number("2.000")},
		"list":      []any{json.Number("3.0")},
	}
}

// TestIntegralNumbersPrintConsistently: the API writes some integral
// values as doubles (a monitor threshold of 1000000.0). --jq printed them
// as 1000000 while table and json kept the .0; every format must agree.
func TestIntegralNumbersPrintConsistently(t *testing.T) {
	for col, want := range map[string]string{
		"threshold": "1000000", "ratio": "0.50", "big": "12345678901234567890",
		"exp": "1.0e3", "neg": "0", "nested": `{"v":2}`, "list": "[3]",
	} {
		for _, f := range []Format{FormatCSV, FormatTable} {
			var b bytes.Buffer
			if err := NewPrinter(&b, f).PrintRows([]string{col}, []map[string]any{numbersRow()}); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(b.String()), "\n")
			got := strings.TrimSpace(lines[len(lines)-1])
			if f == FormatCSV {
				got = strings.ReplaceAll(strings.Trim(got, `"`), `""`, `"`)
			}
			if got != want {
				t.Errorf("%s %s = %s, want %s", f, col, got, want)
			}
		}
	}

	var jq bytes.Buffer
	p := NewPrinter(&jq, FormatJSON)
	code, err := CompileJQ(".threshold")
	if err != nil {
		t.Fatal(err)
	}
	p.SetJQ(code)
	if err := p.PrintJSON(numbersRow()); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(jq.String()); got != "1000000" {
		t.Fatalf("--jq .threshold = %s", got)
	}

	row := numbersRow()
	for _, f := range []Format{FormatJSON, FormatJSONL} {
		var b bytes.Buffer
		if err := NewPrinter(&b, f).PrintRows(nil, []map[string]any{row}); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"threshold": ?1000000[,\n}]`, `"ratio": ?0\.50`, `"v": ?2[\s}]`,
			`"neg": ?0[,\n}]`, `"exp": ?1\.0e3`, `\[\s*3\s*\]`} {
			if !regexp.MustCompile(want).MatchString(b.String()) {
				t.Errorf("%s output lacks %s:\n%s", f, want, b.String())
			}
		}
	}
	var b bytes.Buffer
	if err := NewPrinter(&b, FormatJSON).PrintJSON(row); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "\"threshold\": 1000000\n") {
		t.Errorf("PrintJSON = %s", b.String())
	}
	if row["threshold"] != json.Number("1000000.0") || row["nested"].(map[string]any)["v"] != json.Number("2.000") {
		t.Fatal("printing must not rewrite the caller's rows")
	}
}
