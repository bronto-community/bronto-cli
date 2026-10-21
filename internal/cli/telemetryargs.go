package cli

import (
	"context"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/bronto-community/bronto-cli/internal/api"
	"github.com/bronto-community/bronto-cli/internal/query"
	"github.com/bronto-community/bronto-cli/internal/telemetry"
)

// argClass says how a flag value or positional argument is recorded on
// the root span. See docs/.../configuration/telemetry.mdx.
type argClass int

const (
	argValue  argClass = iota + 1 // as given: bounded, numeric, names and IDs
	argName                       // only that it was set; the value only with capture=all
	argQuery                      // query literals replaced by "?"
	argPairs                      // key=value with the value replaced by "?"
	argURL                        // without userinfo, query values REDACTED
	argSecret                     // never recorded, not even with capture=all
)

const redacted = "REDACTED"

// Flag types that can't carry free text; their values are always recorded.
var safeFlagTypes = map[string]bool{
	"bool": true, "int": true, "int64": true, "uint": true, "float64": true, "duration": true, "count": true,
}

// flagClasses classifies every flag whose type isn't in safeFlagTypes, by
// flag name: a flag name means the same thing on every command that has it.
// TestTelemetryClassifiesEveryArg fails when a flag or positional argument
// has no class, so a new flag is recorded only once someone has decided how.
var flagClasses = map[string]argClass{
	"by":            argValue,
	"collection":    argValue,
	"content-type":  argValue,
	"dataset":       argValue,
	"direction":     argValue,
	"fields":        argValue,
	"from":          argValue, // RFC3339 time
	"group-by":      argValue,
	"kind":          argValue,
	"operation":     argValue,
	"order-by":      argValue,
	"output":        argValue,
	"profile":       argValue,
	"region":        argValue,
	"saved":         argValue,
	"service":       argValue,
	"since":         argValue,
	"storage-class": argValue,
	"time-range":    argValue,
	"template":      argValue, // id or name
	"to":            argValue, // RFC3339 time
	"window":        argValue,

	"bucket":     argName, // S3 bucket name
	"download":   argName, // local path
	"exclude":    argName, // regex
	"groups":     argName, // ids or names
	"highlight":  argName, // regex
	"include":    argName, // regex
	"input":      argName, // local path
	"jq":         argName,
	"local":      argName, // local path
	"message":    argName,
	"prefix":     argName, // S3 key prefix
	"select":     argName, // expressions can hold literals
	"users":      argName, // ids or emails
	"widget-ids": argName, // ids or names

	"from-expr": argQuery,
	"where":     argQuery,

	"eq": argPairs, "ne": argPairs, "gt": argPairs, "ge": argPairs,
	"lt": argPairs, "le": argPairs, "match": argPairs, "nmatch": argPairs,
	"field": argPairs,
	"param": argPairs, // values are query expressions
	"tag":   argPairs,

	"base-url":   argURL,
	"ingest-url": argURL,

	"api-key": argSecret,
}

// argPlaceholders classifies positional arguments by the placeholder that
// names them in a command's Use line, e.g. <id> or [query].
var argPlaceholders = map[string]argClass{
	"METHOD":         argValue,
	"dashboard":      argValue,
	"dataset":        argValue,
	"definition":     argValue,
	"forward-config": argValue,
	"group":          argValue,
	"id":             argValue,
	"key":            argValue,
	"metric":         argValue,
	"name-filter":    argValue,
	"parser":         argValue,
	"profile":        argValue,
	"template":       argValue,
	"trace-id":       argValue,
	"widget":         argValue,

	"message":  argName,
	"question": argName,
	"user":     argName, // id or email
	"value":    argName,

	"query": argQuery,

	"path": argURL,
}

// Attribute keys for the root span. bronto.flags lists the flags that were
// set; bronto.flag.<name> and bronto.args hold values per argClass.
const (
	flagsKey      = attribute.Key("bronto.flags")
	flagKeyPrefix = "bronto.flag."
	argsKey       = attribute.Key("bronto.args")
)

var placeholderRE = regexp.MustCompile(`^[<\[]([A-Za-z][A-Za-z-]*)(?:\.\.\.)?[>\]]$`)

// usePlaceholders returns the positional placeholder names in c.Use,
// skipping flag placeholders such as "--widget-ids <id,...>".
func usePlaceholders(c *cobra.Command) []string {
	fields := strings.Fields(c.Use)
	var names []string
	for i := 1; i < len(fields); i++ {
		f := fields[i]
		if strings.HasPrefix(f, "--") || strings.HasPrefix(f, "[--") {
			i++ // the flag's own value placeholder
			continue
		}
		if m := placeholderRE.FindStringSubmatch(f); m != nil {
			names = append(names, m[1])
		}
	}
	return names
}

func flagClass(f *pflag.Flag) argClass {
	if safeFlagTypes[f.Value.Type()] {
		return argValue
	}
	if c, ok := flagClasses[f.Name]; ok {
		return c
	}
	return argName
}

