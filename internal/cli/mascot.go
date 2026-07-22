package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// The mascot commands (graze / herd / rumble) are hidden easter eggs. The
// rendered figure is the Bronto logo, a trademark used with permission —
// see TRADEMARK.md; it is NOT covered by the repo's MIT license. The frame
// data in mascot_frames.go is generated from assets/logomark.png by
// scripts/logo2frames.py.
//
// Naming is deliberately paleontology-safe: sauropods almost certainly
// didn't roar (no syrinx; likely low-frequency resonance) and a 15-tonne
// animal doesn't stampede — but trackways are real evidence of herd
// movement. Hence graze / herd / rumble, not stampede / roar.

// mascot ANSI colors: truecolor sampled from the logo, plus a 256-color
// fallback for terminals without truecolor.
const (
	mascotReset   = "\x1b[0m"
	mascotHideTC  = "\x1b[38;2;247;194;55m" // #f7c237
	mascotFacetTC = "\x1b[38;2;221;150;18m" // #dd9612
	mascotEdgeTC  = "\x1b[38;2;40;30;12m"   // #281e0c (readable on dark)
	mascotHide256 = "\x1b[38;5;220m"
	mascotFac256  = "\x1b[38;5;172m"
	mascotEdge256 = "\x1b[38;5;234m"
)

// mascotStyle bundles the per-cell strings for one rendering mode.
type mascotStyle struct {
	hide, facet, edge, reset string
	// mono maps a cell kind to a density glyph when color is off.
	mono map[byte]rune
}

// pickMascotStyle chooses colored vs monochrome. Monochrome uses ink
// density (░ light hide, ▒ facet, █ outline) so the figure reads on both
// light and dark terminals with no background detection. Colored prefers
// truecolor (sampled from the logo) and falls back to the 256-color
// approximations when COLORTERM doesn't advertise 24-bit.
func pickMascotStyle(color bool) mascotStyle {
	if !color {
		return mascotStyle{mono: map[byte]rune{'y': '░', 'o': '▒', 'k': '█'}}
	}
	if trueColor() {
		return mascotStyle{hide: mascotHideTC, facet: mascotFacetTC, edge: mascotEdgeTC, reset: mascotReset}
	}
	return mascotStyle{hide: mascotHide256, facet: mascotFac256, edge: mascotEdge256, reset: mascotReset}
}

// trueColor reports whether the terminal advertises 24-bit color.
func trueColor() bool {
	ct := os.Getenv("COLORTERM")
	return ct == "truecolor" || ct == "24bit"
}

// renderMascotLine renders one grid row into a printable string.
func renderMascotLine(row string, st mascotStyle) string {
	var sb strings.Builder
	for i := 0; i < len(row); i++ {
		c := row[i]
		if st.mono != nil {
			if g, ok := st.mono[c]; ok {
				sb.WriteRune(g)
			} else {
				sb.WriteByte(' ')
			}
			continue
		}
		switch c {
		case 'y':
			sb.WriteString(st.hide + "█" + st.reset)
		case 'o':
			sb.WriteString(st.facet + "█" + st.reset)
		case 'k':
			sb.WriteString(st.edge + "█" + st.reset)
		default:
			sb.WriteByte(' ')
		}
	}
	return sb.String()
}

// renderMascot returns the full figure as lines, each shifted right by pad
// columns (for placing multiple in a herd or animating a march).
func renderMascot(frame []string, st mascotStyle, pad int) []string {
	prefix := strings.Repeat(" ", pad)
	out := make([]string, len(frame))
	for i, row := range frame {
		out[i] = prefix + renderMascotLine(row, st)
	}
	return out
}

