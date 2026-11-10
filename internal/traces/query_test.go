package traces

import "testing"

func TestNormalizeAttr(t *testing.T) {
	if got, _ := NormalizeAttr(" http.route "); got != "Span:http.route" {
		t.Fatalf("got %q", got)
	}
	if got, _ := NormalizeAttr("Span:kind"); got != "Span:kind" {
		t.Fatalf("got %q", got)
	}
	for _, in := range []string{"span.kind", "$span.kind"} {
		if got, _ := NormalizeAttr(in); got != "Span:kind" {
			t.Fatalf("NormalizeAttr(%q) = %q, want Span:kind", in, got)
		}
	}
	for _, in := range []string{"service.name", "$service.name", "Resource:service.name"} {
		if got, _ := NormalizeAttr(in); got != "Resource:service.name" {
			t.Fatalf("NormalizeAttr(%q) = %q, want Resource:service.name", in, got)
		}
	}
	if got, _ := NormalizeAttr("service.namespace"); got != "Resource:service.namespace" {
		t.Fatalf("got %q", got)
	}
	if got, _ := NormalizeAttr("$http.route"); got != "Span:http.route" {
		t.Fatalf("got %q", got)
	}
	if got, _ := NormalizeAttr("http.response.status_code"); got != "Span:http.response.status_code" {
		t.Fatalf("got %q", got)
	}
	if got, _ := NormalizeAttr("db.system"); got != "Span:db.system" {
		t.Fatalf("got %q", got)
	}
	if got, _ := NormalizeAttr("peer.service"); got != "Span:peer.service" {
		t.Fatalf("got %q", got)
	}
	for _, in := range []string{"cart.items", "$cart.items"} {
		if got, _ := NormalizeAttr(in); got != "Span:cart.items" {
			t.Fatalf("NormalizeAttr(%q) = %q, want Span:cart.items", in, got)
		}
	}
	if _, err := NormalizeAttr("  "); err == nil {
		t.Fatal("empty attr must error")
	}
}

func TestKindClause(t *testing.T) {
	if got, err := KindClause("server"); err != nil || got != "Span:kind = 'SPAN_KIND_SERVER'" {
		t.Fatalf("got %q, err %v", got, err)
	}
	if got, err := KindClause("SPAN_KIND_CLIENT"); err != nil || got != "Span:kind = 'SPAN_KIND_CLIENT'" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestKindClauseValidation(t *testing.T) {
	for _, ok := range []string{"server", "SPAN_KIND_CLIENT", "Internal", "producer", "CONSUMER"} {
		if _, err := KindClause(ok); err != nil {
			t.Errorf("KindClause(%q) errored: %v", ok, err)
		}
	}
	if _, err := KindClause("sideways"); err == nil {
		t.Error("unknown kind must error")
	}
	got, _ := KindClause("server")
	if got != "Span:kind = 'SPAN_KIND_SERVER'" {
		t.Fatalf("clause = %q", got)
	}
}

func TestAndJoinSkipsEmpty(t *testing.T) {
	if got := AndJoin("", "a = 1", "", "b = 2"); got != "a = 1 AND b = 2" {
		t.Fatalf("got %q", got)
	}
	if got := AndJoin("", ""); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestQuoteEscapesSingleQuotes(t *testing.T) {
	if got := Quote("O'Brien"); got != "'O''Brien'" {
		t.Fatalf("got %q", got)
	}
}

func TestMinDurationClause(t *testing.T) {
	if got := MinDurationClause(0); got != "" {
		t.Fatalf("0 ms must add no clause, got %q", got)
	}
	if got := MinDurationClause(-1); got != "" {
		t.Fatalf("negative ms must add no clause, got %q", got)
	}
	// "at least" in the flag help: the boundary value itself matches.
	if got := MinDurationClause(1.5); got != "Span:duration_nano >= 1500000" {
		t.Fatalf("clause = %q", got)
	}
}
