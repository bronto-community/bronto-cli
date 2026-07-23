package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The API key is attached per-hop inside Transport.RoundTrip (a custom
// X-BRONTO-API-KEY header, not one of the sensitive headers net/http
// strips on cross-domain redirects), so without a CheckRedirect guard the
// key follows a redirect to ANY host. These tests pin that a cross-host
// redirect is refused and the key never reaches the second host, while a
// same-host redirect still works (2026-07-23 audit).

func TestCrossHostRedirectDoesNotLeakKey(t *testing.T) {
	var attackerGotKey string
	var attackerHit bool
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHit = true
		attackerGotKey = r.Header.Get("X-BRONTO-API-KEY")
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", attacker.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer origin.Close()

	c := NewHTTPClient("secret-key", "1.2.3")
	resp, err := c.Get(origin.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	// Either shape is acceptable (error, or a stopped redirect returning
	// the 3xx) — the security property is that the attacker host is never
	// contacted with the key.
	_ = err
	if attackerHit {
		t.Errorf("attacker host was contacted on a cross-host redirect (leaked key: %q)", attackerGotKey)
	}
	if attackerGotKey != "" {
		t.Errorf("API key leaked to a different host on redirect: %q", attackerGotKey)
	}
}

func TestSameHostRedirectStillFollowed(t *testing.T) {
	var served int
	var mux http.ServeMux
	mux.HandleFunc("/start", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/end")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) {
		served++
		if r.Header.Get("X-BRONTO-API-KEY") != "secret-key" {
			t.Errorf("same-host redirect target missing the key: %q", r.Header.Get("X-BRONTO-API-KEY"))
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(&mux)
	defer srv.Close()

	c := NewHTTPClient("secret-key", "1.2.3")
	resp, err := c.Get(srv.URL + "/start")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || served != 1 {
		t.Fatalf("same-host redirect not followed: status=%d served=%d", resp.StatusCode, served)
	}
}
