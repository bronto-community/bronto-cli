package output

import (
	"bytes"
	"strings"
	"testing"
)

// The .[] hint prints once per printer, on the notice writer, for both
// the whole-list (PrintRows) and the streaming (PrintRow) paths.
func TestJQListHintOncePerPrinter(t *testing.T) {
	code, err := CompileJQ(".[] | .a")
	if err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{{"a": 1}, {"a": 2}}
	for _, f := range []Format{FormatJSON, FormatJSONL} {
		var out, notice bytes.Buffer
		p := NewPrinter(&out, f)
		p.SetNoticeWriter(&notice)
		p.SetJQ(code)
		p.SetJQListHint(true)
		if err := p.PrintRows([]string{"a"}, rows); err != nil {
			t.Fatal(err)
		}
		if err := p.PrintRow([]string{"a"}, rows[0]); f == FormatJSONL && err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(notice.String(), JQListHint); got != 1 {
			t.Errorf("%s: hint printed %d times: %q", f, got, notice.String())
		}
	}
	// Off by default, and PrintJSON never hints.
	var out, notice bytes.Buffer
	p := NewPrinter(&out, FormatJSON)
	p.SetNoticeWriter(&notice)
	p.SetJQ(code)
	if err := p.PrintRows([]string{"a"}, rows); err != nil {
		t.Fatal(err)
	}
	p.SetJQListHint(true)
	if err := p.PrintJSON([]any{map[string]any{"a": 1}}); err != nil {
		t.Fatal(err)
	}
	if notice.Len() != 0 {
		t.Errorf("unexpected notice %q", notice.String())
	}
}
