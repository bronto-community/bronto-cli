package patterns

import (
	"fmt"
	"strings"
	"testing"
)

func TestExtractClusters(t *testing.T) {
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, fmt.Sprintf("GET /api/v1/users/%d 200 %dms", i, i*3))
	}
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf("connection pool exhausted pool=worker-%d", i))
	}
	lines = append(lines, "totally unique line about nothing else at all")

	got := Extract(lines)
	if len(got) != 3 {
		t.Fatalf("clusters = %d: %+v", len(got), got)
	}
	if got[0].Count != 50 || !strings.Contains(got[0].Template, "<num>") {
		t.Fatalf("top cluster = %+v", got[0])
	}
	if !strings.Contains(got[0].Template, "GET") {
		t.Fatalf("stable tokens must survive: %+v", got[0])
	}
	if got[1].Count != 5 || !strings.HasPrefix(got[1].Template, "connection pool exhausted") {
		t.Fatalf("second cluster = %+v", got[1])
	}
	if got[2].Count != 1 || got[2].Example == "" {
		t.Fatalf("singleton = %+v", got[2])
	}
}

func TestTokenMasking(t *testing.T) {
	toks := tokenize("id=550e8400-e29b-41d4-a716-446655440000 n=42 h=deadbeefdeadbeef plain")
	joined := strings.Join(toks, " ")
	for _, want := range []string{"<uuid>", "<num>", "<hex>", "plain"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("masking missing %s: %q", want, joined)
		}
	}
}

func TestArityNeverMixes(t *testing.T) {
	got := Extract([]string{"a b c", "a b c", "a b c d"})
	if len(got) != 2 {
		t.Fatalf("different arities must not merge: %+v", got)
	}
}
