package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
)

// userEdit sets one string value in the user config file. path is the
// TOML key path: {"default_profile"} or {"profiles", <name>, <key>}.
type userEdit struct {
	path  []string
	value string
}

// mutateUserFile loads the user config file, asks fn which values to set,
// and writes the file back. dir is the parent config dir (same semantics
// as LoadOptions.UserConfigDir). A missing file starts from an empty
// userFile; Profiles is always non-nil when fn runs.
//
// Only the edited lines change: comments, key order and keys this CLI
// doesn't know survive a `bronto config set`. When the file is too unusual
// for the line-level edit (inline tables, dotted keys, multi-line
// strings), it is re-encoded from its parsed contents instead, which keeps
// every key but drops comments.
func mutateUserFile(dir string, fn func(*userFile) []userEdit) error {
	path := filepath.Join(dir, "bronto", "config.toml")
	uf := userFile{Profiles: map[string]map[string]string{}}
	var src []byte
	if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- rewriting the user's own config file in place
		parsed, err := parseUserFile(b)
		if err != nil {
			return clierr.New("config_parse_error", "cannot parse "+path+": "+err.Error())
		}
		uf, src = *parsed, b
	}
	edits := fn(&uf)
	out, err := applyUserEdits(src, edits)
	if err != nil {
		return clierr.New("config_parse_error", "cannot update "+path+": "+err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600) // #nosec G703 -- the user's own config file under their config dir
}

// applyUserEdits returns src with edits applied. It tries a line-level
// edit first and keeps it only if it parses to exactly the expected
// document; otherwise it re-encodes the parsed document.
func applyUserEdits(src []byte, edits []userEdit) ([]byte, error) {
	want := map[string]any{}
	if err := toml.Unmarshal(src, &want); err != nil {
		return nil, err
	}
	for _, e := range edits {
		if err := setPath(want, e.path, e.value); err != nil {
			return nil, err
		}
	}
	text := string(src)
	for _, e := range edits {
		text = editTOMLText(text, e)
	}
	got := map[string]any{}
	if err := toml.Unmarshal([]byte(text), &got); err == nil && reflect.DeepEqual(got, want) {
		return []byte(text), nil
	}
	return toml.Marshal(want)
}

func setPath(doc map[string]any, path []string, value string) error {
	for _, k := range path[:len(path)-1] {
		next, ok := doc[k]
		if !ok {
			m := map[string]any{}
			doc[k] = m
			doc = m
			continue
		}
		m, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("%q is not a table", k)
		}
		doc = m
	}
	doc[path[len(path)-1]] = value
	return nil
}

// editTOMLText sets e in a TOML document by editing lines: it replaces the
// key's line inside the target table, or adds the key after the table's
// last key, or appends the table. The result is verified by the caller.
func editTOMLText(text string, e userEdit) string {
	table, key := e.path[:len(e.path)-1], e.path[len(e.path)-1]
	line := formatTOMLKey(key) + " = " + formatTOMLString(e.value) + "\n"
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	inTarget := len(table) == 0 // the root table starts at line 0
	found := inTarget
	insertAt := -1 // the new key line goes after this index
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			hdr, ok := parseTOMLHeader(t)
			inTarget = ok && slices.Equal(hdr, table)
			if inTarget {
				found, insertAt = true, i
			}
			continue
		}
		if !inTarget {
			continue
		}
		if k, ok := lineTOMLKey(t); ok {
			if k == key {
				indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
				lines[i] = indent + line
				return strings.Join(lines, "")
			}
			insertAt = i
		}
	}
	if found {
		if insertAt >= 0 && !strings.HasSuffix(lines[insertAt], "\n") {
			lines[insertAt] += "\n"
		}
		lines = slices.Insert(lines, insertAt+1, line)
		return strings.Join(lines, "")
	}
	var b strings.Builder
	b.WriteString(strings.Join(lines, ""))
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	parts := make([]string, len(table))
	for i, p := range table {
		parts[i] = formatTOMLKey(p)
	}
	b.WriteString("[" + strings.Join(parts, ".") + "]\n" + line)
	return b.String()
}

var bareTOMLKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func formatTOMLKey(k string) string {
	if bareTOMLKey.MatchString(k) {
		return k
	}
	return formatTOMLString(k)
}

// formatTOMLString renders s as a TOML basic string. JSON's string escapes
// are a subset of TOML's.
func formatTOMLString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// parseTOMLHeader splits a "[a.\"b\"]" table header into its keys. Array
// tables ("[[x]]") and anything it can't read report ok=false.
func parseTOMLHeader(t string) ([]string, bool) {
	if strings.HasPrefix(t, "[[") {
		return nil, false
	}
	var keys []string
	var cur strings.Builder
	quote := byte(0)
	for i := 1; i < len(t); i++ {
		c := t[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' && i+1 < len(t) {
				cur.WriteByte(c)
				i++
				cur.WriteByte(t[i])
				continue
			}
			if c == quote {
				quote = 0
			}
			cur.WriteByte(c)
		case c == '"' || c == '\'':
			quote = c
			cur.WriteByte(c)
		case c == '.' || c == ']':
			k, ok := unquoteTOMLKey(strings.TrimSpace(cur.String()))
			if !ok {
				return nil, false
			}
			keys = append(keys, k)
			cur.Reset()
			if c == ']' {
				return keys, true
			}
		default:
			cur.WriteByte(c)
		}
	}
	return nil, false
}

