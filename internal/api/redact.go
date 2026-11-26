package api

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Redaction of secret-bearing JSON members, for the --debug trace
// (RedactBody, over raw and possibly cut-off text) and for --dry-run plans
// (RedactJSON, over a decoded document). Both decide by the decoded member
// name through memberMode, so they can't disagree about what a secret is.

// secretName matches a member name that holds a secret: one ending in
// api_key, secret, password, private_key, token or authorization, in any
// case and with or without the separator (client_secret, clientSecret,
// apiKey, x-api-key, accessToken). Plurals and lookalikes (api_keys,
// token_count, secretary) don't match.
var secretName = regexp.MustCompile(`(?i)(?:api[_-]?key|secret|password|private[_-]?key|token|authorization)$`)

// headersName is a webhook's custom_headers once case and separators are
// dropped (customHeaders, Custom-Headers). Any header can carry
// credentials, so every value in it is masked.
const headersName = "customheaders"

const redactedValue = "REDACTED"

type redactMode int

const (
	keepValue   redactMode = iota // not a secret: look inside it
	maskValue                     // a secret, whatever its JSON type
	maskMembers                   // custom_headers: mask every member's value
)

// memberMode says how to treat the value of the member called name.
// keyAPI also masks "key", which holds key material only on /api-keys;
// elsewhere it names a log field.
func memberMode(name string, keyAPI bool) redactMode {
	switch {
	case secretName.MatchString(name), keyAPI && name == "key":
		return maskValue
	case strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(name)) == headersName:
		return maskMembers
	}
	return keepValue
}

// RedactJSON masks the secrets in a decoded JSON document, in place, and
// returns it: the value of a secret-named member becomes "REDACTED"
// whatever its type (null stays), at any depth.
func RedactJSON(doc any, keyAPI bool) any {
	return redactDecoded(doc, keepValue, keyAPI)
}

func redactDecoded(v any, mode redactMode, keyAPI bool) any {
	switch t := v.(type) {
	case nil:
		return nil
	case map[string]any:
		if mode == maskValue {
			return redactedValue
		}
		for k, child := range t {
			childMode := maskValue
			if mode == keepValue {
				childMode = memberMode(k, keyAPI)
			}
			t[k] = redactDecoded(child, childMode, keyAPI)
		}
		return t
	case []any:
		if mode != keepValue {
			return redactedValue
		}
		for i, child := range t {
			t[i] = redactDecoded(child, keepValue, keyAPI)
		}
		return t
	}
	if mode != keepValue {
		return redactedValue
	}
	return v
}

// RedactBody masks the secrets in JSON text the way RedactJSON does, and
// copies everything else byte for byte. It doesn't need the text to be
// valid JSON: a body cut off by the debug cap, one document per line, or
// a malformed one are scanned as far as they go, and a secret cut off
// mid-value is masked up to the end of the text.
func RedactBody(body string, keyAPI bool) string {
	r := bodyRedactor{s: body, keyAPI: keyAPI}
	r.out.Grow(len(body))
	for r.i < len(r.s) {
		start := r.i
		r.value(keepValue)
		if r.i == start { // nothing a value starts with: copy it and go on
			r.copyByte()
		}
	}
	return r.out.String()
}

type bodyRedactor struct {
	s      string
	i      int
	out    strings.Builder
	keyAPI bool
}

func (r *bodyRedactor) copyByte() {
	r.out.WriteByte(r.s[r.i])
	r.i++
}

func (r *bodyRedactor) at(c byte) bool { return r.i < len(r.s) && r.s[r.i] == c }

func (r *bodyRedactor) space() {
	for r.i < len(r.s) && strings.IndexByte(" \t\r\n", r.s[r.i]) >= 0 {
		r.copyByte()
	}
}

// value writes the value at r.i, treated as mode says. A string followed
// by a colon is a member name wherever it stands, so a member is still
// recognized after malformed text has thrown off the nesting.
func (r *bodyRedactor) value(mode redactMode) {
	r.space()
	start := r.i
	switch {
	case r.i >= len(r.s):
		return
	case mode != maskValue && (r.at('{') || mode == keepValue && r.at('[')):
		r.copyByte()
		for {
			r.value(mode)
			r.space()
			if !r.at(',') {
				break
			}
			r.copyByte()
		}
		if r.at('}') || r.at(']') {
			r.copyByte()
		}
		return
	case mode != maskValue && r.at('"'):
		r.skipString()
		end := r.i
		for r.i < len(r.s) && strings.IndexByte(" \t\r\n", r.s[r.i]) >= 0 {
			r.i++
		}
		if r.at(':') {
			r.out.WriteString(r.s[start : r.i+1])
			r.i++
			var name string
			if json.Unmarshal([]byte(r.s[start:end]), &name) != nil {
				name = strings.Trim(r.s[start:end], `"`)
			}
			child := maskValue
			if mode == keepValue {
				child = memberMode(name, r.keyAPI)
			}
			r.value(child)
			return
		}
		r.i = start // a string value, handled below
	}
	r.skipValue()
	switch text := r.s[start:r.i]; {
	case mode == keepValue || text == "null":
		r.out.WriteString(text)
	case text != "":
		r.out.WriteString(`"` + redactedValue + `"`)
	}
}

// skipString moves past the string starting at r.i, or to the end of the
// text if it is cut off.
func (r *bodyRedactor) skipString() {
	for r.i++; r.i < len(r.s); r.i++ {
		switch r.s[r.i] {
		case '\\':
			r.i++
		case '"':
			r.i++
			return
		}
	}
	r.i = len(r.s)
}

// skipValue moves past one value of any type, nested ones included, or to
// the end of the text if it is cut off. It doesn't move if no value
// starts at r.i.
func (r *bodyRedactor) skipValue() {
	for depth := 0; r.i < len(r.s); {
		switch c := r.s[r.i]; c {
		case '"':
			r.skipString()
		case '{', '[':
			depth++
			r.i++
			continue
		case '}', ']':
			if depth == 0 {
				return
			}
			depth--
			r.i++
		case ',', ':', ' ', '\t', '\r', '\n':
			if depth == 0 {
				return
			}
			r.i++
			continue
		default: // a number, true, false, null, or text that is no JSON
			for r.i < len(r.s) && strings.IndexByte("\",:{}[] \t\r\n", r.s[r.i]) < 0 {
				r.i++
			}
		}
		if depth == 0 {
			return
		}
	}
}
