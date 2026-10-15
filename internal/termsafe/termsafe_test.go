package termsafe

import "testing"

func TestTextEscapesControls(t *testing.T) {
	for _, tc := range []struct{ in, text, line string }{
		{"plain ascii", "plain ascii", "plain ascii"},
		{"héllo wörld ✓", "héllo wörld ✓", "héllo wörld ✓"},
		// OSC title set, screen clear, OSC 8 hyperlink.
		{"ok\x1b]0;PWNED\a\x1b[2J", `ok\x1b]0;PWNED\x07\x1b[2J`, `ok\x1b]0;PWNED\x07\x1b[2J`},
		{"a\rb", `a\rb`, `a\rb`},
		{"line1\nline2\tx", "line1\nline2\tx", `line1\nline2\tx`},
		{"del\x7f", `del\x7f`, `del\x7f`},
		{"c1\u009b31m", `c1\u009b31m`, `c1\u009b31m`},
		{"bad\xffutf8", `bad\xffutf8`, `bad\xffutf8`},
		{"", "", ""},
	} {
		if got := Text(tc.in); got != tc.text {
			t.Errorf("Text(%q) = %q, want %q", tc.in, got, tc.text)
		}
		if got := Line(tc.in); got != tc.line {
			t.Errorf("Line(%q) = %q, want %q", tc.in, got, tc.line)
		}
	}
}

func TestCleanInputIsReturnedUnchanged(t *testing.T) {
	s := "no controls here · ünïcode"
	if got := Text(s); got != s {
		t.Fatalf("got %q", got)
	}
}
