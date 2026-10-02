// Package mock is the deterministic fake Bronto API behind the docs:
// tested snippets (internal/tools/docsnippets) and VHS tapes (docs/tapes)
// run the real bronto binary against it. It serves "the docs mock world"
// described in docs/AGENTS.md from the fixtures in docs/testdata/mock/:
//
//	api/<path>.json            static GET responses, path mirrors the URL
//	                           (api/logs.json serves GET /logs,
//	                           api/monitors/<id>/events.json serves
//	                           GET /monitors/<id>/events)
//	events/<coll>/<name>.jsonl log events per dataset (/search, /context,
//	                           /top-keys are computed from these)
//	traces/spans.jsonl         spans in the .traces logset
//
// Everything is read-only: mutations answer plausibly (201 with the body
// echoed back, 204 for deletes) but change nothing, so every command sees
// the same world. The one piece of state is the tail cursor (see tail.go),
// which Reset clears.
//
// Any request the mock does not know answers 404 and logs a
// "docsmock: unhandled METHOD /path" line, so a docs example that needs a
// new route fails loudly instead of silently printing nothing.
package mock

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// APIKey is the only key the mock accepts (X-BRONTO-API-KEY). It is the
// fake key the docs use in every example.
const APIKey = "bronto_docs_example_key" // #nosec G101 -- the docs' published fake key, not a credential

// IngestPath is where the mock accepts `bronto send` batches. Point
// BRONTO_INGEST_URL at <mock URL> + IngestPath.
const IngestPath = "/ingest"

// Server is an http.Handler serving the docs mock world.
type Server struct {
	dir  string
	logf func(format string, args ...any)
	now  func() time.Time

	datasets []map[string]any            // rows of api/logs.json
	events   map[string][]map[string]any // log_id -> events, oldest first
	spans    []map[string]any            // .traces logset, oldest first
	tail     tailConfig

	mu        sync.Mutex
	tailPolls map[string]int
	requests  []string
}

// Option configures a Server.
type Option func(*Server)

// WithLogf routes the mock's log lines (unhandled routes, bad requests)
// somewhere other than the standard logger, e.g. t.Logf in tests.
func WithLogf(f func(format string, args ...any)) Option { return func(s *Server) { s.logf = f } }

// WithClock overrides the clock used for "${ago:...}" fixture values.
func WithClock(now func() time.Time) Option { return func(s *Server) { s.now = now } }

// New loads the fixtures in dir (normally docs/testdata/mock) and returns
// a ready Server.
func New(dir string, opts ...Option) (*Server, error) {
	s := &Server{
		dir:       dir,
		logf:      log.Printf,
		now:       time.Now,
		events:    map[string][]map[string]any{},
		tailPolls: map[string]int{},
		tail:      defaultTail,
	}
	for _, o := range opts {
		o(s)
	}
	var logs map[string]any
	if err := readJSON(filepath.Join(dir, "api", "logs.json"), &logs); err != nil {
		return nil, fmt.Errorf("docsmock: %w", err)
	}
	for _, r := range asSlice(logs["logs"]) {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		s.datasets = append(s.datasets, row)
		coll, _ := row["collection"].(string)
		name, _ := row["log"].(string)
		id, _ := row["log_id"].(string)
		evs, err := readJSONL(filepath.Join(dir, "events", coll, name+".jsonl"))
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("docsmock: %w", err)
		}
		s.events[id] = evs
	}
	spans, err := readJSONL(filepath.Join(dir, "traces", "spans.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("docsmock: %w", err)
	}
	s.spans = spans
	if b, err := os.ReadFile(filepath.Join(dir, "tail.json")); err == nil { // #nosec G304 -- fixture dir chosen by the caller
		if err := json.Unmarshal(b, &s.tail); err != nil {
			return nil, fmt.Errorf("docsmock: tail.json: %w", err)
		}
	}
	return s, nil
}

// Reset clears the mock's only state (tail cursors and the request log),
// so the next command starts from a known point. The snippet tester calls
// it before every command; tapes and other clients can POST /__mock/reset.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tailPolls = map[string]int{}
	s.requests = nil
}

