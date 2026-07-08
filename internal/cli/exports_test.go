package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

// withFastExportPoll shrinks exportPollInterval for the duration of a test
// so --wait tests don't sit on a real 3s clock.
func withFastExportPoll(t *testing.T) {
	t.Helper()
	old := exportPollInterval
	exportPollInterval = time.Millisecond
	t.Cleanup(func() { exportPollInterval = old })
}

func TestExportsCreateConvenienceFlagsBodyShape(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing content type")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"exp-1","status":"CREATED"}`))
	}, "", "exports", "create",
		"--dataset", "ds-1", "--where", "status=500", "--since", "1h", "-o", "json")
	if err != nil {
		t.Fatalf("err = %v, out = %q", err, out)
	}
	if gotMethod != http.MethodPost || gotPath != "/exports" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	details, ok := gotBody["search_details"].(map[string]any)
	if !ok {
		t.Fatalf("body missing search_details: %v", gotBody)
	}
	fromArr, ok := details["from"].([]any)
	if !ok || len(fromArr) != 1 || fromArr[0] != "ds-1" {
		t.Fatalf("search_details.from = %v", details["from"])
	}
	if details["time_range"] != "Last 1 hour" {
		t.Fatalf("search_details.time_range = %v", details["time_range"])
	}
	if details["where"] != "status=500" {
		t.Fatalf("search_details.where = %v", details["where"])
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc["id"] != "exp-1" {
		t.Fatalf("stdout = %q, err = %v", out, err)
	}
}

func TestExportsCreateBodyVsConvenienceConflict(t *testing.T) {
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be contacted")
	}, "", "exports", "create", "-f", "search_details.where=x", "--dataset", "ds-1")
	if err == nil {
		t.Fatal("expected a conflict error")
	}
	if clierr.ExitCode(err) != 2 {
		t.Fatalf("exit code = %d, want 2", clierr.ExitCode(err))
	}
}

func TestExportsCreateMissingBodyIsUsageError(t *testing.T) {
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be contacted")
	}, "", "exports", "create")
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage error exit 2, got %v", err)
	}
}

func TestExportsCreateWaitPollsUntilComplete(t *testing.T) {
	withFastExportPoll(t)
	var getCount int32
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/exports":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"exp-1","status":"IN_PROGRESS"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/exports/exp-1":
			n := atomic.AddInt32(&getCount, 1)
			if n == 1 {
				_, _ = w.Write([]byte(`{"id":"exp-1","status":"IN_PROGRESS"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"exp-1","status":"COMPLETE","location":"http://example.invalid/file"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}, "", "exports", "create", "--dataset", "ds-1", "--since", "1h", "--wait", "-o", "json")
	if err != nil {
		t.Fatalf("err = %v, out = %q", err, out)
	}
	if atomic.LoadInt32(&getCount) < 2 {
		t.Fatalf("expected at least 2 polls, got %d", getCount)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc["status"] != "COMPLETE" {
		t.Fatalf("stdout = %q, err = %v", out, err)
	}
}

func TestExportsCreateWaitFailedExitsOne(t *testing.T) {
	withFastExportPoll(t)
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/exports":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"exp-1","status":"IN_PROGRESS"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/exports/exp-1":
			_, _ = w.Write([]byte(`{"id":"exp-1","status":"FAILED","failure_detail":"boom"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}, "", "exports", "create", "--dataset", "ds-1", "--since", "1h", "--wait")
	if err == nil {
		t.Fatal("expected an error")
	}
	if clierr.ExitCode(err) != 1 {
		t.Fatalf("exit code = %d, want 1", clierr.ExitCode(err))
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "export_failed" {
		t.Fatalf("want export_failed, got %v", err)
	}
	if !strings.Contains(ce.Message, "boom") {
		t.Fatalf("message = %q, want it to include failure_detail", ce.Message)
	}
}

func TestExportsCreateDownloadWritesFileWithoutAuthHeaderOnLocation(t *testing.T) {
	withFastExportPoll(t)

	var locationAuthHeader string
	var locationHit bool
	locSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		locationHit = true
		locationAuthHeader = r.Header.Get("X-BRONTO-API-KEY")
		_, _ = w.Write([]byte("exported-bytes-payload"))
	}))
	defer locSrv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "export.out")

	_, stderr, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/exports":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"exp-1","status":"IN_PROGRESS"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/exports/exp-1":
			_, _ = w.Write([]byte(`{"id":"exp-1","status":"COMPLETE","location":"` + locSrv.URL + `"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}, "", "exports", "create", "--dataset", "ds-1", "--since", "1h", "--download", dest)
	if err != nil {
		t.Fatalf("err = %v, stderr = %q", err, stderr)
	}
	if !locationHit {
		t.Fatal("location server was never contacted")
	}
	if locationAuthHeader != "" {
		t.Fatalf("location request must not carry the API key header, got %q", locationAuthHeader)
	}
	data, rerr := os.ReadFile(dest)
	if rerr != nil {
		t.Fatalf("reading downloaded file: %v", rerr)
	}
	if string(data) != "exported-bytes-payload" {
		t.Fatalf("downloaded content = %q", string(data))
	}
	if !strings.Contains(stderr, dest) {
		t.Fatalf("stderr missing download progress note: %q", stderr)
	}
}

func TestExportsCreateDownloadImpliesWaitWithoutFlag(t *testing.T) {
	withFastExportPoll(t)
	locSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data"))
	}))
	defer locSrv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")

	var getHit bool
	_, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/exports":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"exp-1","status":"CREATED"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/exports/exp-1":
			getHit = true
			_, _ = w.Write([]byte(`{"id":"exp-1","status":"COMPLETE","location":"` + locSrv.URL + `"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}, "", "exports", "create", "--dataset", "ds-1", "--download", dest)
	if err != nil {
		t.Fatal(err)
	}
	if !getHit {
		t.Fatal("--download must imply --wait (GET /exports/{id} was never called)")
	}
}

func TestExportsListGetDeleteViaGenericFactory(t *testing.T) {
	out, _, err := runResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/exports" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"exports":[{"id":"exp-1","status":"COMPLETE"}]}`))
	}, "", "exports", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestExportsHasNoUpdateCommand(t *testing.T) {
	root := NewRootCmd()
	exportsCmd, _, err := root.Find([]string{"exports"})
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range exportsCmd.Commands() {
		if firstWord(sub.Use) == "update" {
			t.Fatal("exports must not have an update subcommand")
		}
	}
}
