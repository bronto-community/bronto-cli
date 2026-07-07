// Package timerange converts CLI time flags (--since / --from / --to) into
// the Bronto search API's time parameters: a relative time_range string
// ("Last 15 minutes") or absolute from_ts/to_ts unix-millisecond bounds.
// The API treats the two as mutually exclusive.
package timerange

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

type Spec struct {
	TimeRange string
	FromTs    int64
	ToTs      int64
}

func (s Spec) IsZero() bool { return s.TimeRange == "" && s.FromTs == 0 && s.ToTs == 0 }

var tokenRe = regexp.MustCompile(`([0-9]+)([smhdw])`)

var unitDur = map[string]time.Duration{
	"s": time.Second, "m": time.Minute, "h": time.Hour,
	"d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
}

var unitName = map[string]string{
	"s": "second", "m": "minute", "h": "hour", "d": "day", "w": "week",
}

func Resolve(since, from, to string, now func() time.Time) (Spec, error) {
	if now == nil {
		now = time.Now
	}
	if since != "" && (from != "" || to != "") {
		return Spec{}, clierr.New("usage_conflicting_time_flags",
			"--since cannot be combined with --from/--to")
	}
	if since != "" {
		return resolveSince(since, now)
	}
	if to != "" && from == "" {
		return Spec{}, clierr.New("usage_invalid_time_flags",
			"--to requires --from").WithHint("Provide both bounds, or use --since for a relative range.")
	}
	if from != "" {
		fromT, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return Spec{}, clierr.New("usage_invalid_time_flags",
				fmt.Sprintf("--from is not RFC3339: %q", from))
		}
		toT := now()
		if to != "" {
			toT, err = time.Parse(time.RFC3339, to)
			if err != nil {
				return Spec{}, clierr.New("usage_invalid_time_flags",
					fmt.Sprintf("--to is not RFC3339: %q", to))
			}
		}
		return Spec{FromTs: fromT.UnixMilli(), ToTs: toT.UnixMilli()}, nil
	}
	return Spec{}, nil
}

func resolveSince(since string, now func() time.Time) (Spec, error) {
	tokens := tokenRe.FindAllStringSubmatch(since, -1)
	consumed := 0
	for _, tok := range tokens {
		consumed += len(tok[0])
	}
	if len(tokens) == 0 || consumed != len(since) {
		return Spec{}, clierr.New("usage_invalid_since",
			fmt.Sprintf("cannot parse --since %q", since)).
			WithHint("Use forms like 30s, 15m, 1h, 2d, 1w, or compounds like 1h30m.")
	}
	if len(tokens) == 1 {
		n, err := strconv.ParseInt(tokens[0][1], 10, 64)
		if err != nil {
			return Spec{}, clierr.New("usage_invalid_since",
				fmt.Sprintf("cannot parse --since %q: number too large", since))
		}
		unit := unitName[tokens[0][2]]
		if n != 1 {
			unit += "s"
		}
		return Spec{TimeRange: fmt.Sprintf("Last %d %s", n, unit)}, nil
	}
	var total time.Duration
	for _, tok := range tokens {
		n, err := strconv.ParseInt(tok[1], 10, 64)
		if err != nil {
			return Spec{}, clierr.New("usage_invalid_since",
				fmt.Sprintf("cannot parse --since %q: number too large", since))
		}
		total += time.Duration(n) * unitDur[tok[2]]
	}
	end := now()
	return Spec{FromTs: end.Add(-total).UnixMilli(), ToTs: end.UnixMilli()}, nil
}
