package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

const (
	replPageSize   = 20
	replHistoryMax = 500
	replTailWindow = "Last 30 seconds"
	replRawCap     = 200 // runes of @raw shown per result line
	replFetchLimit = 200 // events fetched per query; paged replPageSize at a time
)

// replTailEvery is the tail poll interval; a var so tests can shrink it.
var replTailEvery = 3 * time.Second

// replTerm is the line-reading seam: tests replace it to drive the loop
// without a PTY.
type replTerm interface {
	ReadLine() (string, error)
	SetPrompt(prompt string)
}

// termFactory builds the interactive terminal; a package var so tests can
// substitute a scripted reader.
var newReplTerm = func(app *App) (replTerm, func(), error) {
	fd := int(os.Stdin.Fd())
	t := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{os.Stdin, os.Stdout}, "> ")
	loadReplHistory(t, os.Getenv)
	raw := func() (func(), error) {
		old, err := term.MakeRaw(fd)
		if err != nil {
			return nil, err
		}
		return func() { _ = term.Restore(fd, old) }, nil
	}
	rt := &rawTerm{t: t, raw: raw}
	cleanup := func() { saveReplHistory(t, os.Getenv) }
	_ = app
	return rt, cleanup, nil
}

// rawTerm toggles the tty into raw mode only while a line is being read,
// so query output in between renders through the normal cooked tty.
type rawTerm struct {
	t   *term.Terminal
	raw func() (func(), error)
}

func (r *rawTerm) ReadLine() (string, error) {
	restore, err := r.raw()
	if err != nil {
		return "", err
	}
	defer restore()
	return r.t.ReadLine()
}

func (r *rawTerm) SetPrompt(p string) { r.t.SetPrompt(p) }

// replHistoryPath mirrors the config-file location rules (BRONTO_CONFIG_DIR
// override, else the OS user config dir), keeping all bronto state together.
func replHistoryPath(getenv func(string) string) string {
	dir := getenv("BRONTO_CONFIG_DIR")
	if dir == "" {
		d, err := os.UserConfigDir()
		if err != nil {
			return ""
		}
		dir = d
	}
	return filepath.Join(dir, "bronto", "repl_history")
}

func loadReplHistory(t *term.Terminal, getenv func(string) string) {
	path := replHistoryPath(getenv)
	if path == "" {
		return
	}
	b, err := os.ReadFile(path) // #nosec G304 -- fixed filename under the user's own config dir
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line != "" {
			t.History.Add(line)
		}
	}
}

