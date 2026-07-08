---
name: bronto-cli
description: Use when querying Bronto logs/traces, sending events, or managing Bronto resources from the command line
---

# bronto-cli

Command-line client for the [Bronto](https://bronto.io) observability platform. This file is a short orientation — `bronto --help` and `bronto <command> --help` are the source of truth for exact flags.

## Auth quickstart

- CI / agents: set `BRONTO_API_KEY` and `BRONTO_REGION` (`eu` or `us`) in the environment. No further setup needed.
- Humans: `bronto auth login` prompts for a key and stores it in the OS keychain (`--key-stdin` to pipe it instead).
- Scripting from a human's stored credential: `export BRONTO_API_KEY=$(bronto auth token)`.
- Check what's resolved and from where: `bronto auth status`.

## The six workhorse commands

```
bronto search "status >= 500" --since 1h
bronto tail "level = 'error'" --window 5m
bronto traces show <trace-id>
bronto send -d app -m 'hello world'
bronto fields -d <dataset-uuid> --since 1h
bronto context --sequence 111721913 -d <dataset-uuid> --timestamp 1711535140632
```

`search` runs a one-shot query. `tail` polls and follows new events. `traces` has subcommands (`list`, `show`, `services`, `operations`, `aggregate`, `shape`) over the `.traces` logset. `send` posts one event (`-m`) or streams NDJSON/text lines from stdin. `fields` discovers top-level keys in a dataset. `context` shows events around a specific anchor event.

## Machine-output contract

- Output to a non-TTY (piped/redirected) defaults to JSONL, one JSON object per line — no flag needed.
- Force a format with `-o json|jsonl|raw|csv|table`.
- `--jq '<expr>'` runs a jq expression over json/jsonl output, one result per line. Deviation from the `jq` CLI: a value that errors or halts on the expression is silently **skipped**, not a fatal abort — every other row still prints.
- `--fields a,b,c` narrows output to those columns/keys; `--fields ?` lists the field names available instead of the data. Only works with table/json/jsonl/csv; `-o raw` and custom TTY renderers (`traces show`, `traces shape`) reject `--fields` with a usage error pointing at a machine format.
- Errors go to stderr. In machine mode (non-TTY stderr) they're a stable JSON envelope: `{"error":{"code":"...","message":"...","retryable":true|false}}`.
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

Resources (`datasets`, `monitors`, `dashboards`, `parsers`, `exports`, `api-keys`, `saved-searches`) share one pattern:

```
bronto <resource> list
bronto <resource> get <id>
bronto <resource> create -f key=value -f other=value    # or --input body.json / --input -
bronto <resource> update <id> -f key=value
bronto <resource> delete <id> --yes                      # --yes skips the confirmation prompt
```

## Escape hatch

Any endpoint without a dedicated command: `bronto api <METHOD> <path>`, e.g. `bronto api GET /monitors -f limit=10` or `bronto api POST /search --input query.json`. Auth and region are resolved the same way as every other command.

## Exports

```
bronto exports create --dataset <uuid> --since 1h --where "status=500" --wait
bronto exports create --dataset <uuid> --since 1h --download out.json.gz
```

`--wait` polls until the export is `COMPLETE`/`FAILED`; `--download <path>` implies `--wait` and saves the result.

## Plugins

An executable named `bronto-<name>` anywhere on `PATH` is invoked when `<name>` is the first argument — e.g. `bronto deploy` invokes a `bronto-deploy` executable found on PATH. <!-- skilldoc:ignore: illustrative plugin name, not a real subcommand -->
Discover installed plugins with `bronto plugins list`. Plugins do **not** inherit keychain-stored credentials — only environment variables are passed through. A plugin needing API access should call `bronto auth token` itself or require the caller to set `BRONTO_API_KEY` directly.
