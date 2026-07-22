package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestMascotFrameShape(t *testing.T) {
	if len(mascotFrame) == 0 {
		t.Fatal("frame is empty — did logo2frames.py run?")
	}
	// the generated frame must only contain the four cell kinds
	for i, row := range mascotFrame {
		for j := 0; j < len(row); j++ {
			switch row[j] {
			case ' ', 'y', 'o', 'k':
			default:
				t.Fatalf("row %d col %d has unexpected cell %q", i, j, row[j])
			}
		}
	}
	// it should actually contain each kind (a real figure, not blank)
	all := strings.Join(mascotFrame, "")
	for _, c := range []string{"y", "o", "k"} {
		if !strings.Contains(all, c) {
			t.Fatalf("frame missing cell kind %q", c)
		}
	}
}

func TestRenderMascotLineColorAndMono(t *testing.T) {
	row := " yok "
	mono := renderMascotLine(row, pickMascotStyle(false))
	if mono != " ░▒█ " {
		t.Fatalf("mono render = %q", mono)
	}
	// spaces stay spaces
	if !strings.HasPrefix(mono, " ") || !strings.HasSuffix(mono, " ") {
		t.Fatalf("spaces not preserved: %q", mono)
	}

	t.Setenv("COLORTERM", "truecolor")
	col := renderMascotLine(row, pickMascotStyle(true))
	if strings.Count(col, "█") != 3 || !strings.Contains(col, "\x1b[38;2;247;194;55m") {
		t.Fatalf("truecolor render = %q", col)
	}

	t.Setenv("COLORTERM", "")
	c256 := renderMascotLine(row, pickMascotStyle(true))
	if strings.Count(c256, "█") != 3 || !strings.Contains(c256, "\x1b[38;5;220m") {
		t.Fatalf("256-color render = %q", c256)
	}
}

func TestTrimmedFrameLeftAligns(t *testing.T) {
	tf := trimmedFrame()
	// at least one row must now start at column 0 with a cell
	found := false
	for _, row := range tf {
		if row != "" && row[0] != ' ' {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("trimmed frame is not left-aligned")
	}
}

func TestSpeechBubbleWraps(t *testing.T) {
	long := strings.Repeat("word ", 30)
	out := speechBubble(strings.TrimSpace(long))
	if len(out) < 4 {
		t.Fatalf("bubble too short: %v", out)
	}
	if !strings.HasPrefix(out[0], " _") || !strings.HasPrefix(out[len(out)-1], " -") {
		t.Fatalf("bubble borders wrong: %q / %q", out[0], out[len(out)-1])
	}
	// multi-line uses / \ | corners, not < >
	if !strings.HasPrefix(out[1], "/") {
		t.Fatalf("multiline bubble should open with '/': %q", out[1])
	}
	// single line uses < >
	one := speechBubble("hi")
	if !strings.HasPrefix(one[1], "< ") || !strings.HasSuffix(one[1], " >") {
		t.Fatalf("single-line bubble: %q", one[1])
	}
}

func countInk(rows []string) int {
	n := 0
	for _, r := range rows {
		n += strings.Count(r, "█") + strings.Count(r, "░") + strings.Count(r, "▒")
	}
	return n
}

func TestCompositePlacesFigures(t *testing.T) {
	st := pickMascotStyle(false)
	h := mascotFrameHeight
	one := composite(mascotFrame, [][2]int{{10, 0}}, 300, h, st)
	three := composite(mascotFrame, [][2]int{{10, 0}, {90, 0}, {170, 0}}, 300, h, st)
	if countInk(three) < countInk(one)*2 {
		t.Fatalf("3 figures should have ~3x the ink: c1=%d c3=%d", countInk(one), countInk(three))
	}
}

func TestCompositeVerticalPlacement(t *testing.T) {
	st := pickMascotStyle(false)
	fig := trimmedFrameOf(mascotFrameTiny)
	// same figure at two different y bands must land on different rows
	out := composite(fig, [][2]int{{2, 0}, {2, 10}}, 60, 20, st)
	top, bottom := false, false
	for i, line := range out {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if i < len(fig) {
			top = true
		}
		if i >= 10 {
			bottom = true
		}
	}
	if !top || !bottom {
		t.Fatalf("figures not placed at both y bands (top=%v bottom=%v)", top, bottom)
	}
}

func TestGrazeStaticWhenPiped(t *testing.T) {
	// non-TTY → static frame, no ANSI cursor moves, exits immediately
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"graze", "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b[?25l") || strings.Contains(out.String(), "\x1b[1A") {
		t.Fatalf("piped graze must not animate:\n%q", out.String())
	}
	if strings.Count(out.String(), "\n") < 20 {
		t.Fatalf("expected the full figure, got:\n%s", out.String())
	}
}

