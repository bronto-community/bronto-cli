package api

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestURLTemplate(t *testing.T) {
	cases := []struct {
		path, want string
		ok         bool
	}{
		{"/logs", "/logs", true},
		{"/logs/", "/logs", true},
		{"/monitors/8f3a2c1e-0000-4000-8000-000000000000", "/monitors/{monitorId}", true},
		{"/monitors/8f3a/events", "/monitors/{monitorId}/events", true},
		{"/users/all", "/users/all", true}, // literal beats /users/{userId}
		{"/users/42", "/users/{userId}", true},
		{"/saved-searches/abc", "/saved-searches/{savedSearchId}", true}, // live but not in the spec
		{"/", "", false},
		{"/no-such-endpoint", "", false},
		{"/api/logs", "", false}, // base_url with a path prefix
		{"/monitors//events", "", false},
	}
	for _, c := range cases {
		got, ok := URLTemplate(c.path)
		if got != c.want || ok != c.ok {
			t.Errorf("URLTemplate(%q) = %q, %v; want %q, %v", c.path, got, ok, c.want, c.ok)
		}
	}
}

func TestSpanName(t *testing.T) {
	cases := []struct{ method, target, want string }{
		{"GET", "https://api.eu.bronto.io/monitors/8f3a?x=1", "GET /monitors/{monitorId}"},
		{"POST", "https://api.eu.bronto.io/search", "POST /search"},
		{"GET", "https://api.eu.bronto.io/whatever/123", "GET"}, // never the raw path
		{"POST", "https://ingestion.eu.bronto.io/", "POST"},
		{"FROB", "https://api.eu.bronto.io/logs", "HTTP /logs"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.target, nil)
		if got := SpanName(r); got != c.want {
			t.Errorf("SpanName(%s %s) = %q, want %q", c.method, c.target, got, c.want)
		}
	}
}

func TestRedactURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://api.eu.bronto.io/monitors/8f3a", "https://api.eu.bronto.io/monitors/8f3a"},
		{"https://api.eu.bronto.io/search?where=user%3D%22a%40b.c%22&limit=5", "https://api.eu.bronto.io/search?limit=REDACTED&where=REDACTED"},
		{"https://api.eu.bronto.io/x?a=1&a=2&flag", "https://api.eu.bronto.io/x?a=REDACTED&flag=REDACTED"},
		{"https://user:secret@api.eu.bronto.io/x?k=v#frag", "https://api.eu.bronto.io/x?k=REDACTED"},
		{"https://api.eu.bronto.io/x?%zz=1&ok=2", "https://api.eu.bronto.io/x?ok=REDACTED"}, // undecodable key dropped
	}
	for _, c := range cases {
		u, err := url.Parse(c.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := RedactURL(u); got != c.want {
			t.Errorf("RedactURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := RedactURL(nil); got != "" {
		t.Errorf("RedactURL(nil) = %q", got)
	}
}
