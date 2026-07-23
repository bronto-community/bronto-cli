package cli

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

func newTailCmd() *cobra.Command {
	var (
		datasets           []string
		fromExpr           string
		interval           time.Duration
		window             string
		limit              int
		dedupSize          int
		includes, excludes []string
		highlights         []string
		noFollow           bool
	)
	cmd := &cobra.Command{
		Use:   "tail [query]",
		Short: "Follow new events live (like tail -f)",
		Long: "Follows new events live (like tail -f), polling on --interval and looking back --window\n" +
			"each poll. Known limitation: out-of-order events arriving later than one window are not\n" +
			"re-ordered across polls (per-batch ordering only); a cross-poll reorder buffer is future work.",
		Example: "  bronto tail\n" +
			"  bronto tail \"level = 'error'\" --include 'timeout' --exclude 'healthz'\n" +
			"  bronto tail --no-follow --window 5m   # catch up, then exit",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if interval < time.Second {
				return clierr.New("usage_invalid_interval", "interval must be at least 1s")
			}
			if dedupSize <= 0 {
				return clierr.New("usage_invalid_dedup_size", "dedup-size must be a positive integer")
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			tailFormat, err := app.DetectFormat(true)
			if err != nil {
				return err
			}
			// tail's table renderer DOES support --fields projection
			// (klp-style, see renderTailLine), so only --fields=? needs a
			// machine format here: the streaming table view can't enumerate
			// available field names up front the way a buffered list can.
			if tailFormat == output.FormatTable && app.ListFieldsOnly {
				return clierr.New("usage_invalid_flags",
					"--fields ? is not supported by tail's table view").
					WithHint("Use -o jsonl to list available field names.")
			}
			ids, expr, err := resolveDataset(cmd.Context(), app, datasets, fromExpr)
			if err != nil {
				return err
			}
			spec, err := timerange.Resolve(window, "", "", nil)
			if err != nil {
				return err
			}
			if spec.TimeRange == "" {
				return clierr.New("usage_invalid_since", "window must be a single-unit duration (e.g. 30s, 5m)")
			}
			filter, err := buildFilter(includes, excludes)
			if err != nil {
				return err
			}
			hlRes, err := compileRegexps(highlights)
			if err != nil {
				return err
			}
			where := ""
			if len(args) == 1 {
				where = args[0]
			}

			if tailFormat == output.FormatJSON || tailFormat == output.FormatCSV {
				return clierr.New("usage_invalid_output_format",
					fmt.Sprintf("tail streams events and cannot produce %s; use jsonl, raw, or table", tailFormat))
			}
			p, err := app.PrinterFor(tailFormat)
			if err != nil {
				return err
			}
			humanMode := tailFormat == output.FormatTable // table format: TTY default or explicit -o table; PrintRow cannot render tables
			if !app.Quiet {
				_, _ = fmt.Fprintf(app.Stderr, "Tailing every %s (window %s). Ctrl-C to stop.\n", interval, window)
			}

			mrf := false
			selects := make([]string, 0, 5+len(filter.Fields())+len(app.FieldFilter))
			selects = append(selects, "@time", "@raw", "@sequence", "@origin", "@status")
			selects = append(selects, filter.Fields()...)
			selects = append(selects, app.FieldFilter...)
			req := bronto.SearchRequest{
				From: ids, FromExpr: expr, Time: spec, Where: where,
				Select: dedupStrings(selects),
				Limit:  limit, MostRecentFirst: &mrf,
			}
			client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
			dedup := bronto.NewDedup(dedupSize)

			for {
				resp, err := client.Search(cmd.Context(), req)
				if err != nil {
					if cmd.Context().Err() != nil {
						return nil //nolint:nilerr // cancelled mid-request (SIGINT): clean exit by contract
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
					raw := fmt.Sprint(ev["@raw"])
					if !filter.MatchEvent(ev, raw) {
						continue
					}
					if humanMode {
						_, _ = fmt.Fprintln(app.Stdout, renderTailLine(ev, raw, hlRes, app.Color, app.FieldFilter))
						continue
					}
					if err := p.PrintRow(nil, bronto.Flatten(ev)); err != nil {
						return err
					}
				}
				if noFollow {
					return nil
				}
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(interval):
				}
			}
		},
	}
	f := cmd.Flags()
	f.StringArrayVarP(&datasets, "dataset", "d", nil, "dataset name or UUID to tail (repeatable)")
	f.StringVar(&fromExpr, "from-expr", "", "dataset selector expression")
	f.DurationVar(&interval, "interval", 3*time.Second, "polling interval (min 1s)")
	f.StringVar(&window, "window", "30s", "per-poll lookback window (single unit)")
	f.IntVarP(&limit, "limit", "n", 500, "max events per poll")
	f.StringArrayVar(&includes, "include", nil, "only show lines matching this regex (repeatable, ANDed)")
	f.StringArrayVar(&excludes, "exclude", nil, "hide lines matching this regex (repeatable)")
	f.StringArrayVar(&highlights, "highlight", nil, "highlight regex matches in the output (repeatable)")
	f.BoolVar(&noFollow, "no-follow", false, "fetch the current window once, then exit")
	f.IntVar(&dedupSize, "dedup-size", 20000, "events remembered for duplicate suppression across polls; very high-volume streams may need more")
	return cmd
}