func TestRumbleTipAndMessage(t *testing.T) {
	run := func(args ...string) string {
		root := NewRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{"rumble", "--api-key", "k"}, args...))
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	msg := run("hello", "there")
	if !strings.Contains(msg, "hello there") {
		t.Fatalf("message not in bubble:\n%s", msg)
	}
	tip := run()
	// no-arg prints one of the curated tips
	got := false
	for _, tp := range mascotTips {
		// the tip may be wrapped across bubble lines; check a distinctive prefix
		if strings.Contains(strings.ReplaceAll(tip, "\n", " "), tp[:12]) {
			got = true
			break
		}
	}
	if !got {
		t.Fatalf("no-arg rumble did not print a known tip:\n%s", tip)
	}
}

func TestMarchStopsOnCancel(t *testing.T) {
	old := marchTick
	marchTick = time.Millisecond
	defer func() { marchTick = old }()
	app := &App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, StdoutIsTTY: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: march must return promptly and restore the cursor
	if err := marchMascots(ctx, app, pickMascotStyle(false), 2); err != nil {
		t.Fatal(err)
	}
	out := app.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "\x1b[?25h") {
		t.Fatalf("cursor not restored: %q", out)
	}
}

func TestMascotSmallFrameShape(t *testing.T) {
	if len(mascotFrameSmall) == 0 {
		t.Fatal("small frame empty — did logo2frames.py run?")
	}
	all := strings.Join(mascotFrameSmall, "")
	for _, c := range []string{"y", "o", "k"} {
		if !strings.Contains(all, c) {
			t.Fatalf("small frame missing cell kind %q", c)
		}
	}
	if len(mascotFrameSmall) >= len(mascotFrame) {
		t.Fatal("small frame should be shorter than the full frame")
	}
}

func TestCompositeClipsToWidth(t *testing.T) {
	st := pickMascotStyle(false) // mono: 1 rune per cell, easy to measure width
	const width = 50
	// figures straddling both edges must never produce a line wider than width
	frame := composite(mascotFrame, [][2]int{{width - 5, 0}, {-10, 0}}, width, mascotFrameHeight, st)
	for i, line := range frame {
		if n := len([]rune(line)); n > width {
			t.Fatalf("row %d is %d cols wide, exceeds terminal width %d — would wrap and stripe", i, n, width)
		}
	}
}

func TestRainRNGDeterministicAndBounded(t *testing.T) {
	a := &rainRNG{s: 12345}
	b := &rainRNG{s: 12345}
	for i := 0; i < 100; i++ {
		x, y := a.Intn(10), b.Intn(10)
		if x != y {
			t.Fatalf("same seed diverged at %d: %d vs %d", i, x, y)
		}
		if x < 0 || x >= 10 {
			t.Fatalf("Intn(10) out of range: %d", x)
		}
	}
	if a.Intn(0) != 0 {
		t.Fatal("Intn(0) must be 0")
	}
}

func TestRainHerdStopsOnCancel(t *testing.T) {
	old := rainTick
	rainTick = time.Millisecond
	defer func() { rainTick = old }()
	app := &App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, StdoutIsTTY: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := rainHerd(ctx, app, pickMascotStyle(false), 6); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(app.Stdout.(*bytes.Buffer).String(), "\x1b[?25h") {
		t.Fatal("cursor not restored after rain")
	}
}

func TestRumbleTetherReachesHead(t *testing.T) {
	fig := trimmedFrameOf(mascotFrameSmall)
	head := headColumn(fig)
	if head <= 0 {
		t.Fatalf("head column not found: %d", head)
	}
	out := rumbleLayout("hi", pickMascotStyle(false))
	// find the last tether line (a lone backslash) and the first figure row
	var tetherCol = -1
	for _, l := range out {
		t := strings.TrimRight(l, " ")
		if strings.HasSuffix(t, "\\") && strings.Count(t, "\\") == 1 {
			tetherCol = len(t) - 1
		}
	}
	if tetherCol < 0 {
		t.Fatalf("no tether in layout:\n%s", strings.Join(out, "\n"))
	}
	// the tether should land within a couple columns of the head, not float away
	if diff := head - tetherCol; diff < -2 || diff > 3 {
		t.Fatalf("tether at col %d does not meet head at col %d", tetherCol, head)
	}
}
