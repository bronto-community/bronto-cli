// Command tapegolden turns a VHS text recording ("Output x.txt": one
// screen snapshot per tape command, separated by ──── rules) into a stable
// golden file for docs/tapes/golden/.
//
// Raw VHS text output is not deterministic: a snapshot taken right after
// Type or Enter may or may not include the last typed character or the
// first output line, depending on timing. What IS deterministic is each
// screen's settled state just before it is cleared (or the tape ends),
// because the tapes Sleep before every Ctrl+L. So tapegolden keeps only
// the "settled" frames:
//
//  1. normalize each frame (trailing whitespace and blank lines trimmed)
//  2. drop empty frames and consecutive duplicates
//  3. drop every frame whose text is a prefix of the next frame's text —
//     it was an intermediate state (typing, streaming output) on the way
//     to that next frame
//
// What remains is one frame per scene. This holds as long as a scene's
// output fits on the screen (no scrolling), which the tapes are written
// to respect.
//
//	go run ./internal/tools/tapegolden < raw.txt > golden.txt
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

const rule = "────────────────────────────────────────────────────────────────────────────────"

func main() {
	b, err := io.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		fmt.Fprintln(os.Stderr, "tapegolden:", err)
		os.Exit(1)
	}
	frames := Settled(string(b))
	w := bufio.NewWriter(os.Stdout)
	for _, f := range frames {
		_, _ = fmt.Fprintln(w, f)
		_, _ = fmt.Fprintln(w, rule)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "tapegolden:", err)
		os.Exit(1)
	}
}

// isRule reports whether line is a VHS frame separator (a run of ─).
func isRule(line string) bool {
	t := strings.TrimSpace(line)
	return t != "" && strings.Trim(t, "─") == ""
}

// Settled returns the settled frames of a raw VHS text recording.
func Settled(raw string) []string {
	var frames []string
	var cur []string
	flush := func() {
		frames = append(frames, normalize(cur))
		cur = nil
	}
	for _, line := range strings.Split(raw, "\n") {
		if isRule(line) {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()

	var dedup []string
	for _, f := range frames {
		if f == "" || (len(dedup) > 0 && dedup[len(dedup)-1] == f) {
			continue
		}
		dedup = append(dedup, f)
	}
	var out []string
	for i, f := range dedup {
		if i+1 < len(dedup) && strings.HasPrefix(dedup[i+1], f) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func normalize(lines []string) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, strings.TrimRight(l, " \t\r"))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	return strings.Join(out, "\n")
}