// Requests returns "METHOD /path" for every API request since the last
// Reset (admin routes excluded).
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasPrefix(path, "/__mock/") {
		s.serveAdmin(w, r)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+path)
	s.mu.Unlock()

	if r.Header.Get("X-BRONTO-API-KEY") != APIKey {
		writeError(w, http.StatusUnauthorized, "Invalid API key")
		return
	}
	switch {
	case path == IngestPath || strings.HasPrefix(path, IngestPath+"/"):
		s.serveIngest(w, r)
	case path == "/search" && (r.Method == http.MethodPost || r.Method == http.MethodGet):
		s.serveSearch(w, r)
	case path == "/context" && r.Method == http.MethodGet:
		s.serveContext(w, r)
	case path == "/top-keys" && r.Method == http.MethodGet:
		s.serveTopKeys(w, r)
	case path == "/collections" && r.Method == http.MethodGet:
		s.serveCollections(w)
	default:
		s.serveResource(w, r)
	}
}

func (s *Server) serveAdmin(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/__mock/reset":
		s.Reset()
		w.WriteHeader(http.StatusNoContent)
	case "/__mock/requests":
		writeJSON(w, http.StatusOK, s.Requests())
	case "/__mock/health":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		s.unhandled(w, r)
	}
}

func (s *Server) unhandled(w http.ResponseWriter, r *http.Request) {
	s.logf("docsmock: unhandled %s %s (add a fixture under docs/testdata/mock/api/ or a handler in internal/tools/docsmock/mock)", r.Method, r.URL.Path)
	writeError(w, http.StatusNotFound, fmt.Sprintf("docsmock: no route for %s %s", r.Method, r.URL.Path))
}

