package cli

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

const (
	tOrg   = "aaaaaaaa-aaaa-aaaa-aaaa-00000000a001"
	tUser  = "aaaaaaaa-aaaa-aaaa-aaaa-00000000a002"
	tGroup = "aaaaaaaa-aaaa-aaaa-aaaa-00000000a003"
	tDash  = "aaaaaaaa-aaaa-aaaa-aaaa-00000000a004"
)

// cmdStub answers the lookups the org, access, tags and account commands
// make (active org, user by email, group and dashboard by name, roles)
// and records every other request as "METHOD path?query body". Reads get
// one payload with the live shapes (2026-11-26) of each list.
func cmdStub(got *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			switch r.URL.Path {
			case "/organizations":
				_, _ = w.Write([]byte(`{"organisations":[{"id":"other"},{"id":"` + tOrg + `","active":true}]}`))
				return
			case "/users":
				_, _ = w.Write([]byte(`{"users":[{"id":"` + tUser + `","email":"ada@example.com"}]}`))
				return
			case "/groups":
				_, _ = w.Write([]byte(`{"groups":[{"group_id":"` + tGroup + `","name":"sre"}]}`))
				return
			case "/dashboards":
				_, _ = w.Write([]byte(`{"dashboards":[{"id":"` + tDash + `","name":"Overview"}]}`))
				return
			case "/roles":
				_, _ = w.Write([]byte(`{"roles":[{"role_id":"Admin","display_name":"Admin"},{"role_id":"aaaaaaaa-aaaa-aaaa-aaaa-00000000a009","display_name":"Auditor"}]}`))
				return
			}
		}
		body, _ := io.ReadAll(r.Body)
		req := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			req += "?" + r.URL.RawQuery
		}
		if len(body) > 0 {
			req += " " + string(body)
		}
		*got = append(*got, req)
		if r.Method == http.MethodDelete || strings.Contains(r.URL.Path, "/access/") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Tags carry "value", not the spec's "values", and each type lists
		// its entities; access members are a top-level array.
		_, _ = w.Write([]byte(`{"ok":true,` +
			`"tags":[{"name":"team","value":[{"value":"sre","types":[{"type":"DASHBOARD","entities":[{"id":"a"},{"id":"b"}]},{"type":"LOG","entities":[{"id":"c"}]}]}]}],` +
			`"members":[{"type":"user","id":"u1","status":"active"}],` +
			`"organisations":[{"id":"o1","name":"Acme","active":true}]}`))
	}
}

// cmdRequest is a command line and the one request it must send, with
// names resolved to ids.
type cmdRequest struct {
	args []string
	want string
}

func checkCmdRequests(t *testing.T, cases []cmdRequest) {
	t.Helper()
	for _, c := range cases {
		var got []string
		if _, _, err := runResource(t, cmdStub(&got), "", c.args...); err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%v requested\n  %v, want\n  [%s]", c.args, got, c.want)
		}
	}
}

// checkCmdUsageErrors: each command line fails with exit 2 before any
// request beyond name lookups.
func checkCmdUsageErrors(t *testing.T, cases [][]string) {
	t.Helper()
	for _, args := range cases {
		var got []string
		_, _, err := runResource(t, cmdStub(&got), "", args...)
		var ce *clierr.Error
		if !errors.As(err, &ce) || clierr.ExitCode(err) != 2 {
			t.Errorf("%v: err = %v, want a usage error (exit 2)", args, err)
		}
		if len(got) != 0 {
			t.Errorf("%v made requests %v", args, got)
		}
	}
}