// lineTOMLKey returns the key of a "key = value" line (comments and blank
// lines report ok=false).
func lineTOMLKey(t string) (string, bool) {
	if t == "" || strings.HasPrefix(t, "#") {
		return "", false
	}
	k, _, ok := strings.Cut(t, "=")
	if !ok {
		return "", false
	}
	return unquoteTOMLKey(strings.TrimSpace(k))
}

func unquoteTOMLKey(k string) (string, bool) {
	switch {
	case k == "":
		return "", false
	case strings.HasPrefix(k, "'") && strings.HasSuffix(k, "'") && len(k) >= 2:
		return k[1 : len(k)-1], true
	case strings.HasPrefix(k, `"`):
		s, err := strconv.Unquote(k)
		return s, err == nil
	}
	return k, true
}

// SetUserValue writes key=value into the profile section of the user config.
// dir is the parent config dir (same semantics as LoadOptions.UserConfigDir).
// Unknown keys and invalid values are rejected before anything is written:
// a typo would otherwise be saved and silently ignored, and a bad timeout
// or region would make every later command, including the `bronto config
// set` that fixes it, fail at startup.
func SetUserValue(dir, profile, key, value string) error {
	if err := ValidateUserValue(key, value); err != nil {
		return err
	}
	if profile == "" {
		profile = "default"
	}
	return mutateUserFile(dir, func(uf *userFile) []userEdit {
		var edits []userEdit
		if uf.DefaultProfile == "" {
			edits = append(edits, userEdit{path: []string{"default_profile"}, value: profile})
		}
		return append(edits, userEdit{path: []string{"profiles", profile, key}, value: value})
	})
}

// SetDefaultProfile persists default_profile in the user config file.
func SetDefaultProfile(dir, name string) error {
	return mutateUserFile(dir, func(*userFile) []userEdit {
		return []userEdit{{path: []string{"default_profile"}, value: name}}
	})
}

// ValidateUserValue checks a `bronto config set` key and value: the key
// must be one the user config file is read for, and the value must pass
// the same check Load / NewApp apply when the value is used.
func ValidateUserValue(key, value string) error {
	switch key {
	case "api_key":
		return clierr.New("config_secret_rejected", "api_key cannot be stored in the config file").
			WithHint("Use the BRONTO_API_KEY environment variable or 'bronto auth login' (keychain).")
	case "ask_api_key":
		return clierr.New("config_secret_rejected", "ask_api_key cannot be stored in the config file").
			WithHint("Use the BRONTO_ASK_API_KEY environment variable.")
	}
	if !slices.Contains(userFileKeys, key) {
		return clierr.New("config_unknown_key", fmt.Sprintf("unknown config key %q", key)).
			WithHint("Settable keys: " + strings.Join(userFileKeys, ", ") + ".")
	}
	switch key {
	case "region":
		return validateRegion(value)
	case "timeout":
		_, err := ParseTimeout(Value{Val: value, Source: SourceUser})
		return err
	case "max_retries":
		_, err := ParseMaxRetries(Value{Val: value, Source: SourceUser})
		return err
	case "output":
		if _, err := output.ParseFormat(value); err != nil {
			return err
		}
	case "base_url", "app_url", "ingest_url", "ask_url":
		return validateURLSetting(key, value)
	}
	return nil
}

// ParseTimeout parses the timeout setting: a positive number of seconds.
func ParseTimeout(v Value) (int, error) {
	secs, err := strconv.Atoi(v.Val)
	if err != nil || secs <= 0 {
		return 0, clierr.New("config_invalid_timeout",
			fmt.Sprintf("timeout must be a positive integer (seconds), got %q%s", v.Val, fromSource(v.Source))).
			WithHint("Set it with 'bronto config set timeout 30', or BRONTO_TIMEOUT / --timeout for one run.")
	}
	return secs, nil
}

// ParseMaxRetries parses the max_retries setting: a non-negative integer.
func ParseMaxRetries(v Value) (int, error) {
	n, err := strconv.Atoi(v.Val)
	if err != nil || n < 0 {
		return 0, clierr.New("config_invalid_max_retries",
			fmt.Sprintf("max_retries must be a non-negative integer, got %q%s", v.Val, fromSource(v.Source))).
			WithHint("Set it with 'bronto config set max_retries 2', or BRONTO_MAX_RETRIES / --max-retries for one run.")
	}
	return n, nil
}

func fromSource(s Source) string {
	switch s {
	case SourceUser:
		return " (from the user config file)"
	case SourceProject:
		return " (from .bronto.toml)"
	case SourceEnv:
		return " (from the environment)"
	case SourceFlag:
		return " (from a flag)"
	}
	return ""
}
