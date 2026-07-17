package traces

import (
	"fmt"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

const RootOnlyClause = "NOT EXISTS $span.parent_span_id"

// ErrorsClause is the WHERE fragment matching error-status spans.
const ErrorsClause = "$span.status_code = 'STATUS_CODE_ERROR'"

// validKinds are the OpenTelemetry span kinds accepted by KindClause,
// bare (server) or SPAN_KIND_-prefixed (SPAN_KIND_SERVER), case-insensitive.
var validKinds = map[string]bool{
	"SERVER": true, "CLIENT": true, "INTERNAL": true, "PRODUCER": true, "CONSUMER": true,
}

// NormalizeAttr turns a user-supplied attribute into query form:
// leading $ added unless present. http.route -> $http.route.
func NormalizeAttr(attr string) (string, error) {
	a := strings.TrimSpace(attr)
	if a == "" {
		return "", clierr.New("usage_invalid_attr", "attribute name is empty")
	}
	if !strings.HasPrefix(a, "$") {
		a = "$" + a
	}
	return a, nil
}

// KindClause builds the span-kind filter. Accepts bare (server) or
// prefixed (SPAN_KIND_SERVER) forms; where-clauses always use the
// full SPAN_KIND_* wire form (extraction §5.3). Unknown kinds error
// rather than silently building a clause that can never match.
func KindClause(kind string) (string, error) {
	k := strings.ToUpper(strings.TrimSpace(kind))
	bare := strings.TrimPrefix(k, "SPAN_KIND_")
	if !validKinds[bare] {
		return "", clierr.New("usage_invalid_kind",
			fmt.Sprintf("unknown span kind %q", kind)).
			WithHint("Valid kinds: server, client, internal, producer, consumer.")
	}
	return fmt.Sprintf("$span.kind = 'SPAN_KIND_%s'", bare), nil
}

func AndJoin(clauses ...string) string {
	var parts []string
	for _, c := range clauses {
		if c != "" {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, " AND ")
}

// Quote single-quotes a literal for the query language, doubling
// embedded single quotes (SQL-style escaping) so values like O'Brien
// are quoted safely.
func Quote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}
