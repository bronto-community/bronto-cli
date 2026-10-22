# Agent eval: building dashboards

Checks whether a coding agent can build a working dashboard with the bronto CLI using only what a user has. Background and earlier results: [#195](https://github.com/bronto-community/bronto-cli/issues/195).

Re-run it after changes to `dashboards apply`, the metric definition commands, the dashboards guide, or `skill.md`.

## Setup

1. A local test environment (CONTRIBUTING.md, "Local test environment") pointing at the CI org.
2. Traffic: `make demo-traces ARGS="-burst 300 -for 2h -rate 60"`. Start it at least 10 minutes before the agents, so every service has data.
3. A binary built from the commit under test, e.g. `go build -o <scratch>/eval/bronto ./cmd/bronto`.
4. One empty working directory per run: `<scratch>/eval/work-<run>`. Agents running in parallel must not share one.

Run each task twice (runs `1a`, `1b`, `2a`, …), each in its own agent. Name objects `<prefix>-<run>`, with a prefix that's new for this round (the second round used `eval2`).

## Tasks

1. **Checkout API health**, on `.traces/checkout-api`: request rate over time (line), p95 latency of server spans over time (line), error count over time (bar), top 10 operations by request count (top-list).
2. **Service overview**, across `.traces/storefront-web`, `.traces/catalog-api`, `.traces/checkout-api` and `.traces/payments-gateway`: requests per service over time, one line per service (line); total error spans in the range as one number (score); error rate in percent per service over time, as errors / total * 100 (line).
3. **Payments gateway**, on `.traces/payments-gateway`: p95 latency per operation, one line per operation (line); error spans by status code or error type (bar); the 10 slowest operations by p95 latency (top-list).

## Prompt

Replace `<TASK>`, `<BIN>`, `<WORKDIR>` and `<PREFIX>`. Write the task as its own sentence, starting with `Build a dashboard named "<PREFIX> <title>"` and then the dataset(s) and widget list from above.

```text
You are an evaluation subject. We want to learn how well an agent can build a Bronto dashboard with the bronto CLI as it is today. Behave like a capable user who just installed the CLI: do the task as well as you can, then report honestly what happened, including every dead end.

## Task
<TASK>
Each widget must show real data for the last hour (the data is synthetic traffic that started recently).

## Tools and allowed sources
- CLI binary: `<BIN>`. Run every command from the repository root as `scripts/test-env <BIN> <args>`; that points it at the test org. `bronto api <method> <path>` is allowed.
- Your own working directory for files you write: `<WORKDIR>`. Write nothing anywhere else; other agents run in parallel next to you.
- You may read ONLY what a real user has: `bronto --help` and `<command> --help`, `skill.md`, `llms.txt`, the docs under `docs/cli/`, and the public API reference `api/openapi.yaml`. Do NOT read the Go source code (`internal/`, `cmd/`, `integration/`), GitHub issues or PRs, or anything else.
- Querying data (`search`, `fields`, `traces`, etc.) to discover field names is fine.

## Hard safety rules
- This is a shared test org. Name every object you create with the prefix `<PREFIX>`. Never update or delete anything you didn't create in this session.
- Never run `bronto auth login/logout/switch`, never touch the keychain, never set a fake HOME, never create/update/delete API keys. Don't use a browser.

## Report (your final message)
1. Dashboard name and id, and for each widget: name, type, whether it shows data, and how you verified that.
2. The ordered list of every command you ran, each with a one-line outcome (ok / error + short message).
3. Dead ends, guesses, and things you had to discover by trial and error.
4. What information or command was missing that would have made this easy.
5. Your confidence (1–5) that the dashboard renders correctly in the web UI, and why.
Keep the report factual; don't pad it.
```

## Scoring

A run passes when:

- the dashboard exists with every widget attached,
- every widget returns data (`widgets query`),
- the agent needed no guessed URLs, no raw `bronto api` and no hand-wired IDs,
- a second `dashboards apply --dry-run` plans zero writes.

Also record tool calls and wall time per run, and the gaps each agent reports. Then check the dashboards in the test browser (`make browser`): agents can't see the rendering.

## Results

| Round | Date | CLI | Passed | Tool calls | Time |
| --- | --- | --- | --- | --- | --- |
| 1 | 2026-10-21 | before the redesign | 0 of 6 by these criteria (every dashboard worked, but only by guessing endpoints and wiring IDs) | 28–60 | 3.4–7.1 min |
| 2 | 2026-10-21 | with `dashboards apply` (#209) | 6 of 6; two runs first hit #212 | 14–21 | 1.9–3.3 min |
