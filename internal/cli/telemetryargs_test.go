package cli

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.opentelemetry.io/otel/attribute"

	"github.com/bronto-community/bronto-cli/internal/telemetry"
)

// TestTelemetryClassifiesEveryArg is the guard behind flagClasses and
// argPlaceholders: every flag and positional argument in the tree must have
// a class, and every class entry must still be used.
func TestTelemetryClassifiesEveryArg(t *testing.T) {
	usedFlags, usedArgs := map[string]bool{}, map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		visit := func(f *pflag.Flag) {
			if safeFlagTypes[f.Value.Type()] {
				return
			}
			if _, ok := flagClasses[f.Name]; !ok {
				t.Errorf("%s: flag --%s (%s) has no class in flagClasses", c.CommandPath(), f.Name, f.Value.Type())
			}
			usedFlags[f.Name] = true
		}
		c.LocalFlags().VisitAll(visit)
		for _, p := range usePlaceholders(c) {
			if _, ok := argPlaceholders[p]; !ok {
				t.Errorf("%s: positional <%s> has no class in argPlaceholders", c.CommandPath(), p)
			}
			usedArgs[p] = true
		}
		// A Use token that looks like an argument but isn't parsed as one
		// would silently fall back to argName; catch new syntax early.
		fields := strings.Fields(c.Use)
		for i := 1; i < len(fields); i++ {
			f := fields[i]
			if strings.HasPrefix(f, "-") || strings.HasPrefix(f, "[-") {
				i++
				continue
			}
			if strings.HasPrefix(f, "<") || strings.HasPrefix(f, "[") {
				if placeholderRE.FindStringSubmatch(f) == nil {
					t.Errorf("%s: Use token %q isn't a recognized placeholder", c.CommandPath(), f)
				}
			}
		}
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(NewRootCmd())
	for name := range flagClasses {
		if !usedFlags[name] {
			t.Errorf("flagClasses has --%s, which no command defines", name)
		}
	}
	for p := range argPlaceholders {
		if !usedArgs[p] {
			t.Errorf("argPlaceholders has <%s>, which no Use line names", p)
		}
	}
}

func TestCommandAttributes(t *testing.T) {
	exe := os.Args[0]
	cases := []struct {
		name string
		argv []string
		lvl  telemetry.Capture
		want map[string]any
	}{
		{
			name: "search default",
			argv: []string{"search", "status >= 500 AND user = 'a@b.c'", "-d", "prod/api", "-d", "prod/web",
				"--since", "1h", "--limit", "5", "-o", "json", "--api-key", "sk-secret",
				"--eq", "service=checkout", "--jq", ".[] | select(.user == \"bob\")"},
			lvl: telemetry.CaptureDefault,
			want: map[string]any{
				"bronto.flags":        []string{"api-key", "dataset", "eq", "jq", "limit", "output", "since"},
				"bronto.flag.dataset": []string{"prod/api", "prod/web"},
				"bronto.flag.since":   "1h",
				"bronto.flag.limit":   "5",
				"bronto.flag.output":  "json",
				"bronto.flag.eq":      []string{"service=?"},
				"bronto.args":         []string{"status >= ? AND user = ?"},
			},
		},
		{
			name: "flags before the command, free-text query, url",
			argv: []string{"--base-url", "https://u:p@api.example.com/v1?token=x", "search", "checkout failed", "--from-expr", "log_id = 'x'"},
			lvl:  telemetry.CaptureDefault,
			want: map[string]any{
				"bronto.flags":          []string{"base-url", "from-expr"},
				"bronto.flag.base-url":  "https://api.example.com/v1?token=REDACTED",
				"bronto.flag.from-expr": "log_id = ?",
				"bronto.args":           []string{"? ?"},
			},
		},
		{
			name: "unsanitizable query",
			argv: []string{"query", "check", "x = 1 | stats count()"},
			lvl:  telemetry.CaptureDefault,
			want: map[string]any{"bronto.args": []string{"REDACTED"}},
		},
		{
			name: "api path and body fields",
			argv: []string{"api", "GET", "/logs?where=x", "-f", "password=hunter2", "--input", "/home/me/body.json"},
			lvl:  telemetry.CaptureDefault,
			want: map[string]any{
				"bronto.flags":      []string{"field", "input"},
				"bronto.flag.field": []string{"password=?"},
				"bronto.args":       []string{"GET", "/logs?where=REDACTED"},
			},
		},
		{
			name: "free-text positional",
			argv: []string{"ask", "why is checkout slow for bob@example.com?", "-d", "prod"},
			lvl:  telemetry.CaptureDefault,
			want: map[string]any{
				"bronto.flags":        []string{"dataset"},
				"bronto.flag.dataset": "prod",
				"bronto.args":         []string{"REDACTED"},
			},
		},
		{
			name: "resource id",
			argv: []string{"monitors", "get", "8f3a"},
			lvl:  telemetry.CaptureDefault,
			want: map[string]any{"bronto.args": []string{"8f3a"}},
		},
		{
			name: "none",
			argv: []string{"search", "x", "--since", "1h"},
			lvl:  telemetry.CaptureNone,
			want: map[string]any{},
		},
		{
			name: "all keeps raw values but not the api key",
			argv: []string{"search", "user = 'a@b.c'", "--jq", ".x", "--api-key", "sk-secret", "--from-expr=b = 2"},
			lvl:  telemetry.CaptureAll,
			want: map[string]any{
				"bronto.flags":          []string{"api-key", "from-expr", "jq"},
				"bronto.flag.jq":        ".x",
				"bronto.flag.from-expr": "b = 2",
				"bronto.args":           []string{"user = 'a@b.c'"},
				"process.command_args": []string{exe, "search", "user = 'a@b.c'", "--jq", ".x",
					"--api-key", "REDACTED", "--from-expr=b = 2"},
			},
		},
		{
			name: "all redacts --api-key=value",
			argv: []string{"ping", "--api-key=sk-secret"},
			lvl:  telemetry.CaptureAll,
			want: map[string]any{
				"bronto.flags":         []string{"api-key"},
				"process.command_args": []string{exe, "ping", "--api-key=REDACTED"},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := NewRootCmd()
			cmd, _, err := root.Find(c.argv)
			if err != nil {
				t.Fatal(err)
			}
			got := attrMap(commandAttributes(root, cmd, c.argv, c.lvl))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("attributes:\n got %v\nwant %v", got, c.want)
			}
			if s := fmt.Sprint(got); strings.Contains(s, "sk-secret") {
				t.Errorf("api key recorded: %s", s)
			}
		})
	}
}

func attrMap(kvs []attribute.KeyValue) map[string]any {
	m := map[string]any{}
	for _, kv := range kvs {
		switch kv.Value.Type() {
		case attribute.STRINGSLICE:
			m[string(kv.Key)] = kv.Value.AsStringSlice()
		default:
			m[string(kv.Key)] = kv.Value.String()
		}
	}
	return m
}
