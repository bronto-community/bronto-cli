# bronto-cli

[![CI](https://github.com/bronto-community/bronto-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/bronto-community/bronto-cli/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/bronto-community/bronto-cli)](https://github.com/bronto-community/bronto-cli/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/bronto-community/bronto-cli/badge)](https://scorecard.dev/viewer/?uri=github.com/bronto-community/bronto-cli)

A community command-line client for the [Bronto](https://bronto.io) observability platform. One scriptable binary wraps Bronto's REST and ingestion APIs: search and tail logs, explore OpenTelemetry traces, send events, and manage every resource from datasets to monitors.

Built for pipelines and agents: JSONL by default when piped, typed errors with machine-readable hints, stable exit codes, and `--dry-run` plans for every mutating call.

bronto-cli is an official open-source project from [Bronto](https://bronto.io), maintained as a **community artifact**: free to use, contributions welcome — but not covered by Bronto's product support. Questions, bugs, and feature requests are handled best-effort through [GitHub issues](https://github.com/bronto-community/bronto-cli/issues), not Bronto's support channels.

## Install

### Homebrew (coming soon)

```sh
brew install bronto-community/tap/bronto
```

The tap isn't published yet — this will work once `homebrew-tap` exists and the release workflow's cask upload is flipped from `skip_upload` to `auto`. Until then, use one of the options below.

### curl install script

```sh
curl -fsSL https://raw.githubusercontent.com/bronto-community/bronto-cli/main/scripts/install.sh | sh
```

Downloads the latest (or a pinned `VERSION=`) release for your OS/arch from GitHub Releases and verifies its checksum against the release's `checksums.txt` before installing to `/usr/local/bin` (override with `BINDIR=`).

### go install

```sh
go install github.com/bronto-community/bronto-cli/cmd/bronto@latest
```

### Docker

```sh
docker run --rm -e BRONTO_API_KEY -e BRONTO_REGION ghcr.io/bronto-community/bronto-cli:latest search "status >= 500" --since 1h
```

Two image variants are published on every release: the default (`scratch` + CA certs) and `-distroless` (`ghcr.io/bronto-community/bronto-cli:latest-distroless`), both `ENTRYPOINT ["/bronto"]`. Neither image has a shell, so pass credentials as environment variables rather than files (see [Restricted environments](#restricted-environments)).

### Releases

Prebuilt archives (tar.gz for macOS/Linux, zip for Windows) plus `.deb`/`.rpm` packages and shell completions are attached to every [GitHub release](https://github.com/bronto-community/bronto-cli/releases).

## Quickstart

```sh
bronto auth login                                  # paste an API key, stored in the OS keychain
bronto datasets list                               # see what data you have
bronto search "status >= 500" -d <dataset> --since 1h   # one-shot query
bronto tail "level = 'error'" -d <dataset> --window 5m  # follow new events live
```

`bronto auth login` prompts for a key interactively (or `--key-stdin` to pipe one in) and picks a region (`eu`/`us`). Everything after that resolves credentials automatically.

`-d` takes a dataset **name** from `datasets list` (UUIDs work too). A name that exists in several collections is qualified as `collection/name` — e.g. `-d prod/api-logs`. You can drop `-d` entirely once you set a default (`bronto config set default_dataset <name>`) — or if the account has only one dataset, which is auto-picked.

At a terminal, `bronto search "status >= 500" --since 1h` renders a table:

```
@TIME                        @STATUS  @RAW                                       MESSAGE_KVS.STATUS
2026-07-19 09:14:05.312 UTC  error    {"level":"error","status":502,"path":...}  502
2026-07-19 09:13:58.007 UTC  error    {"level":"error","status":500,"path":...}  500
```

Piped, the same command emits JSONL — one full event per line, 64-bit ids preserved exactly:

```json
{"@time":"2026-07-19 09:14:05.312 UTC","@status":"error","message_kvs.status":502,"metadata.sequence":4367602734065516544,...}
```

## Command tour

**Explore** — search, tail, and traces:

```sh
bronto search "status >= 500" --since 1h
bronto search --select "count()" -g host --since 15m
bronto tail "level = 'error'" --include timeout --exclude healthz
bronto traces show <trace-id>
bronto traces services --since 1h
bronto fields -d <dataset> --since 1h
bronto context --sequence 111721913 -d <dataset> --timestamp 1711535140632
```

`traces` also has `list`, `operations`, `aggregate`, and `shape` subcommands over the `.traces` logset.

**Manage** — resources, exports, usage:

```sh
bronto datasets list
bronto monitors get <id>
bronto dashboards create -f name=Overview -f description=Prod
bronto parsers update <id> -f name=new-name
bronto api-keys delete <id> --yes
bronto exports create -d <dataset> --since 1h --where "status=500" --wait
bronto usage --since 7d
bronto users list
bronto groups create -f name=oncall
bronto monitors templates list
bronto webhooks create -f name=alerts -f url=https://example.com/hook
```

Every resource (`datasets`, `monitors` — incl. `monitors templates` and `monitors downtimes` — `dashboards`, `parsers`, `exports`, `api-keys`, `saved-searches`, `users`, `groups`, `webhooks`, `slack`, `limits`, `encryption-keys`, `forward-configs`, plus read-only `collections` and `log-views`) shares the same `list | get <id|name> | create | update <id|name> | delete <id|name> --yes` pattern (list-only where the API documents no other verbs). Everywhere an id is accepted, a unique **name** works too (users: email; datasets: `collection/name` qualifies duplicates) — ambiguous names error with the candidates; `create`/`update` take repeated `-f key=value` or `--input file.json`/`--input -`, and `delete` prompts for confirmation unless `--yes` is passed.

**Pipe** — send data in:

```sh
bronto send -d app -m 'hello world'
echo '{"message":"m","level":"warn"}' | bronto send -d app
```

`send` posts one event with `-m`/`--message`, or reads NDJSON/plain-text lines from stdin and batches them (`--batch-size`, `--batch-bytes`, `--flush-interval`) — e.g. `tail -f access.log | bronto send -d app --collection prod`. <!-- skilldoc:ignore: tail here is the unix coreutil, not bronto tail -->

**Ops** — auth, config, plugins:

```sh
bronto auth status
bronto config list
bronto plugins list
bronto ping
bronto version
```

Anything without a dedicated command is reachable via the escape hatch: `bronto api GET /monitors -f limit=10` or `bronto api POST /search --input query.json`.

## Scripting & agents

Output to a non-TTY (piped or redirected) defaults to **JSONL**, one JSON object per line — no flag needed. Force a format explicitly with `-o table|json|jsonl|raw|csv`.

```sh
bronto search "status >= 500" --since 1h --jq '.message' | wc -l
bronto query check "stauts >= 500" -d api-logs      # catches the typo before the server does
bronto datasets list --fields log,log_id
bronto datasets list --fields '?'          # list available field names instead of data
```

- `--dry-run` prints any mutating API call as a plan document (`{"dry_run":true,"method":"POST","path":"/monitors","body":{…}}`) instead of executing it — reads still run, so dataset-name resolution keeps working. Destructive commands skip their confirmation prompt (nothing destructive can happen).
- `--debug` traces every API request/response on stderr (method, URL, status, latency, truncated bodies — the API key never appears).
- `--timeout <seconds>` and `--max-retries <n>` tune the HTTP client per invocation (also config keys / `BRONTO_TIMEOUT`, `BRONTO_MAX_RETRIES`).
- `--jq '<expr>'` runs a [gojq](https://github.com/itchyny/gojq) expression over json/jsonl output, one result per line. Unlike the `jq` CLI, a value that errors or halts on the expression is silently **skipped** — every other row still prints.
- `--fields a,b,c` narrows table/json/jsonl/csv output to those columns/keys.
- Errors go to stderr; in machine mode (non-TTY stderr) they're a stable JSON envelope: `{"error":{"code":"...","message":"...","retryable":true|false}}`.
- Exit codes are stable: `0` success, `1` unexpected error, `2` usage/config error, `3` auth error, `4` not found, `5` rate limited or timeout (retryable).

For agents (Claude Code, MCP tool wrappers, etc.), see [`skill.md`](./skill.md) for a short orientation doc, or [`llms.txt`](./llms.txt) for a 20-line summary. `bronto --help` and `bronto <command> --help` are always the authoritative reference.

## Configuration

Values resolve with this precedence, highest first:

| Precedence | Source | Example |
|---|---|---|
| 1 | Flags | `--region eu`, `--api-key ...` |
| 2 | Environment variables | `BRONTO_API_KEY`, `BRONTO_REGION` |
| 3 | Project file `.bronto.toml` (walks up from cwd, like `.git`) | `region = "eu"` |
| 4 | User config `<config dir>/bronto/config.toml` (profile section) | `[profiles.prod]` |
| 5 | Built-in defaults | `region = "eu"` |

Run `bronto config list` to see every resolved value and which source it came from; `bronto config get <key>` for one value; `bronto config set <key> <value>` to persist to the user config file. `api_key` is deliberately never read from either TOML file — secrets only come from the OS keychain (`bronto auth login`) or `BRONTO_API_KEY`.

Profiles let you keep multiple accounts/regions side by side: `bronto auth login --profile prod`, then `bronto auth switch prod` or `--profile prod` per-invocation.

Environment variables:

| Variable | Purpose |
|---|---|
| `BRONTO_API_KEY` | API key (bypasses the keychain) |
| `BRONTO_REGION` | `eu` or `us` |
| `BRONTO_BASE_URL` | full API base URL (staging, localhost) — overrides the region-derived URL |
| `BRONTO_PROFILE` | named profile to use |
| `BRONTO_TIMEOUT` | request timeout override (seconds) |
| `BRONTO_MAX_RETRIES` | retries for idempotent requests on 429/5xx |
| `BRONTO_INGEST_URL` | override the ingestion endpoint (`bronto send`) |
| `BRONTO_CONFIG_DIR` | override the user config directory (parent of `bronto/config.toml`) |

Config keys (settable via `bronto config set`, project `.bronto.toml`, or profile files — flags and env always win):

| Key | Env | Purpose | Default |
|---|---|---|---|
| `region` | `BRONTO_REGION` | `eu` or `us` | `eu` |
| `base_url` | `BRONTO_BASE_URL` | full API base URL override (staging/localhost) | derived from region |
| `output` | — | default output format | table (TTY) / jsonl (piped) |
| `default_dataset` | — | dataset name/UUID or `from_expr` used when `-d` is omitted | — |
| `timeout` | `BRONTO_TIMEOUT` | HTTP timeout in seconds | 30 |
| `max_retries` | `BRONTO_MAX_RETRIES` | retries for idempotent requests | 2 |
| `ingest_url` | `BRONTO_INGEST_URL` | ingestion endpoint for `bronto send` | derived from region |
| `profile` | `BRONTO_PROFILE` | named profile | `default` |

`api_key` is deliberately **not** file-settable — keys live in the keychain or `BRONTO_API_KEY` only.

## Troubleshooting

- **`auth_invalid_key` (exit 3)** — the key is wrong or an ingestion key was used where a management key is needed. Run `bronto auth login`, or check `bronto auth status` (exits non-zero when the credential is broken, so you can gate scripts on it).
- **No OS keychain (containers, CI)** — set `BRONTO_API_KEY` directly; the keychain is never touched once a key is resolved. `bronto auth login` falls back to a credentials file with a warning.
- **`usage_confirmation_required` (exit 2)** — a destructive command ran without a TTY; pass `--yes` (or `--dry-run` to preview).
- **`usage_missing_dataset`** — the account has several datasets; the error lists them. Pick one with `-d <name>` or set `default_dataset`.
- **Wrong region** — `bronto ping` shows the resolved base URL and latency; override with `--region` / `BRONTO_REGION`.
- **What is it actually sending?** — add `--debug` for a curl-style trace (API key never printed), or `--dry-run` to see mutating request bodies without executing.

## Staging & local development

Point the CLI at any Bronto-compatible API — a staging environment or a local instance:

```sh
export BRONTO_BASE_URL=http://localhost:8080   # or --base-url per invocation
export BRONTO_INGEST_URL=http://localhost:8081 # ingestion host for `bronto send`
```

Flags beat env, env beats config files, so a one-off `--base-url` always wins. Keep environments cleanly separated with profiles instead: `bronto config set base_url https://api.staging.example --profile staging`, then `--profile staging` (or `BRONTO_PROFILE=staging`) per invocation. `bronto ping` and `bronto auth status` show which base URL actually resolved.

## Restricted environments

For containers, CI runners, or sandboxes without a usable OS keychain or writable home directory:

- Set `BRONTO_API_KEY` (and `BRONTO_REGION`) directly — no keychain access is attempted once a key is already resolved.
- Set `BRONTO_CONFIG_DIR` to a writable path if you need `bronto config set` or profile files to work somewhere other than the default user config directory.

The published scratch-based image (`ghcr.io/bronto-community/bronto-cli:latest`) has no shell, package manager, or keychain daemon by design — pass credentials as environment variables when running it.

## Plugins

Any executable named `bronto-<name>` on `PATH` is invoked when `<name>` is the first argument that doesn't match a built-in command — e.g. `bronto deploy` invokes a `bronto-deploy` executable found on PATH. <!-- skilldoc:ignore: illustrative plugin name, not a real subcommand --> Built-in commands always take precedence, so a plugin can't shadow `search`, `auth`, etc. Discover installed plugins with `bronto plugins list`.

Plugins do **not** inherit keychain-stored credentials — only environment variables are passed through. A plugin needing API access should call `bronto auth token` itself (prints the resolved key for scripting) or require the caller to set `BRONTO_API_KEY` directly.

## Development

```sh
git clone https://github.com/bronto-community/bronto-cli
cd bronto-cli
make build   # -> ./bronto
make test    # go test ./...
make lint    # golangci-lint run
```

No code generation is required for day-to-day development — the vendored API client is already checked in. See [CONTRIBUTING.md](./CONTRIBUTING.md) for the architecture map, TDD/lint expectations, and how to add a new resource command.

## No telemetry

bronto-cli sends no telemetry, analytics, or usage data anywhere. The only network calls it makes are the ones you ask for: requests to the Bronto API and ingestion endpoints you've configured.

## License

MIT — see [LICENSE](./LICENSE).
