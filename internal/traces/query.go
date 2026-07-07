package traces

import (
	"fmt"
	"strings"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

const RootOnlyClause = "NOT EXISTS $span.parent_span_id"

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
// full SPAN_KIND_* wire form (extraction §5.3).
func KindClause(kind string) string {
	k := strings.ToUpper(strings.TrimSpace(kind))
	if !strings.HasPrefix(k, "SPAN_KIND_") {
		k = "SPAN_KIND_" + k
	}
	return fmt.Sprintf("$span.kind = '%s'", k)
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
