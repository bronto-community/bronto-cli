package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// askLLMServer fakes an OpenAI-compatible endpoint returning the given plan.
func askLLMServer(t *testing.T, plan string, sawBrontoKey *atomic.Bool, sawBearer *atomic.Value) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-BRONTO-API-KEY") != "" && sawBrontoKey != nil {
			sawBrontoKey.Store(true)
		}
		if sawBearer != nil {
			sawBearer.Store(r.Header.Get("Authorization"))
		}
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": plan}}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func askAPIServer(t *testing.T, searchCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/logs":
			_, _ = w.Write([]byte(`{"logs":[{"log":"payments-api","collection":"prod","log_id":"11111111-1111-1111-1111-111111111111"}]}`))
		case "/top-keys":
			_, _ = w.Write([]byte(`{"11111111-1111-1111-1111-111111111111":{"status":{"rank":9,"type":"NUMBER","field_type":"JSON_KVP"}}}`))
		case "/search":
			if searchCalls != nil {
				searchCalls.Add(1)
			}
			_, _ = w.Write([]byte(`{"events":[{"@time":"t1","@raw":"hit"}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
}

const askPlanJSON = `{"dataset":"payments-api","since":"18h","query":"status >= 500","why":"5xx means status >= 500."}`

func TestAskRequiresConfiguredEndpoint(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"ask", "anything", "--api-key", "k"})
	err := root.Execute()
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "config_ask_not_configured" || clierr.ExitCode(err) != 2 {
		t.Fatalf("want config_ask_not_configured exit 2, got %v", err)
	}
}

func TestAskPipedPrintsPlanOnly(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	var searches atomic.Int32
	api := askAPIServer(t, &searches)
	defer api.Close()
	llm := askLLMServer(t, askPlanJSON, nil, nil)
	defer llm.Close()
	t.Setenv("BRONTO_ASK_URL", llm.URL)

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"ask", "5xx spikes since last night",
		"--base-url", api.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var plan map[string]any
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("plan not json: %q", out.String())
	}
	if plan["query"] != "status >= 500" || !strings.Contains(plan["command"].(string), "bronto search") {
		t.Fatalf("plan = %v", plan)
	}
	if searches.Load() != 0 {
		t.Fatal("plan-only mode must not execute the search")
	}
}

func TestAskYesExecutesThroughSearchPath(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	var searches atomic.Int32
	api := askAPIServer(t, &searches)
	defer api.Close()
	var sawBronto atomic.Bool
	var bearer atomic.Value
	llm := askLLMServer(t, "```json\n"+askPlanJSON+"\n```", &sawBronto, &bearer)
	defer llm.Close()
	t.Setenv("BRONTO_ASK_URL", llm.URL)
	t.Setenv("BRONTO_ASK_API_KEY", "llm-secret")

	root := NewRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"ask", "5xx spikes since last night", "--yes",
		"--base-url", api.URL, "--api-key", "bronto-secret"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if searches.Load() != 1 {
		t.Fatalf("search calls = %d, want 1", searches.Load())
	}
	if !strings.Contains(out.String(), "hit") {
		t.Fatalf("search result missing:\n%s", out.String())
	}
	if !strings.Contains(errb.String(), "Generated command:") || !strings.Contains(errb.String(), "status >= 500") {
		t.Fatalf("plan not shown before running:\n%s", errb.String())
	}
	// the security property: the Bronto key must never reach the LLM endpoint,
	// and the LLM key must (as a Bearer token).
	if sawBronto.Load() {
		t.Fatal("Bronto API key leaked to the LLM endpoint")
	}
	if bearer.Load() != "Bearer llm-secret" {
		t.Fatalf("LLM key not sent: %v", bearer.Load())
	}
}

func TestAskConfirmDecline(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	oldOut, oldIn := stdoutIsTTY, stdinIsTTY
	stdoutIsTTY = func() bool { return true }
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY, stdinIsTTY = oldOut, oldIn })
	var searches atomic.Int32
	api := askAPIServer(t, &searches)
	defer api.Close()
	llm := askLLMServer(t, askPlanJSON, nil, nil)
	defer llm.Close()
	t.Setenv("BRONTO_ASK_URL", llm.URL)

	root := NewRootCmd()
	var errb bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&errb)
	root.SetIn(strings.NewReader("n\n"))
	root.SetArgs([]string{"ask", "5xx spikes", "--base-url", api.URL, "--api-key", "k"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if searches.Load() != 0 {
		t.Fatal("declined plan must not run")
	}
	if !strings.Contains(errb.String(), "Aborted.") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

func TestAskBadLLMResponse(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	api := askAPIServer(t, nil)
	defer api.Close()
	llm := askLLMServer(t, "sorry, I cannot help with that", nil, nil)
	defer llm.Close()
	t.Setenv("BRONTO_ASK_URL", llm.URL)

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"ask", "q", "--yes", "--base-url", api.URL, "--api-key", "k"})
	err := root.Execute()
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "ask_llm_error" {
		t.Fatalf("want ask_llm_error, got %v", err)
	}
}

func TestAskAPIKeyRejectedInConfigFile(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"config", "set", "ask_api_key", "sekrit"})
	err := root.Execute()
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "config_secret_rejected" {
		t.Fatalf("want config_secret_rejected, got %v", err)
	}
}

func TestAskGroundingAndCommandRendering(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	api := askAPIServer(t, nil)
	defer api.Close()
	// capture the system prompt the LLM receives: grounding must carry
	// dataset and field names when -d is given.
	var sysPrompt atomic.Value
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role, Content string
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) > 0 {
			sysPrompt.Store(body.Messages[0].Content)
		}
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]any{
			"content": `{"dataset":"payments-api","since":"1h","query":"","select":["count(*)"],"group_by":["host"],"why":"aggregate"}`,
		}}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer llm.Close()
	t.Setenv("BRONTO_ASK_URL", llm.URL)

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"ask", "count by host", "-d", "payments-api",
		"--base-url", api.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	sp, _ := sysPrompt.Load().(string)
	if !strings.Contains(sp, "payments-api") || !strings.Contains(sp, "status") {
		t.Fatalf("grounding missing dataset/field names:\n%s", sp)
	}
	var plan map[string]any
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	cmdStr, _ := plan["command"].(string)
	for _, want := range []string{"-d payments-api", "--since 1h", `--select "count(*)"`, "-g host"} {
		if !strings.Contains(cmdStr, want) {
			t.Fatalf("command %q missing %q", cmdStr, want)
		}
	}
}

func TestAskYesRunsAggregatePlan(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/logs":
			_, _ = w.Write([]byte(`{"logs":[{"log":"payments-api","collection":"prod","log_id":"11111111-1111-1111-1111-111111111111"}]}`))
		case "/top-keys":
			_, _ = w.Write([]byte(`{}`))
		case "/search":
			_, _ = w.Write([]byte(`{"groups":[{"group":"[web-1]","value":7}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer api.Close()
	llm := askLLMServer(t, `{"dataset":"payments-api","since":"bogus-window","query":"","select":["count(*)"],"group_by":["host"],"why":"w"}`, nil, nil)
	defer llm.Close()
	t.Setenv("BRONTO_ASK_URL", llm.URL)

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"ask", "count by host", "--yes",
		"--base-url", api.URL, "--api-key", "k", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "web-1") {
		t.Fatalf("aggregate rows missing:\n%s", out.String())
	}
}

func TestAskLLMHTTPError(t *testing.T) {
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())
	api := askAPIServer(t, nil)
	defer api.Close()
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key ` + strings.Repeat("x", 300) + `"}}`))
	}))
	defer llm.Close()
	t.Setenv("BRONTO_ASK_URL", llm.URL)
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"ask", "q", "--yes", "--base-url", api.URL, "--api-key", "k"})
	err := root.Execute()
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "ask_llm_error" || !strings.Contains(ce.Message, "401") {
		t.Fatalf("want ask_llm_error with status, got %v", err)
	}
	if len(ce.Message) > 300 {
		t.Fatalf("error body not truncated: %d chars", len(ce.Message))
	}
}
