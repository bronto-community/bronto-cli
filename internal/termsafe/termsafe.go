// Package termsafe escapes control characters in API-sourced text before
// it is printed to a terminal.
package termsafe

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Text makes API-sourced text safe to print in a terminal. Log
// lines, monitor names and error bodies are data other people control, and
// a raw ESC, C1 control or carriage return in them can set the window
// title, clear the screen, plant a disguised OSC 8 hyperlink, overwrite the
// visible line, or on some terminals make the terminal type a reply back.
// Every C0 control except newline and tab, DEL, every C1 control and every
// invalid UTF-8 byte is replaced with a visible Go-style escape (\x1b,
// \u009b, \xff), so the content stays readable and nothing is lost.
//
// Use it on human output only: json, jsonl, csv and raw carry values
// byte-exact (encoding/json already escapes controls). Apply it to the
// data before adding the CLI's own color codes, which must survive.
func Text(s string) string { return sanitize(s, false) }

// Line is Text for single-line contexts such as table
// cells, where a newline or tab would also break the layout: those become
// \n and \t too.
func Line(s string) string { return sanitize(s, true) }

func sanitize(s string, oneLine bool) string {
	if !needsSanitize(s, oneLine) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n' && oneLine:
			b.WriteString(`\n`)
		case r == '\t' && oneLine:
			b.WriteString(`\t`)
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r >= 0x80 && r <= 0x9f:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// needsSanitize is the allocation-free fast path: almost every value is
// clean.
func needsSanitize(s string, oneLine bool) bool {
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\n' || c == '\t':
			if oneLine {
				return true
			}
		case c < 0x20 || c == 0x7f:
			return true
		case c >= 0x80:
			// C1 controls are U+0080–U+009F; invalid UTF-8 needs
			// escaping too.
			r, size := utf8.DecodeRuneInString(s[i:])
			if (r == utf8.RuneError && size == 1) || r <= 0x9f {
				return true
			}
			i += size
			continue
		}
		i++
	}
	return false
}
