package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/bronto"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/output"
	"github.com/bronto-community/bronto-cli/internal/timerange"
)

// askPlan is the structured translation the LLM must return.
type askPlan struct {
	Dataset string   `json:"dataset"`
	Since   string   `json:"since"`
	Query   string   `json:"query"`
	Select  []string `json:"select,omitempty"`
	GroupBy []string `json:"group_by,omitempty"`
	Why     string   `json:"why"`
}

// command renders the equivalent bronto invocation — shown to the user
// before anything runs, per the --dry-run philosophy.
func (p askPlan) command() string {
	var sb strings.Builder
	sb.WriteString("bronto search ")
	if p.Query != "" {
		_, _ = fmt.Fprintf(&sb, "%q ", p.Query)
	}
	if p.Dataset != "" {
		sb.WriteString("-d " + p.Dataset + " ")
	}
	if p.Since != "" {
		sb.WriteString("--since " + p.Since + " ")
	}
	for _, s := range p.Select {
		_, _ = fmt.Fprintf(&sb, "--select %q ", s)
	}
	for _, g := range p.GroupBy {
		sb.WriteString("-g " + g + " ")
	}
	return strings.TrimSpace(sb.String())
}

func newAskCmd() *cobra.Command {
	var (
		dataset string
		yes     bool
	)
	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Translate a question into a query (LLM-assisted)",
		Long: "Translates a natural-language question into a bronto search using a configured\n" +
			"OpenAI-compatible endpoint (config: ask_url, ask_model; key: BRONTO_ASK_API_KEY).\n" +
			"The generated command and its reasoning are shown BEFORE anything runs; only the\n" +
			"question plus dataset and field NAMES (never event data) are sent to the endpoint.",
		Example: "  bronto ask \"5xx spikes in checkout since last night\"\n" +
			"  bronto ask \"errors by host this morning\" --yes\n" +
			"  bronto config set ask_url https://api.openai.com/v1/chat/completions",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			askVal, _ := app.Config.Get("ask_url")
			askURL := askVal.Val
			if askURL == "" {
				return clierr.New("config_ask_not_configured",
					"no LLM endpoint configured for 'bronto ask'").
					WithHint("Set an OpenAI-compatible chat-completions URL: 'bronto config set ask_url <url>' " +
						"(and ask_model; export BRONTO_ASK_API_KEY if the endpoint needs a key).")
			}
			modelVal, _ := app.Config.Get("ask_model")
			model := modelVal.Val
			if model == "" {
				model = "gpt-4o-mini"
			}

			grounding := askGrounding(cmd.Context(), app, dataset)
			plan, err := askLLM(cmd.Context(), app, askURL, model, args[0], grounding)
			if err != nil {
				return err
			}
			if dataset != "" {
				plan.Dataset = dataset // an explicit -d always wins over the model's pick
			}

			format, err := app.DetectFormat(false)
			if err != nil {
				return err
			}
			if format != output.FormatTable && !yes {
				// machine mode without --yes: the plan IS the output.
				p, err := app.PrinterFor(format)
				if err != nil {
					return err
				}
				return p.PrintJSON(map[string]any{
					"command": plan.command(), "dataset": plan.Dataset, "since": plan.Since,
					"query": plan.Query, "select": plan.Select, "group_by": plan.GroupBy, "why": plan.Why,
				})
			}

			_, _ = fmt.Fprintf(app.Stderr, "Generated command:\n\n  %s\n\nWhy: %s\n", plan.command(), plan.Why)
			if !yes {
				if !stdoutIsTTY() || !stdinIsTTY() {
					return nil // plan printed; nothing runs without --yes off a TTY
				}
				_, _ = fmt.Fprint(app.Stderr, "Run it? [Y/n]: ")
				line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				line = strings.TrimSpace(strings.ToLower(line))
				if line != "" && line != "y" && line != "yes" {
					_, _ = fmt.Fprintln(app.Stderr, "Aborted.")
					return nil
				}
			}
			return runAskPlan(cmd.Context(), app, plan)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&dataset, "dataset", "d", "", "target dataset (overrides the model's pick, grounds field discovery)")
	f.BoolVar(&yes, "yes", false, "run the generated query without confirmation")
	return cmd
}

// askGrounding collects dataset and field NAMES (never event contents)
// so the model can target real fields. Best-effort: an unreachable API
// only degrades the grounding, it does not block generation.
func askGrounding(ctx context.Context, app *App, dataset string) string {
	var sb strings.Builder
	if ds, err := listDatasets(ctx, app); err == nil {
		names := make([]string, 0, len(ds))
		for i, d := range ds {
			if i >= 50 {
				break
			}
			names = append(names, d.qualified())
		}
		sb.WriteString("Datasets: " + strings.Join(names, ", ") + "\n")
	}
	ref := dataset
	if ref == "" {
		dv, _ := app.Config.Get("default_dataset")
		ref = dv.Val
	}
	if ref == "" {
		return sb.String()
	}
	logID, err := resolveDatasetRef(ctx, app, ref)
	if err != nil {
		return sb.String()
	}
	var payload map[string]any
	client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
	params := url.Values{"time_range": {"Last 1 hour"}, "log_id": {logID}}
	if err := client.GetJSON(ctx, "/top-keys", params, &payload); err != nil {
		return sb.String()
	}
	rows := normalizeTopKeys(payload)
	keys := make([]string, 0, len(rows))
	for i, r := range rows {
		if i >= 40 {
			break
		}
		keys = append(keys, fmt.Sprint(r["key"]))
	}
	if len(keys) > 0 {
		sb.WriteString("Fields in " + ref + ": " + strings.Join(keys, ", ") + "\n")
	}
	return sb.String()
}

