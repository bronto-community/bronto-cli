package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

func execPing(t *testing.T, srvStatus int) (stdout string, err error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/logs" {
			t.Errorf("ping hit %s, want /logs", r.URL.Path)
		}
		w.WriteHeader(srvStatus)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"ping", "--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	err = root.Execute()
	return out.String(), err
}

func TestPingOK(t *testing.T) {
	out, err := execPing(t, 200)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Status  string `json:"status"`
		BaseURL string `json:"base_url"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output not JSON: %v (%q)", err, out)
	}
	if got.Status != "ok" || got.BaseURL == "" {
		t.Fatalf("got %+v", got)
	}
}

func TestPingForbiddenIsTypedAuthError(t *testing.T) {
	_, err := execPing(t, 403)
	if err == nil {
		t.Fatal("want error")
	}
	if clierr.ExitCode(err) != 3 {
		t.Fatalf("exit code = %d, want 3", clierr.ExitCode(err))
	}
}
