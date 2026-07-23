// Package patterns clusters log lines into templates (drain-style): the
// fastest way to comprehend a firehose is "you have 12 shapes of line,
// here are the counts".
package patterns

import (
	"regexp"
	"sort"
	"strings"
)

type Cluster struct {
	Template string
	Count    int
	Example  string
}

var (
	numRe     = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
	numUnitRe = regexp.MustCompile(`^-?\d+(\.\d+)?(ms|us|ns|s|m|h|d|%|b|kb|mb|gb|KB|MB|GB)$`)
	uuidRe    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hexRe     = regexp.MustCompile(`^[0-9a-fA-F]{12,}$`)
)

// simThreshold: fraction of positions that must match to join a cluster.
const simThreshold = 0.5

type cluster struct {
	tokens  []string
	count   int
	example string
}

// Extract clusters lines into templates. Tokens are pre-masked (<num>,
// <uuid>, <hex>); within a cluster, positions that differ collapse to
// <*>. Lines are bucketed by token count, so templates never mix arities.
func Extract(lines []string) []Cluster {
	buckets := map[int][]*cluster{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		toks := tokenize(line)
		var best *cluster
		bestSim := 0.0
		for _, c := range buckets[len(toks)] {
			if s := similarity(c.tokens, toks); s > bestSim {
				best, bestSim = c, s
			}
		}
		if best == nil || bestSim < simThreshold {
			buckets[len(toks)] = append(buckets[len(toks)], &cluster{tokens: toks, count: 1, example: line})
			continue
		}
		best.count++
		for i := range best.tokens {
			if best.tokens[i] != toks[i] {
				best.tokens[i] = "<*>"
			}
		}
	}
	var out []Cluster
	for _, cs := range buckets {
		for _, c := range cs {
			out = append(out, Cluster{Template: strings.Join(c.tokens, " "), Count: c.count, Example: c.example})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Template < out[j].Template
	})
	return out
}

func tokenize(line string) []string {
	fields := strings.Fields(line)
	for i, f := range fields {
		fields[i] = maskToken(f)
	}
	return fields
}

// maskToken masks variable parts inside one whitespace token: whole
// values, key=value right-hand sides, and per-/-segment path components.
func maskToken(tok string) string {
	// key=value: mask the value side only
	if k, v, ok := strings.Cut(tok, "="); ok && k != "" {
		return k + "=" + maskToken(v)
	}
	if strings.Contains(tok, "/") && len(tok) > 1 {
		segs := strings.Split(tok, "/")
		for i, s := range segs {
			segs[i] = maskCore(s)
		}
		return strings.Join(segs, "/")
	}
	return maskCore(tok)
}

func maskCore(tok string) string {
	core := strings.Trim(tok, `"',;:()[]{}`)
	if core == "" {
		return tok
	}
	switch {
	case numRe.MatchString(core), numUnitRe.MatchString(core):
		return strings.Replace(tok, core, "<num>", 1)
	case uuidRe.MatchString(core):
		return strings.Replace(tok, core, "<uuid>", 1)
	case hexRe.MatchString(core):
		return strings.Replace(tok, core, "<hex>", 1)
	}
	return tok
}

func similarity(a, b []string) float64 {
	if len(a) == 0 {
		return 0
	}
	match := 0
	for i := range a {
		if a[i] == b[i] || a[i] == "<*>" {
			match++
		}
	}
	return float64(match) / float64(len(a))
}