const askSystemPrompt = `You translate natural-language questions about logs into Bronto queries.
Respond with ONLY a JSON object (no markdown fences): {"dataset": "<name>", "since": "<15m|1h|18h|2d|...>", "query": "<WHERE expression>", "select": ["<aggregate>"...] (optional), "group_by": ["<field>"...] (optional), "why": "<one short sentence per mapping decision>"}.
The query language: comparisons = != > >= < <= ~ (regex) !~, combined with AND / OR / NOT and parentheses; strings in single quotes. Example: status >= 500 AND path ~ '/checkout'.
Prefer fields and datasets from the provided context; leave "query" empty to match everything. Keep "why" brief — it teaches the mapping.`

// askLLM calls the configured OpenAI-compatible chat-completions
// endpoint. Deliberately NOT app.HTTPClient: that client injects the
// Bronto API key into every request, which must never reach a
// third-party LLM endpoint.
func askLLM(ctx context.Context, app *App, url, model, question, grounding string) (askPlan, error) {
	body := map[string]any{
		"model":       model,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": askSystemPrompt + "\n\nContext:\n" + grounding},
			{"role": "user", "content": question},
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return askPlan{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return askPlan{}, clierr.New("config_ask_not_configured", fmt.Sprintf("invalid ask_url: %v", err))
	}
	req.Header.Set("Content-Type", "application/json")
	kv, _ := app.Config.Get("ask_api_key")
	if key := kv.Val; key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: app.HTTPClient.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return askPlan{}, clierr.New("ask_llm_error", fmt.Sprintf("LLM endpoint unreachable: %v", err)).
			WithRetryable()
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return askPlan{}, clierr.New("ask_llm_error", fmt.Sprintf("reading LLM response: %v", err))
	}
	if resp.StatusCode != http.StatusOK {
		return askPlan{}, clierr.New("ask_llm_error",
			fmt.Sprintf("LLM endpoint returned %d: %s", resp.StatusCode, truncateForErr(raw)))
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Choices) == 0 {
		return askPlan{}, clierr.New("ask_llm_error", "LLM response is not an OpenAI-compatible chat completion").
			WithHint("ask_url must point at a /chat/completions-style endpoint.")
	}
	content := strings.TrimSpace(envelope.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	var plan askPlan
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &plan); err != nil {
		return askPlan{}, clierr.New("ask_llm_error",
			fmt.Sprintf("LLM did not return the expected JSON plan: %v", err)).
			WithHint("Try again, or use a stronger ask_model.")
	}
	return plan, nil
}

func truncateForErr(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// runAskPlan executes the confirmed plan through the normal, validated
// search path — generation never bypasses it.
func runAskPlan(ctx context.Context, app *App, plan askPlan) error {
	var datasets []string
	if plan.Dataset != "" {
		datasets = []string{plan.Dataset}
	}
	ids, expr, err := resolveDataset(ctx, app, datasets, "")
	if err != nil {
		return err
	}
	spec, err := timerange.Resolve(plan.Since, "", "", nil)
	if err != nil || spec.TimeRange == "" {
		spec = timerange.Spec{TimeRange: "Last 15 minutes"}
	}
	effSelect := plan.Select
	if len(effSelect) == 0 && len(plan.GroupBy) == 0 {
		effSelect = []string{"@time", "@raw"}
	}
	req := bronto.SearchRequest{
		From: ids, FromExpr: expr, Time: spec, Where: plan.Query,
		Select: effSelect, Groups: plan.GroupBy, Limit: 100,
	}
	client := bronto.NewClient(app.HTTPClient, app.Config.BaseURL())
	resp, err := client.Search(ctx, req)
	if err != nil {
		return err
	}
	if len(resp.Groups) > 0 || len(plan.GroupBy) > 0 {
		p, err := app.Printer(false)
		if err != nil {
			return err
		}
		rows := resp.GroupRows()
		if len(rows) == 0 && len(resp.GroupsSeries) > 0 {
			rows = resp.GroupsSeries
		}
		return p.PrintRows(bronto.EventColumns(rows, 0), rows)
	}
	events := resp.EventRows()
	if len(plan.Select) > 0 {
		events = resp.SelectedRows()
	}
	return printEvents(app, events)
}