func saveReplHistory(t *term.Terminal, getenv func(string) string) {
	path := replHistoryPath(getenv)
	if path == "" {
		return
	}
	n := t.History.Len()
	if n > replHistoryMax {
		n = replHistoryMax
	}
	lines := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- { // At(0) is most recent; persist oldest-first
		lines = append(lines, t.History.At(i))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// replSession is the mutable state behind the prompt.
type replSession struct {
	app    *App
	client *bronto.Client
	ids    []string
	expr   string
	ref    string // dataset ref for the prompt, as the user typed it
	since  string // canonical time_range, e.g. "Last 15 minutes"
	limit  int

	lastWhere string
	rows      []map[string]any
	total     string
	page      int
}

func newReplCmd() *cobra.Command {
	var (
		datasets []string
		fromExpr string
		since    string
	)
	cmd := &cobra.Command{
		Use:   "repl",
		Short: "Interactive query prompt (psql-style)",
		Long: "An interactive prompt for iterative log investigation: type a WHERE expression to run\n" +
			"it, refine, rerun. Meta-commands (\\help lists them) switch dataset and window, page\n" +
			"through results, or drop into a live tail. History persists across sessions.",
		Example: "  bronto repl -d payments-api\n" +
			"  bronto repl -d payments-api --since 1h",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !stdinIsTTY() || !stdoutIsTTY() {
				return clierr.New("usage_repl_requires_tty",
					"bronto repl is interactive and requires a terminal").
					WithHint("Scripts should use 'bronto search' — it emits JSONL when piped.")
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			ids, expr, err := resolveDataset(cmd.Context(), app, datasets, fromExpr)
			if err != nil {
				return err
			}
			spec, err := timerange.Resolve(since, "", "", nil)
			if err != nil {
				return err
			}
			if spec.TimeRange == "" {
				spec.TimeRange = "Last 15 minutes"
			}
			ref := "default"
			if len(datasets) > 0 {
				ref = strings.Join(datasets, ",")
			} else if fromExpr != "" {
				ref = fromExpr
			}
			s := &replSession{
				app:    app,
				client: bronto.NewClient(app.HTTPClient, app.Config.BaseURL()),
				ids:    ids, expr: expr, ref: ref,
				since: spec.TimeRange,
				limit: replFetchLimit,
			}
			t, cleanup, err := newReplTerm(app)
			if err != nil {
				return err
			}
			defer cleanup()
			_, _ = fmt.Fprintln(app.Stdout, `Type a WHERE expression to run it; \help lists meta-commands; \q quits.`)
			return s.loop(t)
		},
	}
	f := cmd.Flags()
	f.StringArrayVarP(&datasets, "dataset", "d", nil, "dataset name or UUID to query (repeatable)")
	f.StringVar(&fromExpr, "from-expr", "", "dataset selector expression")
	f.StringVar(&since, "since", "", "initial lookback window (default 15m)")
	return cmd
}

func (s *replSession) loop(t replTerm) error {
	for {
		t.SetPrompt(fmt.Sprintf("bronto (%s, %s)> ", s.ref, strings.ToLower(s.since)))
		line, err := t.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				_, _ = fmt.Fprintln(s.app.Stdout)
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" || strings.HasPrefix(line, `\`) {
			quit, err := s.meta(line)
			if err != nil {
				s.reportErr(err)
			}
			if quit {
				return nil
			}
			continue
		}
		if err := s.runQuery(line); err != nil {
			s.reportErr(err)
		}
	}
}

// reportErr prints an error and keeps the session alive: a typo in a
// query must never cost the accumulated context.
func (s *replSession) reportErr(err error) {
	var ce *clierr.Error
	if errors.As(err, &ce) {
		_, _ = fmt.Fprintf(s.app.Stderr, "Error: %s\n", ce.Message)
		if ce.Hint != "" {
			_, _ = fmt.Fprintf(s.app.Stderr, "Hint: %s\n", ce.Hint)
		}
		return
	}
	_, _ = fmt.Fprintf(s.app.Stderr, "Error: %v\n", err)
}

func (s *replSession) meta(line string) (quit bool, err error) {
	fields := strings.Fields(line)
	cmd, args := fields[0], fields[1:]
	switch cmd {
	case `\q`, `\quit`, "exit", "quit":
		return true, nil
	case `\help`, `\?`:
		s.printHelp()
	case `\since`:
		if len(args) != 1 {
			return false, clierr.New("usage_invalid_since", `\since takes one duration, e.g. \since 1h`)
		}
		spec, err := timerange.Resolve(args[0], "", "", nil)
		if err != nil {
			return false, err
		}
		if spec.TimeRange == "" {
			return false, clierr.New("usage_invalid_since", "the window must be a single-unit duration (e.g. 30s, 5m, 1h)")
		}
		s.since = spec.TimeRange
		_, _ = fmt.Fprintf(s.app.Stdout, "window: %s\n", strings.ToLower(s.since))
	case `\limit`:
		if len(args) != 1 {
			return false, clierr.New("usage_invalid_limit", `\limit takes one number, e.g. \limit 500`)
		}
		n, convErr := strconv.Atoi(args[0])
		if convErr != nil || n < 1 || n > 10000 {
			return false, clierr.New("usage_invalid_limit", "limit must be between 1 and 10000")
		}
		s.limit = n
		_, _ = fmt.Fprintf(s.app.Stdout, "limit: %d\n", n)
	case `\d`:
		return false, s.switchOrListDatasets(args)
	case `\fields`:
		return false, s.showFields()
	case `\more`:
		s.morePage()
	case `\tail`:
		return false, s.tail()
	default:
		return false, clierr.New("usage_unknown_meta",
			fmt.Sprintf("unknown meta-command %s", cmd)).
			WithHint(`\help lists the available meta-commands.`)
	}
	return false, nil
}

func (s *replSession) printHelp() {
	_, _ = fmt.Fprint(s.app.Stdout, `Type a WHERE expression (e.g. status >= 500) to run it against the current dataset/window.
  \d              list datasets
  \d <dataset>    switch dataset (name, collection/name, or UUID)
  \since <dur>    set the lookback window (30s, 5m, 1h, ...)
  \limit <n>      events fetched per query (default `+strconv.Itoa(replFetchLimit)+`)
  \fields         discover field names in the current window
  \more           next page of the last result
  \tail           follow new events live (Ctrl-C returns to the prompt)
  \q              quit (also: exit, quit, Ctrl-D)
`)
}

func (s *replSession) switchOrListDatasets(args []string) error {
	ctx, stop := replCtx()
	defer stop()
	if len(args) == 0 {
		ds, err := listDatasets(ctx, s.app)
		if err != nil {
			return err
		}
		for _, d := range ds {
			_, _ = fmt.Fprintln(s.app.Stdout, d.qualified())
		}
		return nil
	}
	ids, expr, err := resolveDataset(ctx, s.app, args, "")
	if err != nil {
		return err
	}
	s.ids, s.expr, s.ref = ids, expr, strings.Join(args, ",")
	s.rows, s.page, s.total = nil, 0, ""
	_, _ = fmt.Fprintf(s.app.Stdout, "dataset: %s\n", s.ref)
	return nil
}

func (s *replSession) showFields() error {
	ctx, stop := replCtx()
	defer stop()
	params := url.Values{"time_range": []string{s.since}}
	if len(s.ids) == 1 {
		params.Set("log_id", s.ids[0])
	}
	var payload map[string]any
	if err := s.client.GetJSON(ctx, "/top-keys", params, &payload); err != nil {
		return err
	}
	rows := normalizeTopKeys(payload)
	if len(rows) == 0 {
		_, _ = fmt.Fprintln(s.app.Stdout, "No fields discovered in this window.")
		return nil
	}
	for _, r := range rows {
		_, _ = fmt.Fprintln(s.app.Stdout, r["key"])
	}
	return nil
}

// replCtx builds a per-action context cancelled by Ctrl-C: one abort
// kills the in-flight call, not the whole session (the root context is
// cancelled permanently on the first SIGINT, so it cannot be used here).
var replCtx = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func (s *replSession) runQuery(where string) error {
	ctx, stop := replCtx()
	defer stop()
	req := bronto.SearchRequest{
		From: s.ids, FromExpr: s.expr,
		Time:   timerange.Spec{TimeRange: s.since},
		Where:  where,
		Select: []string{"@time", "@raw"},
		Limit:  s.limit,
	}
	resp, err := s.client.Search(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			_, _ = fmt.Fprintln(s.app.Stdout, "(query aborted)")
			return nil
		}
		return err
	}
	s.lastWhere = where
	s.rows = resp.EventRows()
	s.page = 0
	s.total = ""
	if m, ok := resp.Explain["Matching events"]; ok {
		s.total = fmt.Sprint(m)
	}
	if len(s.rows) == 0 {
		_, _ = fmt.Fprintln(s.app.Stdout, "No results.")
		return nil
	}
	s.printPage()
	return nil
}