func compileRegexps(patterns []string) ([]*regexp.Regexp, error) {
	res := make([]*regexp.Regexp, 0, len(patterns))
	for _, pat := range patterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, clierr.New("usage_invalid_regex", fmt.Sprintf("invalid regex %q: %v", pat, err))
		}
		res = append(res, re)
	}
	return res, nil
}

// fieldRulePattern recognizes the "field~regex" form of --include/
// --exclude: a field name (letters/digits/_/./-/@/$) before the first
// '~', a regex after. Anything else is a whole-line regex, so regexes
// containing '~' still work unless they *start* with something that
// looks like a field name — quote a leading char class to disambiguate.
var fieldRulePattern = regexp.MustCompile(`^([@$]?[A-Za-z0-9_][A-Za-z0-9_.-]*)~(.+)$`)

func buildFilter(includes, excludes []string) (bronto.TailFilter, error) {
	var f bronto.TailFilter
	parse := func(patterns []string, plain *[]*regexp.Regexp, rules *[]bronto.FieldRule) error {
		for _, p := range patterns {
			if m := fieldRulePattern.FindStringSubmatch(p); m != nil {
				re, err := regexp.Compile(m[2])
				if err != nil {
					return clierr.New("usage_invalid_regex",
						fmt.Sprintf("invalid regex in field rule %q: %v", p, err))
				}
				*rules = append(*rules, bronto.FieldRule{Field: m[1], Re: re})
				continue
			}
			re, err := regexp.Compile(p)
			if err != nil {
				return clierr.New("usage_invalid_regex", fmt.Sprintf("invalid regex %q: %v", p, err))
			}
			*plain = append(*plain, re)
		}
		return nil
	}
	if err := parse(includes, &f.Include, &f.IncludeFields); err != nil {
		return bronto.TailFilter{}, err
	}
	if err := parse(excludes, &f.Exclude, &f.ExcludeFields); err != nil {
		return bronto.TailFilter{}, err
	}
	return f, nil
}

var originColors = []string{"31", "32", "33", "34", "35", "36"}

// colorIndex maps an fnv-32a origin hash to an originColors index. Unsigned
// math is load-bearing: int(uint32) is negative on 32-bit builds for
// hashes >= 2^31, and a negative modulo would panic with an out-of-range
// index. Shared with the test so the two can't diverge.
func colorIndex(sum uint32) int {
	// #nosec G115 -- len(originColors) is a fixed 6 and the modulus result is 0..5; no overflow is possible
	return int(sum % uint32(len(originColors)))
}

// levelColor maps a severity value to an ANSI prefix ("" = uncolored).
// Shared by the tail renderer and the table cell colorizer (tailspin
// pattern: errors jump out with zero configuration).
func levelColor(level string) string {
	switch strings.ToLower(level) {
	case "error", "fatal", "critical", "err":
		return "\x1b[1;31m"
	case "warn", "warning":
		return "\x1b[33m"
	case "debug", "trace":
		return "\x1b[2m"
	}
	return ""
}

func renderTailLine(ev map[string]any, raw string, highlights []*regexp.Regexp, color bool, fields []string) string {
	// --fields: render exactly the requested fields, klp-style.
	if len(fields) > 0 {
		vals := make([]string, len(fields))
		for i, f := range fields {
			if v, ok := ev[f]; ok && v != nil {
				vals[i] = fmt.Sprint(v)
			} else {
				vals[i] = "-"
			}
		}
		line := strings.Join(vals, "  ")
		if color {
			if lc := levelColor(fmt.Sprint(ev["@status"])); lc != "" {
				line = lc + line + "\x1b[0m"
			}
		}
		return line
	}

	ts := fmt.Sprint(ev["@time"])
	status := ""
	if s, ok := ev["@status"]; ok && s != nil {
		status = fmt.Sprint(s)
	}
	origin := ""
	if o, ok := ev["@origin"]; ok && o != nil {
		origin = fmt.Sprint(o)
	}
	if color {
		for _, re := range highlights {
			raw = re.ReplaceAllString(raw, "\x1b[1;33m$0\x1b[0m")
		}
		line := "\x1b[2m" + ts + "\x1b[0m "
		if status != "" {
			if lc := levelColor(status); lc != "" {
				line += lc + strings.ToUpper(status) + "\x1b[0m "
			} else {
				line += status + " "
			}
		}
		if origin != "" {
			h := fnv.New32a()
			_, _ = h.Write([]byte(origin))
			c := originColors[colorIndex(h.Sum32())]
			line += "\x1b[" + c + "m" + origin + "\x1b[0m "
		}
		return line + raw
	}
	parts := []string{ts}
	if status != "" {
		parts = append(parts, status)
	}
	if origin != "" {
		parts = append(parts, origin)
	}
	return strings.Join(append(parts, raw), " ")
}

// dedupStrings preserves order, drops repeats and empties.
func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
