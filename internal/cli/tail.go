package cli

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/bronto"
	"github.com/svrnm/bronto-cli/internal/clierr"
	"github.com/svrnm/bronto-cli/internal/output"
	"github.com/svrnm/bronto-cli/internal/timerange"
)

func newTailCmd() *cobra.Command {
	var (
		datasets           []string
		fromExpr           string
		interval           time.Duration
		window             string
		limit              int
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
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			ids, expr, err := resolveDataset(app, datasets, fromExpr)
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

			format, err := output.DetectFormat(app.OutputFlag, app.StdoutIsTTY, true)
			if err != nil {
				return err
			}
			p := output.NewPrinter(app.Stdout, format)
			humanMode := format == output.FormatTable // TTY default
			if !app.Quiet {
				_, _ = fmt.Fprintf(app.Stderr, "Tailing every %s (window %s). Ctrl-C to stop.\n", interval, window)
			}

			mrf := false
			req := bronto.SearchRequest{
				From: ids, FromExpr: expr, Time: spec, Where: where,
				Select: []string{"@time", "@raw", "@sequence", "@origin"},
				Limit:  limit, MostRecentFirst: &mrf,
			}
			client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
			dedup := bronto.NewDedup(20000)

			for {
				resp, err := client.Search(cmd.Context(), req)
				if err != nil {
					if cmd.Context().Err() != nil {
						return nil // cancelled mid-request: clean exit
					}
					return err
				}
				batch := resp.EventRows()
				fresh := batch[:0:0]
				for _, ev := range batch {
					if dedup.Admit(dedup.Key(ev)) {
						fresh = append(fresh, ev)
					}
				}
				bronto.SortEvents(fresh)
				for _, ev := range fresh {
					raw := fmt.Sprint(ev["@raw"])
					if !filter.Match(raw) {
						continue
					}
					if humanMode {
						_, _ = fmt.Fprintln(app.Stdout, renderTailLine(ev, raw, hlRes, app.Color))
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
	f.StringArrayVarP(&datasets, "dataset", "d", nil, "dataset UUID to tail (repeatable)")
	f.StringVar(&fromExpr, "from-expr", "", "dataset selector expression")
	f.DurationVar(&interval, "interval", 3*time.Second, "polling interval (min 1s)")
	f.StringVar(&window, "window", "30s", "per-poll lookback window (single unit)")
	f.IntVarP(&limit, "limit", "n", 500, "max events per poll")
	f.StringArrayVar(&includes, "include", nil, "only show lines matching this regex (repeatable, ANDed)")
	f.StringArrayVar(&excludes, "exclude", nil, "hide lines matching this regex (repeatable)")
	f.StringArrayVar(&highlights, "highlight", nil, "highlight regex matches in the output (repeatable)")
	f.BoolVar(&noFollow, "no-follow", false, "fetch the current window once, then exit")
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

func buildFilter(includes, excludes []string) (bronto.TailFilter, error) {
	inc, err := compileRegexps(includes)
	if err != nil {
		return bronto.TailFilter{}, err
	}
	exc, err := compileRegexps(excludes)
	if err != nil {
		return bronto.TailFilter{}, err
	}
	return bronto.TailFilter{Include: inc, Exclude: exc}, nil
}

var originColors = []string{"31", "32", "33", "34", "35", "36"}

func renderTailLine(ev map[string]any, raw string, highlights []*regexp.Regexp, color bool) string {
	ts := fmt.Sprint(ev["@time"])
	origin := ""
	if o, ok := ev["@origin"]; ok && o != nil {
		origin = fmt.Sprint(o)
	}
	if color {
		for _, re := range highlights {
			raw = re.ReplaceAllString(raw, "\x1b[1;33m$0\x1b[0m")
		}
		line := "\x1b[2m" + ts + "\x1b[0m "
		if origin != "" {
			h := fnv.New32a()
			_, _ = h.Write([]byte(origin))
			c := originColors[h.Sum32()%uint32(len(originColors))]
			line += "\x1b[" + c + "m" + origin + "\x1b[0m "
		}
		return line + raw
	}
	if origin != "" {
		return ts + " " + origin + " " + raw
	}
	return ts + " " + raw
}
