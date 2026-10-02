# bronto-cli v2 — Design

**Date:** 2026-07-06
**Status:** Approved design, pre-implementation
**Supersedes:** [bronto-community/bronto-cli](https://github.com/bronto-community/bronto-cli) (Python/Typer, v0.1.0)

## 1. Goals & positioning

A ground-up rebuild of `bronto`, the CLI for the [Bronto](https://docs.bronto.io) observability platform.

- **Ambition:** serious community OSS tool — well-engineered, contributor-friendly, pragmatic scope. Not claiming official status.
- **First-class usage contexts (all four):**
  1. Interactive terminal use (rich tables, colors, waterfalls, completions)
  2. Scripting / CI / pipes (stable JSON contracts, exit codes, no prompts, minimal containers)
  3. AI agents driving the CLI (machine-readable output/help, typed errors, token-frugal defaults)
  4. Ingestion — *sending* data to Bronto, not just reading (new vs. v1)
- **API coverage: hybrid.** Hand-crafted UX for high-value workflows (search, tail, traces, send, auth); broad generated coverage for CRUD resources; a raw-API escape hatch for everything else.
- **Compatibility:** clean slate. v1's command grammar informs but does not constrain; no drop-in compatibility requirement.

### Non-goals

- No MCP server of our own (Bronto ships one; the CLI + a short skill file is the agent surface).
- No telemetry (not even opt-out; nothing without explicit opt-in).
- No TUI-only functionality — every interactive nicety has a scriptable equivalent.
- No runtime-loaded plugins/RPC plugin framework — exec-based plugins only (YAGNI).

## 2. Language & core stack: Go

Research verdict (three parallel studies: CLI best practices, language comparison, OpenAPI tooling — sources in §10):

- **Go** is the canonical fit for every hard requirement: `CGO_ENABLED=0` static binary → `FROM scratch`/distroless/air-gapped; ~0 ms startup; trivial cross-compilation; goreleaser distribution; kubectl/gh-style exec plugins with production reference code; SRE-skewed contributor pool already Go-literate (days-to-weeks onboarding vs. months for Rust); mature OpenAPI codegen (`oapi-codegen`).
- Rust (progenitor/clap, oxide.rs pattern) matches Go on binaries/startup with a higher correctness ceiling, but costs months of contributor ramp-up and buys nothing decisive for an I/O-bound HTTP/JSON tool. Rejected.
- Python (status quo) fails the restricted-environment and startup requirements — the reasons for this rebuild. TypeScript/oclif ships 60–100 MB runtime-embedded binaries. Both rejected.

**Stack:** Cobra (commands), oapi-codegen (generated client/types), charmbracelet lipgloss + a table renderer (interactive output), gojq (embedded `--jq`), a Go keyring library (credentials), goreleaser + nfpm (distribution), testscript + Prism (testing).

## 3. Command surface & grammar

Noun-verb grammar (`bronto <resource> <verb>`) for resources; a small set of top-level **workflow verbs** for daily-driver operations (the Stripe `listen`/`trigger` lesson: a few high-leverage live commands matter more than exhaustive CRUD).

### Workflow verbs (hand-written, the product)

| Command | Behavior |
|---|---|
| `bronto search "<query>"` | Query as positional arg or stdin. `--since 15m`, `--from`/`--to` (RFC3339), `--dataset`, `--select`, `--group-by`, `--limit`. Streams results as they arrive; never buffers a whole result set. |
| `bronto tail` | stern-grade live tail: `--include`/`--exclude`/`--highlight` client-side regex, per-dataset color coding, `--no-follow` (catch-up-then-exit), reorder buffer for out-of-order events, JSONL when piped. |
| `bronto traces services\|operations\|list\|show\|shape` | v1's trace explorer carried forward: aggregations, span search, ASCII waterfall (`show <trace-id>`), aggregated "shape" waterfall. |
| `bronto send` | Ingestion from stdin (`tail -f app.log \| bronto send --dataset myapp`) or one-shot `--message`. NDJSON to the ingestion host; `--collection`, `--tags`. Completes the test-your-pipeline loop. |
| `bronto login` / `bronto auth …` | First-run: paste key (or `--key-stdin`) → validated against API → region detected → stored in OS keychain under a named profile. See §6. |
| `bronto fields` | Field discovery on a dataset (v1 `top-keys`). |
| `bronto context` | Events around a specific log event (v1 `context`). |

### Resource nouns (thin, mostly generated)

`datasets`, `monitors`, `dashboards`, `parsers`, `api-keys`, `exports`, `tags`, `saved-searches`, `usage` — each with `list|get|create|update|delete` as the spec allows. Naming note: **`api-keys`** (not v1's `keys`) frees the mental namespace and avoids collision with field-key discovery. `exports create --wait` keeps its workflow sugar (create → poll → download).

### Escape hatch

`bronto api <method> <path> [--field k=v] [--input body.json]` — gh-style raw access with auth, region resolution, and pagination handled. Covers the entire spec from day one, so generated coverage grows deliberately, not urgently.

### Plumbing

`bronto config get|set|list`, `bronto completion bash|zsh|fish`, `bronto version`, `bronto ping`.

## 4. Architecture & repository layout

Three layers, strict downward dependencies (the Stripe/doctl pattern: **generate the SDK core, hand-craft the command layer**):

```
cmd/bronto/            entry point (thin)
internal/cli/          command layer: Cobra commands, flags, help, prompts
  search/ tail/ traces/ send/ auth/ ...    hand-written workflow commands
  resources/           thin GENERATED CRUD commands (Stripe-style)
internal/bronto/       service layer: cross-endpoint workflows
                       (trace reconstruction, tail poll+dedup+reorder,
                        export create+poll+download, query building)
internal/api/          GENERATED oapi-codegen client + types
                       + hand-written transport wrapper (auth header, retries,
                         rate-limit backoff, User-Agent, region resolution)
internal/output/       single output engine used by every command (§5)
internal/config/       config, contexts/profiles, keychain
api/openapi.yaml       vendored Bronto OpenAPI 3.0.2 spec — source of truth
```

Decisions:

- **Generated code is checked in** (Oxide's model): spec updates arrive as reviewable PRs; CI fails if regeneration is dirty; contributors build with plain `go build`.
- **The generated client is an implementation detail.** Commands with any logic go through the service layer, never `internal/api` directly. This seam allows swapping generators without touching UX.
- **Generated CRUD commands are dumb by contract** (Stripe: they "hold no functionality on their own"): flag parsing → client call → output engine. Anything smarter is promoted to a hand-written command.
- **Spec sync loop:** scheduled CI job fetches Bronto's latest spec → regenerates → `oasdiff` flags breaking changes → opens a PR with a reviewable diff. Spectral lints the vendored spec.
- **Plugins:** exec discovery — any `bronto-foo` executable on PATH becomes `bronto foo` (kubectl/gh pattern). Language-agnostic for plugin authors; no RPC machinery.

## 5. Output system, errors & agent-friendliness

One output engine for every command (the gcloud lesson: a uniform format system beats per-command formatting).

**Formats:** `-o/--output table|json|jsonl|raw|csv`.
- Default: `table` on a TTY; `jsonl` when piped for streaming commands (search/tail); `json` when piped elsewhere.
- `--json <fields>` selects fields; with no argument it lists available fields (gh's self-documenting schema trick).
- Built-in `--jq <expr>` via embedded gojq — no jq binary needed in scratch containers.

**TTY discipline:**
- stdout carries data only; progress, stats, warnings go to stderr; `--quiet` silences stderr chatter.
- Piped: no color, no truncation, exact timestamps, no spinners. Interactive: rich tables, color, spinners.
- `NO_COLOR`, `FORCE_COLOR`, `TERM=dumb`, `--no-color` respected.
- Never prompt when stdin is not a TTY — fail with instructions. Every prompt has a flag equivalent (`--yes`).

**Errors — typed, three audiences.** Every error carries a stable machine code (`auth_invalid_key`, `dataset_not_found`, `rate_limited`, `query_syntax_error`, …) and a `retryable` flag.
- Humans: message + fix suggestion + docs URL. (v1's README tip "403 = you used an ingestion key" becomes the actual error message, informed by `auth status` role detection.)
- Machines: `{"error": {"code": "...", "message": "...", "retryable": bool}}` on stderr.
- Exit codes: `0` success · `1` API error · `2` usage/config · `3` auth · `4` not found · `5` rate-limit/timeout.

**Agent-friendliness (a design axis):**
- Stable JSON contracts — additive-only changes; breaking a machine-output field is a breaking release.
- Typed error codes + distinct exit codes distinguish retryable from permanent failures, so agents self-correct deterministically.
- JSONL streams for incremental parsing; token-frugal defaults (modest `--limit`, field selection).
- `--help` is the agent discovery surface: examples first, complete on every subcommand.
- `--dry-run` on mutating commands, with structured plan output.
- A `skill.md` (+ llms.txt) ships in the repo and with releases; the CLI is designed so that the skill stays short.

## 6. Config, auth & profiles

**Named profiles** (AWS-style terminology; same idea as doctl auth contexts — the word "context" is reserved for `bronto context`, the log-neighborhood command), because SREs juggle EU/US regions, staging/prod orgs, and key roles:

- `bronto auth login [--profile prod-eu]` — validate key, detect region, store.
- `bronto auth switch <profile>`, `bronto auth status` (shows key role; flags ingestion-vs-management mismatch proactively), `bronto auth logout`.
- `--profile <name>` on any command for one-off override.

**Credential storage:** OS keychain by default (macOS Keychain / Secret Service / Windows Credential Manager); fallback to a `0600` credentials file with a warning when no keychain exists (headless/container). Secrets never live in the main config file. `--api-key` exists for CI convenience but is documented second-choice to env.

**Precedence (first non-empty wins):**
1. Flags (`--api-key`, `--profile`, …)
2. Env (`BRONTO_API_KEY`, `BRONTO_PROFILE`, `BRONTO_REGION`) — v1's `BRONTO_CLI_API_KEY` split is retired; profiles solve the collision it worked around
3. Project `.bronto.toml` — non-secret settings only (default profile/dataset/output); walks up parent directories like `.git`; refuses to read secrets
4. User `~/.config/bronto/config.toml` (XDG; `BRONTO_CONFIG_DIR` override)

`bronto config list` prints resolved values **with the source of each** (flag/env/project/user/default).

**Restricted environments:** fully functional with exactly `BRONTO_API_KEY` + `BRONTO_REGION` and zero files; that path never touches keychain or config.

## 7. Distribution

- goreleaser: static binaries (`CGO_ENABLED=0`) for darwin/linux/windows × amd64/arm64; checksummed GitHub Releases; Homebrew tap; `.deb`/`.rpm` (nfpm); Scoop; install script.
- Container images published per release: `FROM scratch` (binary + CA certs, a few MB) and distroless variant.
- Completions generated at build time, bundled in packages.
- `bronto version` prints version, commit, build date — enough for bug reports.
- **No telemetry** without explicit opt-in, ever.

## 8. Testing

- **Unit tests** on the service layer: trace reconstruction, tail dedup/reorder, time-range parsing, query building.
- **Contract tests:** generated client exercised against a Prism mock server started from the vendored spec in CI; request/response shapes validated on every PR.
- **Golden-file tests** for output formats — the JSON contract is public API; snapshot it.
- **End-to-end harness** via `testscript`: exit codes, TTY-vs-pipe behavior, prompting rules, config precedence.
- **CI:** golangci-lint · test · cross-platform build · regeneration-is-clean check · oasdiff breaking-change gate on spec updates · README/skill examples exercised so docs don't rot.

## 9. Contributor experience

- 5-minute path: `git clone && go build && ./bronto --help` — no generation tooling required.
- Paved-road first contribution: "add a resource command" (spec annotation + thin glue over the generated tail).
- Conventional commits; automated changelog/release PRs (release-please or similar).
- `CONTRIBUTING.md`, architecture doc (this file's §4), and the skill file kept in-repo.

## 10. Research inputs (summary)

Three parallel research studies fed this design (full reports in session transcript):

1. **CLI best practices** — clig.dev, 12-Factor CLI, gh/kubectl/aws/gcloud/stripe/doctl/logcli/stern analysis. Key adoptions: TTY discipline, `--json` everywhere + field discovery, typed errors, contexts, keychain storage, stern-style tail UX, no opt-out telemetry (gh's April 2026 backlash), agent-friendliness as first-class (CLIs measured 10–32× cheaper than MCP tools on tokens).
2. **Language comparison** — Go recommended over Rust/Python/TypeScript for this workload; details in §2.
3. **OpenAPI tooling** — industry pattern is generated SDK core + hand-crafted commands (Stripe `ARCHITECTURE.md`, oxide.rs, doctl); fully-generated CLIs mirror APIs, not workflows; oapi-codegen is the leading Go generator; Restish embeddable but generic; Speakeasy CLI-gen is beta/commercial (rejected for a community tool); Stainless defunct as a vendor (acquired May 2026); Prism + oasdiff + checked-in generated code is the standard CI loop.

## 11. Open items deferred to implementation planning

- Exact oapi-codegen configuration (which spec subsets, naming overrides via `x-` extensions).
- Which resources get generated commands in v0.1 vs. `bronto api`-only.
- Table renderer choice and waterfall rendering port details.
- Release cadence and versioning policy pre-1.0.