// commonIndent is the smallest leading-space count across non-empty rows;
// stripping it left-aligns the figure for static / bubble views without
// distorting its internal shape.
func commonIndent(frame []string) int {
	min := -1
	for _, row := range frame {
		if strings.TrimSpace(row) == "" {
			continue
		}
		n := len(row) - len(strings.TrimLeft(row, " "))
		if min == -1 || n < min {
			min = n
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

// trimmedFrame left-aligns the figure by removing the common indent.
func trimmedFrame() []string {
	ci := commonIndent(mascotFrame)
	out := make([]string, len(mascotFrame))
	for i, row := range mascotFrame {
		if len(row) >= ci {
			out[i] = row[ci:]
		} else {
			out[i] = ""
		}
	}
	return out
}

func newGrazeCmd() *cobra.Command {
	var noAnim bool
	cmd := &cobra.Command{
		Use:    "graze",
		Short:  "🦕",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			st := pickMascotStyle(app.Color)
			if noAnim || !app.StdoutIsTTY {
				printMascotStatic(app, st, 0)
				return nil
			}
			return marchMascots(cmd.Context(), app, st, 1)
		},
	}
	cmd.Flags().BoolVar(&noAnim, "no-anim", false, "print a single static frame instead of animating")
	return cmd
}

func newHerdCmd() *cobra.Command {
	var noAnim bool
	var count int
	cmd := &cobra.Command{
		Use:    "herd",
		Short:  "🦕🦕🦕",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			if count < 1 {
				count = 1
			}
			if count > 5 {
				count = 5
			}
			st := pickMascotStyle(app.Color)
			if noAnim || !app.StdoutIsTTY {
				printMascotStatic(app, st, 0)
				return nil
			}
			return marchMascots(cmd.Context(), app, st, count)
		},
	}
	cmd.Flags().BoolVar(&noAnim, "no-anim", false, "print a single static frame instead of animating")
	cmd.Flags().IntVar(&count, "count", 3, "number of brontos in the herd (1-5)")
	return cmd
}

func newRumbleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "rumble [message]",
		Short:  "💬🦕",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			msg := strings.TrimSpace(strings.Join(args, " "))
			if msg == "" {
				msg = randomTip(cmd)
			}
			st := pickMascotStyle(app.Color)
			for _, line := range speechBubble(msg) {
				_, _ = fmt.Fprintln(app.Stdout, line)
			}
			// a small tether from the bubble down to the figure
			_, _ = fmt.Fprintln(app.Stdout, "        \\")
			_, _ = fmt.Fprintln(app.Stdout, "         \\")
			for _, line := range renderMascot(trimmedFrame(), st, 6) {
				_, _ = fmt.Fprintln(app.Stdout, line)
			}
			return nil
		},
	}
	return cmd
}

// printMascotStatic draws the figure once (non-TTY / --no-anim path).
func printMascotStatic(app *App, st mascotStyle, pad int) {
	for _, line := range renderMascot(trimmedFrame(), st, pad) {
		_, _ = fmt.Fprintln(app.Stdout, line)
	}
}

// mascotFrameHeight is the row count of the figure.
var mascotFrameHeight = len(mascotFrame)

// mascotFrameWidth is the widest grid row (before color escapes).
func mascotFrameWidth() int {
	w := 0
	for _, r := range mascotFrame {
		if len(r) > w {
			w = len(r)
		}
	}
	return w
}

// march animation tuning; vars so tests can shrink them.
var (
	marchTick  = 90 * time.Millisecond
	marchStep  = 3   // columns per tick
	marchWidth = 100 // assumed terminal width when not detectable
)

// marchMascots walks `count` brontos right-to-left across the terminal,
// redrawing in place, until they exit or the context is cancelled. The
// cursor is hidden during the walk and always restored.
func marchMascots(ctx context.Context, app *App, st mascotStyle, count int) error {
	width := marchWidth
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		width = w
	}
	fw := mascotFrameWidth()
	h := mascotFrameHeight

	_, _ = fmt.Fprint(app.Stdout, "\x1b[?25l")                                // hide cursor
	defer func() { _, _ = fmt.Fprint(app.Stdout, "\x1b[?25h"+mascotReset) }() // restore on any exit

	// each herd member trails the one ahead by ~1.5 body widths
	gap := fw*3/2 + 4
	drawn := false
	for x := width; x > -(fw + gap*count); x -= marchStep {
		if ctx.Err() != nil {
			break
		}
		if drawn {
			_, _ = fmt.Fprintf(app.Stdout, "\x1b[%dA", h) // cursor up h rows
		}
		frame := compositeHerd(st, count, x, gap, width)
		for _, line := range frame {
			_, _ = fmt.Fprintf(app.Stdout, "\x1b[2K%s\n", line) // clear line + draw
		}
		drawn = true
		select {
		case <-ctx.Done():
		case <-time.After(marchTick):
		}
	}
	return nil
}