// serveIngest accepts a `bronto send` batch: a JSON array (or NDJSON) of
// events, optionally gzip-encoded. Nothing is stored.
func (s *Server) serveIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.unhandled(w, r)
		return
	}
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid gzip body")
			return
		}
		defer func() { _ = zr.Close() }()
		body = zr
	}
	b, err := io.ReadAll(io.LimitReader(body, 64<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "unreadable body")
		return
	}
	n := 0
	var arr []any
	if json.Unmarshal(b, &arr) == nil {
		n = len(arr)
	} else {
		for _, line := range bytes.Split(b, []byte("\n")) {
			if len(bytes.TrimSpace(line)) > 0 {
				n++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": n})
}

// serveCollections derives GET /collections from the dataset list: one
// {collection: [{dataset, id}...]} map, the live shape.
func (s *Server) serveCollections(w http.ResponseWriter) {
	byColl := map[string]any{}
	for _, d := range s.datasets {
		coll, _ := d["collection"].(string)
		list, _ := byColl[coll].([]any)
		byColl[coll] = append(list, map[string]any{"dataset": d["log"], "id": d["log_id"]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": []any{byColl}})
}

// idKeys are the fields a resource row may carry its id under.
var idKeys = []string{"id", "log_id", "group_id", "role_id", "metric_id", "export_id"}

// serveResource answers everything that maps onto api/ fixtures:
//
//	GET    /a/b         -> api/a/b.json
//	GET    /a/<id>      -> the row of api/a.json whose id is <id>
//	POST   /a           -> 201, body echoed back with a fixed new id
//	PUT/PATCH /a/<id>   -> 200, the row merged with the body
//	DELETE /a/<id>      -> 204
//	POST/PUT/PATCH a sub-resource of a known row (/monitors/<id>/status,
//	/groups/<id>/members, ...) -> 200 {}
func (s *Server) serveResource(w http.ResponseWriter, r *http.Request) {
	clean := strings.Trim(r.URL.Path, "/")
	if clean == "" || strings.Contains(clean, "..") {
		s.unhandled(w, r)
		return
	}
	file := filepath.Join(s.dir, "api", filepath.FromSlash(clean)+".json")
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		if doc, err := s.loadFixture(file); err == nil {
			writeJSON(w, http.StatusOK, doc)
			return
		}
	}
	segs := strings.Split(clean, "/")
	// Collection-level create.
	if r.Method == http.MethodPost {
		if _, err := os.Stat(file); err == nil { // #nosec G703 -- ".." rejected above; stays under the fixture dir
			body := readBodyObject(r)
			body["id"] = "00000000-0000-4000-8000-000000000001"
			writeJSON(w, http.StatusCreated, body)
			return
		}
	}
	// Find the longest prefix /a[/b] whose fixture is a list containing segs[i] as an id.
	for i := len(segs) - 1; i >= 1; i-- {
		listFile := filepath.Join(s.dir, "api", filepath.FromSlash(strings.Join(segs[:i], "/"))+".json")
		doc, err := s.loadFixture(listFile)
		if err != nil {
			continue
		}
		row := findRow(doc, segs[i])
		if row == nil {
			continue
		}
		rest := segs[i+1:]
		switch {
		case len(rest) == 0 && r.Method == http.MethodGet:
			writeJSON(w, http.StatusOK, row)
		case len(rest) == 0 && (r.Method == http.MethodPut || r.Method == http.MethodPatch):
			for k, v := range readBodyObject(r) {
				row[k] = v
			}
			writeJSON(w, http.StatusOK, row)
		case len(rest) == 0 && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case len(rest) > 0 && r.Method == http.MethodGet:
			// A sub-resource without a fixture: an empty list, not a 404 —
			// e.g. /monitors/<id>/notifications.
			s.logf("docsmock: no fixture for GET /%s, answering an empty list", clean)
			writeJSON(w, http.StatusOK, map[string]any{rest[len(rest)-1]: []any{}})
		case len(rest) > 0 && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case len(rest) > 0:
			writeJSON(w, http.StatusOK, map[string]any{})
		default:
			s.unhandled(w, r)
		}
		return
	}
	s.unhandled(w, r)
}

// findRow returns the row (in any top-level array of doc) whose id field
// equals id.
func findRow(doc any, id string) map[string]any {
	var arrays [][]any
	switch t := doc.(type) {
	case []any:
		arrays = append(arrays, t)
	case map[string]any:
		for _, v := range t {
			if a, ok := v.([]any); ok {
				arrays = append(arrays, a)
			}
		}
	}
	for _, arr := range arrays {
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			for _, k := range idKeys {
				if v, _ := m[k].(string); v == id {
					return m
				}
			}
		}
	}
	return nil
}

// loadFixture reads a JSON fixture and resolves its relative-time
// placeholders against the mock's clock.
func (s *Server) loadFixture(path string) (any, error) {
	var doc any
	if err := readJSON(path, &doc); err != nil {
		return nil, err
	}
	return s.resolveAgo(doc), nil
}

// agoRe matches the relative-time placeholders fixtures may use where a
// table renders a value relative to now ("2m ago"): "${ago:2m}" becomes
// epoch milliseconds, "${ago_s:3h}" epoch seconds. Everything else in the
// fixtures is an absolute timestamp.
var agoRe = regexp.MustCompile(`^\$\{(ago|ago_s):([0-9a-z]+)\}$`)

func (s *Server) resolveAgo(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = s.resolveAgo(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = s.resolveAgo(val)
		}
		return t
	case string:
		m := agoRe.FindStringSubmatch(t)
		if m == nil {
			return t
		}
		d, err := parseAgo(m[2])
		if err != nil {
			return t
		}
		at := s.now().Add(-d)
		if m[1] == "ago_s" {
			return json.Number(fmt.Sprint(at.Unix()))
		}
		return json.Number(fmt.Sprint(at.UnixMilli()))
	}
	return v
}

// parseAgo parses "90s", "2m", "3h", "5d".
func parseAgo(v string) (time.Duration, error) {
	var n int
	var unit string
	if _, err := fmt.Sscanf(v, "%d%s", &n, &unit); err != nil {
		return 0, err
	}
	switch unit {
	case "s":
		return time.Duration(n) * time.Second, nil
	case "m":
		return time.Duration(n) * time.Minute, nil
	case "h":
		return time.Duration(n) * time.Hour, nil
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("unknown unit %q", unit)
}

func readBodyObject(r *http.Request) map[string]any {
	out := map[string]any{}
	b, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return out
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&out)
	return out
}

func readJSON(path string, out any) error {
	b, err := os.ReadFile(path) // #nosec G304 G703 -- fixture paths under the caller's fixture dir ("..": rejected by serveResource)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func readJSONL(path string) ([]map[string]any, error) {
	f, err := os.Open(path) // #nosec G304 -- fixture paths under the caller's fixture dir
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	line := 0
	for sc.Scan() {
		line++
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return eventMillis(out[i]) < eventMillis(out[j]) })
	return out, nil
}

func asSlice(v any) []any {
	a, _ := v.([]any)
	return a
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeError answers in the live error shape ({"details": ...}), which
// the CLI surfaces as "Bronto API returned <code>: <details>".
func writeError(w http.ResponseWriter, status int, details string) {
	writeJSON(w, status, map[string]any{"details": details})
}
