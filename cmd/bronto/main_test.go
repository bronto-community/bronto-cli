package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"bronto": main,
	})
}

// stubAPI serves canned, deterministic responses so txtar scripts can
// golden-snapshot the output formats (table/json/jsonl/csv) end to end
// through the real binary. Extend the canned set as more commands gain
// golden coverage.
func stubAPI() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/logs", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"logs":[` +
			`{"collection":"prod","dataset":"web","log":"web","log_id":"11111111-1111-1111-1111-111111111111","metadata":{"last_heartbeat_at":1700000000000}},` +
			`{"collection":"prod","dataset":"app","log":"app","log_id":"22222222-2222-2222-2222-222222222222"}]}`))
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"events":[` +
			`{"@time":"2026-01-02 03:04:05.000 UTC","@status":"info","@raw":"{\"level\":\"info\",\"msg\":\"a\"}","message_kvs":{"level":"info","status":200}},` +
			`{"@time":"2026-01-02 03:04:06.000 UTC","@status":"warn","@raw":"{\"level\":\"warn\",\"msg\":\"b\"}","message_kvs":{"level":"warn","status":500}}]}`))
	})
	return httptest.NewServer(mux)
}

func TestScripts(t *testing.T) {
	srv := stubAPI()
	t.Cleanup(srv.Close)
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
		Setup: func(env *testscript.Env) error {
			env.Setenv("STUB_URL", srv.URL)
			return nil
		},
	})
}