func argClassAt(placeholders []string, i int) argClass {
	if len(placeholders) == 0 {
		return argName
	}
	if i >= len(placeholders) {
		i = len(placeholders) - 1 // extra args take the last one's class
	}
	if c, ok := argPlaceholders[placeholders[i]]; ok {
		return c
	}
	return argName
}

// recordValue renders v for class c at capture level lvl; ok is false when
// nothing should be recorded.
func recordValue(c argClass, v string, lvl telemetry.Capture) (string, bool) {
	switch {
	case c == argSecret:
		return "", false
	case lvl == telemetry.CaptureAll || c == argValue:
		return v, true
	case c == argQuery:
		if s, ok := query.Sanitize(v); ok {
			return s, true
		}
		return redacted, true
	case c == argPairs:
		if k, _, ok := strings.Cut(v, "="); ok {
			return k + "=?", true
		}
		return "?", true
	case c == argURL:
		if u, err := url.Parse(v); err == nil {
			return api.RedactURL(u), true
		}
		return redacted, true
	default: // argName
		return "", false
	}
}

// commandAttributes describes how c was invoked: argv is the full argument
// list (os.Args[1:]) that resolved to c.
func commandAttributes(root, c *cobra.Command, argv []string, lvl telemetry.Capture) []attribute.KeyValue {
	if lvl == telemetry.CaptureNone {
		return nil
	}
	_, rest, err := root.Find(argv)
	if err != nil {
		rest = nil
	}
	// Re-walk the flags with a callback instead of reading the parsed
	// values back: that keeps every occurrence of a repeated flag, works
	// for flag types whose String() is empty, and sets nothing.
	fs := pflag.NewFlagSet(c.Name(), pflag.ContinueOnError)
	fs.ParseErrorsAllowlist.UnknownFlags = true
	fs.Usage = func() {}
	fs.AddFlagSet(c.Flags())
	fs.AddFlagSet(c.InheritedFlags())
	values := map[string][]string{}
	set := map[string]bool{}
	_ = fs.ParseAll(rest, func(f *pflag.Flag, v string) error {
		set[f.Name] = true
		if s, ok := recordValue(flagClass(f), v, lvl); ok {
			values[f.Name] = append(values[f.Name], s)
		}
		return nil
	})

	var attrs []attribute.KeyValue
	if len(set) > 0 {
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		attrs = append(attrs, flagsKey.StringSlice(names))
	}
	for name, vs := range values {
		key := attribute.Key(flagKeyPrefix + name)
		if f := fs.Lookup(name); f != nil && isRepeatable(f) {
			attrs = append(attrs, key.StringSlice(vs))
		} else {
			attrs = append(attrs, key.String(vs[len(vs)-1])) // the last one wins, as in pflag
		}
	}

	placeholders := usePlaceholders(c)
	var args []string
	for i, a := range fs.Args() {
		if s, ok := recordValue(argClassAt(placeholders, i), a, lvl); ok {
			args = append(args, s)
		} else {
			args = append(args, redacted)
		}
	}
	if len(args) > 0 {
		attrs = append(attrs, argsKey.StringSlice(args))
	}
	if lvl == telemetry.CaptureAll {
		attrs = append(attrs, commandArgs(argv))
	}
	return attrs
}

// isRepeatable reports whether f collects every occurrence, so its
// attribute is a list even when it was given once.
func isRepeatable(f *pflag.Flag) bool {
	if _, ok := f.Value.(pflag.SliceValue); ok {
		return true
	}
	_, ok := f.Value.(*filterFlag)
	return ok
}

// commandArgs is process.command_args for capture=all: the executable and
// argv as given, with the values of argSecret flags replaced.
func commandArgs(argv []string) attribute.KeyValue {
	out := make([]string, 0, len(argv)+1)
	out = append(out, os.Args[0])
	redactNext := false
	for i, a := range argv {
		switch {
		case redactNext:
			out = append(out, redacted)
			redactNext = false
			continue
		case a == "--":
			out = append(out, argv[i:]...)
			return semconv.ProcessCommandArgs(out...)
		}
		if name, _, hasValue := strings.Cut(strings.TrimPrefix(a, "--"), "="); strings.HasPrefix(a, "--") && flagClasses[name] == argSecret {
			if hasValue {
				a = "--" + name + "=" + redacted
			} else {
				redactNext = true
			}
		}
		out = append(out, a)
	}
	return semconv.ProcessCommandArgs(out...)
}

// recordCommand adds the invocation's attributes to the root span in ctx.
// It does nothing when the span isn't recording (tracing off).
func recordCommand(ctx context.Context, root, c *cobra.Command, argv []string) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.SetAttributes(commandAttributes(root, c, argv, telemetry.CaptureLevel())...)
}

// recordPluginCommand records a plugin run. bronto doesn't know a plugin's
// flags, so only capture=all records anything: the raw argument list.
func recordPluginCommand(ctx context.Context, argv []string) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() || telemetry.CaptureLevel() != telemetry.CaptureAll {
		return
	}
	span.SetAttributes(commandArgs(argv))
}
