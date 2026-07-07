package bronto

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

func TestSearchPostsBodyAndParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/search" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil || body["where"] != "x" {
			t.Errorf("body = %s", b)
		}
		_, _ = w.Write([]byte(`{"events":[{"@raw":"hello","@time":"t1"}],"explain":{"Execution time (millis)":"12"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.Client(), srv.URL)
	resp, err := c.Search(context.Background(), SearchRequest{Where: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Events) != 1 || resp.Events[0]["@raw"] != "hello" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestSearchMapsAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"message":"nope"}`))
	}))
	defer srv.Close()
	_, err := NewClient(srv.Client(), srv.URL).Search(context.Background(), SearchRequest{})
	if clierr.ExitCode(err) != 3 {
		t.Fatalf("exit = %d, want 3", clierr.ExitCode(err))
	}
}

func TestGetJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/top-keys" || r.URL.Query().Get("limit") != "5" {
			t.Errorf("got %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"top_keys":[{"key":"a"}]}`))
	}))
	defer srv.Close()
	var out map[string]any
	err := NewClient(srv.Client(), srv.URL).GetJSON(context.Background(), "/top-keys",
		url.Values{"limit": []string{"5"}}, &out)
	if err != nil || out["top_keys"] == nil {
		t.Fatalf("out=%v err=%v", out, err)
	}
}
