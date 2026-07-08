# bronto-cli

A community command-line client for the [Bronto](https://bronto.io) observability platform. It wraps Bronto's REST and ingestion APIs in a single scriptable binary: search and tail logs, explore OpenTelemetry traces, send events, and manage resources (datasets, monitors, dashboards, parsers, exports, API keys) — all with JSONL-by-default output, typed errors, and stable exit codes so it drops cleanly into shell pipelines and agent tool calls.

This project is not affiliated with or endorsed by Bronto.

## Install

### Homebrew (coming soon)

```sh
brew install svrnm/tap/bronto
```

The tap isn't published yet — this will work once `homebrew-tap` exists and the release workflow's cask upload is flipped from `skip_upload` to `auto`. Until then, use one of the options below.

### curl install script

```sh
curl -fsSL https://raw.githubusercontent.com/svrnm/bronto-cli/main/scripts/install.sh | sh
```

Downloads the latest (or a pinned `VERSION=`) release for your OS/arch from GitHub Releases and verifies its checksum against the release's `checksums.txt` before installing to `/usr/local/bin` (override with `BINDIR=`).

### go install

```sh
go install github.com/svrnm/bronto-cli/cmd/bronto@latest
```

### Docker

```sh
docker run --rm -e BRONTO_API_KEY -e BRONTO_REGION ghcr.io/svrnm/bronto-cli:latest search "status >= 500" --since 1h
```

Two image variants are published on every release: the default (`scratch` + CA certs) and `-distroless` (`ghcr.io/svrnm/bronto-cli:latest-distroless`), both `ENTRYPOINT ["/bronto"]`. Neither image has a shell, so pass credentials as environment variables rather than files (see [Restricted environments](#restricted-environments)).

### Releases

Prebuilt archives (tar.gz for macOS/Linux, zip for Windows) plus `.deb`/`.rpm` packages and shell completions are attached to every [GitHub release](https://github.com/svrnm/bronto-cli/releases).

## Quickstart

```sh
bronto auth login                              # paste an API key, stored in the OS keychain
bronto search "status >= 500" --since 1h       # one-shot query
bronto tail "level = 'error'" --window 5m      # follow new events live
```

`bronto auth login` prompts for a key interactively (or `--key-stdin` to pipe one in) and picks a region (`eu`/`us`). Everything after that resolves credentials automatically — no further setup.

## Command tour

**Explore** — search, tail, and traces:

```sh
bronto search "status >= 500" --since 1h
bronto search --select "count()" -g host --since 15m
bronto tail "level = 'error'" --include timeout --exclude healthz
bronto traces show <trace-id>
bronto traces services --since 1h
bronto fields -d <dataset-uuid> --since 1h
bronto context --sequence 111721913 -d <dataset-uuid> --timestamp 1711535140632
```

`traces` also has `list`, `operations`, `aggregate`, and `shape` subcommands over the `.traces` logset.

**Manage** — resources, exports, usage:

```sh
bronto datasets list
bronto monitors get <id>
bronto dashboards create -f name=Overview -f description=Prod
bronto parsers update <id> -f name=new-name
bronto api-keys delete <id> --yes
bronto exports create --dataset <uuid> --since 1h --where "status=500" --wait
bronto usage --since 7d
```

Every resource (`datasets`, `monitors`, `dashboards`, `parsers`, `exports`, `api-keys`, `saved-searches`) shares the same `list | get <id> | create | update <id> | delete <id>` pattern; `create`/`update` take repeated `-f key=value` or `--input file.json`/`--input -`, and `delete` prompts for confirmation unless `--yes` is passed.

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
```

Anything without a dedicated command is reachable via the escape hatch: `bronto api GET /monitors -f limit=10` or `bronto api POST /search --input query.json`.

## Scripting & agents

Output to a non-TTY (piped or redirected) defaults to **JSONL**, one JSON object per line — no flag needed. Force a format explicitly with `-o table|json|jsonl|raw|csv`.

```sh
bronto search "status >= 500" --since 1h --jq '.message' | wc -l
bronto datasets list --fields id,name
bronto datasets list --fields '?'          # list available field names instead of data
```

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
| `BRONTO_PROFILE` | named profile to use |
| `BRONTO_TIMEOUT` | request timeout override |
| `BRONTO_INGEST_URL` | override the ingestion endpoint (`bronto send`) |
| `BRONTO_CONFIG_DIR` | override the user config directory (parent of `bronto/config.toml`) |

## Restricted environments

For containers, CI runners, or sandboxes without a usable OS keychain or writable home directory:

- Set `BRONTO_API_KEY` (and `BRONTO_REGION`) directly — no keychain access is attempted once a key is already resolved.
- Set `BRONTO_CONFIG_DIR` to a writable path if you need `bronto config set` or profile files to work somewhere other than the default user config directory.

The published scratch-based image (`ghcr.io/svrnm/bronto-cli:latest`) has no shell, package manager, or keychain daemon by design — pass credentials as environment variables when running it.

## Plugins

Any executable named `bronto-<name>` on `PATH` is invoked when `<name>` is the first argument that doesn't match a built-in command — e.g. `bronto deploy` invokes a `bronto-deploy` executable found on PATH. <!-- skilldoc:ignore: illustrative plugin name, not a real subcommand --> Built-in commands always take precedence, so a plugin can't shadow `search`, `auth`, etc. Discover installed plugins with `bronto plugins list`.

Plugins do **not** inherit keychain-stored credentials — only environment variables are passed through. A plugin needing API access should call `bronto auth token` itself (prints the resolved key for scripting) or require the caller to set `BRONTO_API_KEY` directly.

## Development

```sh
git clone https://github.com/svrnm/bronto-cli
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
