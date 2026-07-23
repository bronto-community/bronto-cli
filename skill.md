---
name: bronto-cli
description: Use when querying Bronto logs/traces, sending events, or managing Bronto resources from the command line
---

# bronto-cli

Command-line client for the [Bronto](https://bronto.io) observability platform. This file is a short orientation — `bronto --help` and `bronto <command> --help` are the source of truth for exact flags.

## Auth quickstart

- CI / agents: set `BRONTO_API_KEY` and `BRONTO_REGION` (`eu` or `us`) in the environment. No further setup needed.
- Staging/localhost: `BRONTO_BASE_URL` (and `BRONTO_INGEST_URL` for `send`) override the region-derived hosts; `--base-url` wins over env for one-off calls.
- Humans: `bronto auth login` prompts for a key and stores it in the OS keychain (`--key-stdin` to pipe it instead).
- Scripting from a human's stored credential: `export BRONTO_API_KEY=$(bronto auth token)`.
- Check what's resolved and from where: `bronto auth status`.

## The six workhorse commands

```
bronto search "status >= 500" --since 1h
bronto tail "level = 'error'" --window 5m
bronto traces show <trace-id>
bronto send -d app -m 'hello world'
bronto fields -d <dataset> --since 1h
bronto context --sequence 111721913 -d <dataset> --timestamp 1711535140632
```

`search` runs a one-shot query (`--patterns` clusters matches into drain-style templates with counts — the fastest firehose summary; `--histogram` prints a time distribution of matches instead of events; `--saved <name>` runs a stored saved-search; `--url`/`--open` emit a web-UI deep link instead of executing — the link targets your active org, resolved from the API or the `org_id`/`BRONTO_ORG_ID` config). `tail` polls and follows new events (severity is auto-colored: errors red, warns yellow; `--include 'field~regex'` scopes a filter to one field; `--fields a,b` renders only those fields; with `--select`/`-g` it switches to aggregate mode — the same group-by re-run every `--interval` and redrawn in place at a TTY as a live dashboard with a TREND sparkline, one JSONL snapshot per tick when piped, `--select` defaulting to `count(*)`). `traces` has subcommands (`list`, `show`, `services`, `operations`, `aggregate`, `shape`) over the `.traces` logset. `send` posts one event (`-m`) or streams NDJSON/text lines from stdin. `fields` discovers top-level keys in a dataset. `context` shows events around a specific anchor event.

The default `search` table promotes the most informative parsed fields into columns (ranked by non-null frequency, then value diversity; the raw JSON blob is dropped once three real columns exist) and a TTY run ends with a stderr footer naming the discovery loop: `bronto fields -d <dataset>` lists available fields, `--select` picks columns, and `-x`/`--expand` renders every field of every event vertically, untruncated, including the `metadata.*`/`links` plumbing the table hides. `-x` is table-only — with `-o json`/`jsonl`/`csv`/`raw` it fails usage-style (those formats already carry every field; pass `-o table` to force the expanded view in a pipe).

`-d`/`--dataset` accepts a dataset **name** or UUID everywhere; a name duplicated across collections is qualified as `collection/name` (e.g. `-d prod/api-logs`). With one dataset in the account it is auto-picked; with several, the error lists them.

Agent-critical flags (global): `--dry-run` prints any mutating call as a plan document (`{"dry_run":true,"method":"POST","path":"/monitors","body":{…}}`) instead of executing — reads still run. `--debug` traces requests/responses on stderr (API key never printed). `--timeout <s>` and `--max-retries <n>` tune the HTTP client.

## Offline mode

`bronto search --local <file|-> "<query>"` evaluates the query client-side over local NDJSON or plain text (downloaded exports, `kubectl logs` dumps) — no server, no auth needed. JSON lines are queryable by their (dotted) keys; plain lines match via `@raw ~ 'regex'`. Composes with `-o`, `--fields`, `--jq`, and `-n`; dataset/time-range/select/group flags don't apply and are rejected.
## Interactive REPL

`bronto repl -d <dataset>` opens a psql-style prompt for iterative investigation (TTY only — piped invocations are refused with exit 2). Type a WHERE expression to run it; `\since <dur>` and `\d <dataset>` change window/dataset, `\more` pages, `\fields` lists keys, `\tail` follows live until Ctrl-C, `\q` quits. Line history persists across sessions in the config dir.

## Ask (LLM-assisted)

`bronto ask "<question>"` translates natural language into a search using a user-configured OpenAI-compatible endpoint (`bronto config set ask_url <chat-completions URL>`, optional `ask_model`, key via `BRONTO_ASK_API_KEY` env — never the config file). The generated command and its reasoning print BEFORE anything runs; a TTY confirms `[Y/n]`, `--yes` runs immediately, and machine formats without `--yes` emit the plan as JSON instead of executing. Only the question plus dataset/field names are sent to the endpoint — never event data, never the Bronto API key.

## Machine-output contract

- Streaming commands (`search`, `tail`, `traces`) piped to a non-TTY default to JSONL, one JSON object per line — no flag needed. Every other command (resource `list`/`get`, `usage`, `config list`, …) piped emits a single pretty-printed JSON document (usually an array) — parse it whole, not line-by-line; pass `-o jsonl` if you want line-delimited rows.
- Force a format with `-o json|jsonl|raw|csv|table`.
- `--jq '<expr>'` runs a jq expression over json/jsonl output, one result per line. Deviation from the `jq` CLI: a value that errors or halts on the expression is silently **skipped**, not a fatal abort — every other row still prints.
- `--fields a,b,c` narrows output to those columns/keys; `--fields ?` lists the field names available instead of the data. Works with json/jsonl/csv, table for resource lists, and `tail`'s table view (klp-style projection). `-o raw` and the custom trace renderers (`traces show`, `traces shape`) reject `--fields`; `--fields ?` (name listing) needs a machine format like `-o jsonl` for the streaming views (`tail`, `traces`).
- Errors go to stderr. In machine mode (non-TTY stderr) they're a stable JSON envelope: `{"error":{"code":"...","message":"...","retryable":true|false,"hint":"..."}}` (`hint` present only when there is remediation advice).
- Numbers are lossless: 64-bit ids (e.g. `metadata.sequence`) survive json/jsonl/`--jq` byte-exact.
- Exit codes:

  | Code | Meaning |
  |------|---------|
  | 0 | success |
  | 1 | unexpected/unclassified error |
  | 2 | usage error (bad flags/args) or config error |
  | 3 | auth error |
  | 4 | not found |
  | 5 | rate limited or timeout (retryable) |

## Resource commands

Resources (`datasets`, `monitors` — plus nested `monitors templates` and `monitors downtimes` — `dashboards`, `parsers`, `exports`, `api-keys`, `saved-searches`, `users`, `groups`, `webhooks`, `slack`, `limits`, `encryption-keys`, `forward-configs`, and read-only `collections` / `log-views`) share one pattern (list-only where the API documents no other verbs):

```
bronto <resource> list
bronto <resource> get <id-or-name>
bronto <resource> create -f key=value -f other=value    # or --input body.json / --input -
bronto <resource> update <id-or-name> -f key=value
bronto <resource> delete <id-or-name> --yes              # --yes skips the confirmation prompt
```

Exceptions: no `get` for `parsers`, `api-keys`, `forward-configs`, `webhooks`, `slack`, `monitors downtimes`; no `update` for `exports`.

A unique name resolves anywhere an id is accepted (users match by email; datasets support `collection/name`). Ambiguous names error with the candidate ids.

`api-keys list` masks key material in **every** format (including json/jsonl) by default so keys don't leak into pipelines or CI logs; pass `--reveal` for the full values.

Extras beyond the uniform pattern: `monitors events|mute|check` (`check --input monitor.json` lints definitions — query syntax, window bounds, dataset existence — with non-zero exit for CI), `users deactivate|reactivate|resend-invite`, `groups members`.

## Utility commands

`bronto ping` (reachability + latency), `bronto version` (`-o json` for machine parsing), `bronto config list` (resolved config with provenance), `bronto usage --since 7d` (ingestion/search volume).

## Query validation

`bronto query check "<expr>"` validates syntax client-side (caret-positioned errors) and, with `-d <dataset>`, warns on fields not seen recently (did-you-mean included); `--strict` makes unknown fields fatal for CI. Server 400s on searches automatically carry the same local diagnosis when applicable.

## Escape hatch

Any endpoint without a dedicated command: `bronto api <METHOD> <path>`, e.g. `bronto api GET /monitors -f limit=10` or `bronto api POST /search --input query.json`. Auth and region are resolved the same way as every other command.

## Exports

```
bronto exports create --dataset <dataset> --since 1h --where "status=500" --wait
bronto exports create --dataset <dataset> --since 1h --download out.json.gz
```

`--wait` polls until the export is `COMPLETE`/`FAILED`, giving up after `--wait-timeout` (default 15m) with a non-zero `export_wait_timeout` exit; `--download <path>` implies `--wait` and saves the result.

## Plugins

An executable named `bronto-<name>` anywhere on `PATH` is invoked when `<name>` is the first argument — e.g. `bronto deploy` invokes a `bronto-deploy` executable found on PATH. <!-- skilldoc:ignore: illustrative plugin name, not a real subcommand -->
Discover installed plugins with `bronto plugins list`. Plugins do **not** inherit keychain-stored credentials — only environment variables are passed through. A plugin needing API access should call `bronto auth token` itself or require the caller to set `BRONTO_API_KEY` directly.