// compositeHerd overlays `count` figures at descending x positions into a
// single frame of blank-padded lines.
func compositeHerd(st mascotStyle, count, headX, gap, width int) []string {
	h := mascotFrameHeight
	// build each row as a rune canvas so figures can overlap cleanly
	canvasW := width + mascotFrameWidth() + 4
	rows := make([][]rune, h)
	for i := range rows {
		rows[i] = []rune(strings.Repeat(" ", canvasW))
	}
	for n := 0; n < count; n++ {
		x := headX + n*gap
		blitFigure(rows, x)
	}
	out := make([]string, h)
	for i, r := range rows {
		out[i] = renderCanvasLine(string(trimRightRunes(r)), st)
	}
	return out
}

// blitFigure stamps the classified grid onto the rune canvas at column x
// (cells off the canvas are clipped; spaces are transparent).
func blitFigure(rows [][]rune, x int) {
	for i, row := range mascotFrame {
		if i >= len(rows) {
			break
		}
		for j := 0; j < len(row); j++ {
			c := row[j]
			if c == ' ' {
				continue
			}
			col := x + j
			if col < 0 || col >= len(rows[i]) {
				continue
			}
			rows[i][col] = rune(c)
		}
	}
}

// renderCanvasLine colors a composited row (its runes are already cell
// kinds 'y'/'o'/'k' or spaces).
func renderCanvasLine(row string, st mascotStyle) string {
	var sb strings.Builder
	for _, c := range row {
		if st.mono != nil {
			switch c {
			case 'y':
				sb.WriteRune('░')
			case 'o':
				sb.WriteRune('▒')
			case 'k':
				sb.WriteRune('█')
			default:
				sb.WriteRune(' ')
			}
			continue
		}
		switch c {
		case 'y':
			sb.WriteString(st.hide + "█" + st.reset)
		case 'o':
			sb.WriteString(st.facet + "█" + st.reset)
		case 'k':
			sb.WriteString(st.edge + "█" + st.reset)
		default:
			sb.WriteRune(' ')
		}
	}
	return sb.String()
}

func trimRightRunes(r []rune) []rune {
	n := len(r)
	for n > 0 && r[n-1] == ' ' {
		n--
	}
	return r[:n]
}

// speechBubble wraps msg into a cowsay-style rounded box.
func speechBubble(msg string) []string {
	const width = 44
	words := strings.Fields(msg)
	var lines []string
	cur := ""
	for _, w := range words {
		switch {
		case cur == "":
			cur = w
		case len(cur)+1+len(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	inner := 0
	for _, l := range lines {
		if len(l) > inner {
			inner = len(l)
		}
	}
	out := make([]string, 0, len(lines)+2)
	out = append(out, " "+strings.Repeat("_", inner+2))
	for i, l := range lines {
		left, right := "<", ">"
		if len(lines) > 1 {
			switch i {
			case 0:
				left, right = "/", "\\"
			case len(lines) - 1:
				left, right = "\\", "/"
			default:
				left, right = "|", "|"
			}
		}
		out = append(out, fmt.Sprintf("%s %-*s %s", left, inner, l, right))
	}
	out = append(out, " "+strings.Repeat("-", inner+2))
	return out
}

// mascotTips are the real one-liners rumble teaches when given no message.
var mascotTips = []string{
	"did you know: 'bronto fields -d <dataset>' lists the columns you can query",
	"tip: --fields '?' prints available field names instead of data",
	"tip: pipe any command and it emits JSONL — no flag needed",
	"tip: 'bronto search -x' expands one event across full, untruncated lines",
	"tip: 'bronto repl' is a psql-style prompt for iterative queries",
	"tip: everything that takes an id also takes a unique name",
	"tip: --dry-run prints a mutating call as a plan instead of running it",
}

// randomTip picks a tip without Math.rand (unavailable determinism aside,
// keep it simple): derive an index from the process's monotonic clock.
func randomTip(cmd *cobra.Command) string {
	idx := int(time.Now().UnixNano()) % len(mascotTips)
	if idx < 0 {
		idx += len(mascotTips)
	}
	_ = cmd
	return mascotTips[idx]
}