func (s *replSession) morePage() {
	if s.page*replPageSize >= len(s.rows) {
		_, _ = fmt.Fprintln(s.app.Stdout, "No more results.")
		return
	}
	s.printPage()
}

func (s *replSession) printPage() {
	start := s.page * replPageSize
	end := start + replPageSize
	if end > len(s.rows) {
		end = len(s.rows)
	}
	if s.page == 0 {
		total := s.total
		if total == "" {
			total = strconv.Itoa(len(s.rows))
		}
		note := ""
		if len(s.rows) > replPageSize {
			note = ` (\more for the next page)`
		}
		_, _ = fmt.Fprintf(s.app.Stdout, "%s events — showing %d most recent%s\n", total, end-start, note)
	}
	for _, ev := range s.rows[start:end] {
		_, _ = fmt.Fprintln(s.app.Stdout, renderReplLine(ev, s.app.Color))
	}
	s.page++
}

// renderReplLine is the compact per-event line: clock time, status, capped raw.
func renderReplLine(ev map[string]any, color bool) string {
	ts := fmt.Sprint(ev["@time"])
	if len(ts) >= 19 {
		ts = ts[11:19] // "2026-07-21 10:13:42.247 UTC" -> "10:13:42"
	}
	status := ""
	if v, ok := ev["@status"]; ok && v != nil {
		status = fmt.Sprint(v)
	}
	raw := ""
	if v, ok := ev["@raw"]; ok && v != nil {
		raw = fmt.Sprint(v)
	}
	if r := []rune(raw); len(r) > replRawCap {
		raw = string(r[:replRawCap-1]) + "…"
	}
	if color {
		ts = "\x1b[2m" + ts + "\x1b[0m"
	}
	parts := make([]string, 0, 3)
	parts = append(parts, ts)
	if status != "" {
		parts = append(parts, status)
	}
	parts = append(parts, raw)
	return strings.Join(parts, " ")
}

func (s *replSession) tail() error {
	ctx, stop := replCtx()
	defer stop()
	_, _ = fmt.Fprintln(s.app.Stdout, "tailing… (Ctrl-C returns to the prompt)")
	mrf := false
	req := bronto.SearchRequest{
		From: s.ids, FromExpr: s.expr,
		Time:   timerange.Spec{TimeRange: replTailWindow},
		Where:  s.lastWhere,
		Select: []string{"@time", "@raw", "@sequence", "@origin"},
		Limit:  500, MostRecentFirst: &mrf,
	}
	dedup := bronto.NewDedup(20000)
	for {
		resp, err := s.client.Search(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				_, _ = fmt.Fprintln(s.app.Stdout, "(tail stopped)")
				return nil //nolint:nilerr // Ctrl-C: back to the prompt by contract
			}
			return err
		}
		batch := resp.SelectedRows()
		fresh := batch[:0:0]
		for _, ev := range batch {
			if dedup.Admit(dedup.Key(ev)) {
				fresh = append(fresh, ev)
			}
		}
		bronto.SortEvents(fresh)
		for _, ev := range fresh {
			_, _ = fmt.Fprintln(s.app.Stdout, renderTailLine(ev, fmt.Sprint(ev["@raw"]), nil, s.app.Color))
		}
		select {
		case <-ctx.Done():
			_, _ = fmt.Fprintln(s.app.Stdout, "(tail stopped)")
			return nil
		case <-time.After(replTailEvery):
		}
	}
}
