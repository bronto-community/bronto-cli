# bronto-cli docs — conventions

The user-facing documentation for the `bronto` CLI, written for
[Mintlify](https://mintlify.com). The pages are published in two places:

- **Dev site:** a Mintlify Starter project that deploys `docs/` from `main`
  through Mintlify's GitHub app. It shows only the CLI tab.
- **Official docs:** https://docs.bronto.io/cli. When a release is
  published, the `docs-sync` workflow copies `docs/cli/` at that tag into
  the official docs repository with `scripts/sync-official-docs.sh` and
  opens a pull request there for a maintainer to merge. Never edit the
  copy there: the next sync overwrites it.

Everything a page links to or embeds must therefore work in both places:
pages and assets live under `cli/`, and links into the rest of
docs.bronto.io are full `https://docs.bronto.io/...` URLs.

**What these docs are:** task-oriented guides for *using the CLI*. They are
**not** a copy of the Bronto API/product docs. Whenever a concept belongs to
the Bronto platform (datasets, collections, monitors, the query language,
regions, API keys, …) explain it in one sentence and link to the matching page
on https://docs.bronto.io/ instead of re-documenting it.

## Naming

**Bronto** (capitalized) is the product: the platform, its API, ingestion, the
web app. This repo is the **bronto CLI**. Never call the CLI plain "bronto" or
"Bronto" in prose, because readers take that to mean the product:

- In body text, write `` `bronto` `` (code formatting) for the program as you
  run it, or "the bronto CLI" (or "the CLI" once the context is clear) for the
  tool as a whole. For example: "`bronto` reads these variables" or "The bronto
  CLI doesn't emit metrics", not "bronto reads…" or "bronto doesn't emit…".
- Commands are always code: `` `bronto search` ``, `` `bronto api` ``.
- Frontmatter `description:`, `<Tape title="…">` and other plain-text fields
  can't hold code formatting. Write "the bronto CLI" there. A full command name
  such as "with bronto search" is fine.
- "Bronto" without qualification means the platform: "Bronto runs the export
  in the background", "send traces to Bronto".
- The repository and project are `bronto-cli`.

This also applies to `README.md`, `CONTRIBUTING.md` and other repo documents.
CLI help text (cobra `Short`/`Long`) is exempt: in a terminal the program name
is the subject, as in "bronto adds the API key". It reaches the generated
reference pages unchanged.

## Layout and ownership

| Path | What | Who edits |
| --- | --- | --- |
| `docs.json`, `.mintignore`, `logo/`, `package.json` | Dev-site shell: theme, logos, the pinned `mint` CLI. Not copied to the official docs. | site |
| `cli/navigation.json` | The CLI tab: groups and page order. Shared by both sites. | content |
| `cli/**` except `reference/commands/` and `tapes/` | Hand-written pages | content |
| `cli/reference/commands/**` | **Generated** command reference and its `navigation.json` (`make docs-reference`). Never hand-edit. | generator |
| `tapes/*.tape`, `tapes/golden/*.txt` | VHS recordings and their text goldens | tapes |
| `cli/tapes/*.webm`, `cli/tapes/*.png` | Rendered videos and posters (committed, produced by `make docs-tapes`) | tapes |
| `testdata/mock/**` | Fixtures for the deterministic docs mock server | tapes |
| `mk/*.mk` | Make fragments (top-level `Makefile` does `-include docs/mk/*.mk`) | one file per owner |

## Sidebar / directories

The sidebar comes from `cli/navigation.json`. A new page appears only once
it is listed there, as `cli/<dir>/<page>` without the extension. Pages not
listed are still reachable by URL, so don't leave drafts in `cli/`. The
command reference group is generated into
`cli/reference/commands/navigation.json` and pulled in with `"$ref"`.

Frontmatter takes `title` and `description`, plus `sidebarTitle` when the
sidebar needs a shorter label than the title.

Directories under `cli/`:

- `index.mdx`: landing page
- `getting-started/`: install, authentication, quickstart
- `guides/`: task guides (searching, tail, traces, ask, sending data, managing resources, exports, scripting & agents, CI usage, …)
- `configuration/`: config files and profiles, environments (staging/local/restricted), shell completion
- `extend/`: plugins, the `bronto api` escape hatch
- `troubleshooting.mdx`
- `reference/`: generated command reference under `reference/commands/`; hand-written reference pages (exit codes, environment variables, output formats) sit next to it
- `tapes/`: rendered recordings (see "Videos")

## Components

Use Mintlify's built-in components; there are no imports:

- Callouts: `<Note>`, `<Tip>`, `<Warning>`. For a titled callout, start it
  with a bold line: `**No keychain?**`.
- `<Tabs>` with `<Tab title="zsh">`; tabs with the same title stay in sync
  across a page.
- `<CardGroup cols={2}>` with `<Card title="…" icon="…" href="…">`. Icons are
  Font Awesome names.
- Comments are MDX comments, `{/* … */}`. HTML comments break the build.

## Code blocks

- **Tested snippets** use the `console` language. Every `console` block is
  tested: the snippet tester (`make docs-snippets`) runs it against the
  deterministic docs mock server using the binary built from this repo.
  Options go in an MDX comment on the line directly above the fence,
  because Mintlify shows any words after the language as the block's title:

  ````md
  {/* test: tty */}
  ```console
  $ bronto datasets list
  COLLECTION  DATASET           ...
  prod        checkout-service  ...
  ...
  ```
  ````

  - A line starting with `$ ` is a command. The lines after it, up to the next
    `$ `, are the expected stdout.
  - Lines are compared exactly after trailing whitespace is trimmed. A line
    that is exactly `...` matches any number of lines (zero or more).
  - A command with no expected-output lines only has to exit 0.
  - `exit=N` expects exit code N for every command in the block.
  - `tty` gives every command a pseudo-terminal as stdout, so `bronto`
    prints what a reader sees in a terminal: tables by default, with no
    `-o table`. Use `tty` for interactive examples. Leave it out, so
    stdout is a pipe, when the JSON or JSONL output is the point. stdin is
    never a terminal, so prompts refuse instead of waiting. Options
    combine, for example `{/* test: tty exit=2 */}`.
  - Expected output is stdout only. To show something printed on stderr, such
    as an error message, add `2>&1`. `--dry-run` plans print on stdout, so
    they need no redirect.
  - Avoid `wc -l` and `uniq -c`, which pad numbers differently on macOS and
    Linux. Use `jq -s length` instead.
  - Pipes into common tools (`jq`, `head`, `wc`) are allowed.
- **Illustrative snippets** (placeholders like `<id>`, needs live data or an
  LLM, and so on) use ` ```sh `, never ` ```console `. They are still checked by the doc-rot guard:
  every `bronto <cmd> <subcmd>` must be a real command. Write placeholders as
  `<...>`. Put `{/* skilldoc:ignore: reason */}` on the same line to exempt a
  deliberately fake example.

## Videos (VHS)

Embed a recording with:

```mdx
<Frame caption="Searching the last hour of errors">
  <video src="/cli/tapes/search.webm" poster="/cli/tapes/search.png" aria-label="Searching the last hour of errors" autoPlay muted loop playsInline controls />
</Frame>
```

`docs/tapes/<name>.tape` renders to `cli/tapes/<name>.webm` and `.png`, and
to `tapes/golden/<name>.txt`, which CI compares against a mock-mode run.
Tapes available to embed: `quickstart`, `search`, `tail`, `traces`,
`resources`, `scripting`. The link check doesn't cover `src`/`poster`, so
check a new embed in `make docs-dev`.

## Linking to docs.bronto.io

Use full URLs, for example `[datasets](https://docs.bronto.io/...)`, so the
link works on the dev site too. Add at
least one "Learn more on docs.bronto.io" link per guide where a platform
concept is involved. Check that the target page exists.

When a page covers a resource or an API feature, also link the matching
API reference page (`https://docs.bronto.io/api-reference/...`; for a
resource, its list operation). Platform concept and API links in the
generated command reference come from `docs/reference-links.yaml`, which
takes a `docs:` and an `api:` URL per command.

The bronto CLI is open source and community-supported. Pages that tell
readers where to get help point to GitHub issues, not to Bronto support.

## Commands

```sh
make docs-dev             # local preview on http://localhost:3000
make docs-validate        # strict Mintlify build + link and anchor check
make docs-reference       # regenerate reference/commands from cobra
make docs-check           # everything CI runs for docs
```

## The docs mock world

Tested snippets and tapes all run against one deterministic fake Bronto org,
served by `internal/tools/docsmock` from `docs/testdata/mock/`. Write examples
in terms of this data:

- Region `eu`. API key `bronto_docs_example_key` (a fake key the mock accepts).
- Collections: `prod`, `staging`.
- Datasets: `prod/checkout-service`, `prod/payments-api`, `prod/web-frontend`,
  `staging/checkout-service`.
- Log fields: `@time`, `@status` (`info|warn|error`), `message`, `service`,
  `host` (`web-1`…`web-3`), `status` (HTTP code), `duration_ms`, `path`,
  `trace_id`.
- The story: `checkout-service` in `prod` emits a burst of `status=502`
  errors from `payments-api` timeouts. Searches for `status >= 500` return
  them, and `traces` shows the slow `payments-api` span.
- Services in `.traces`: `web-frontend`, `checkout-service`, `payments-api`.
  Trace id example: `4bf92f3577b34da6a3ce929d0e0e4736`.
- Monitors: `High 5xx rate on checkout`, `Payments latency p95`. Dashboards:
  `Checkout overview`. Users: `ada@example.com`, `grace@example.com`. Groups:
  `oncall`.
- Time is frozen: every log event and span falls between
  `2026-07-19 09:00 UTC` and `09:30 UTC`. The mock ignores the requested time
  range, so `--since 1h` always returns this data. These keep tables
  stable over time:
  - Resource `created`/`modified` metadata is dated 2026-04 to 2026-06, so
    tables print absolute dates.
  - `last_heartbeat_at`, `last_logins` and export `created_at` are set relative
    to the mock's clock, so tables print stable "2m ago" style ages. Their raw
    epoch values in JSON change on every run; leave them out of expected output.
  - Monitor events and notifications fall in the frozen window too. Their
    tables print absolute UTC times (`2026-07-19 09:21:30 UTC`), not ages.
- Search returns event fields flattened as `message_kvs.<field>`, as the real
  API does. `search --fields status` falls back to `message_kvs.status` and
  keeps the bare name as the output key, like `tail --fields` (which selects
  server-side). `--select`, `-g` and the WHERE clause take bare names too.
  `--jq` runs after `--fields`: alone it sees the whole flattened event
  (`--jq '."message_kvs.status"'`); with `--fields status` it sees only the
  output keys you asked for (`--jq .status`).
- `--jq` prints JSON, so strings keep their quotes. When an id feeds another
  command, add `-r` (`--raw-output`) to print it bare.
- Never stable: `ping` latency, the `updated HH:MM:SS` line of live
  aggregates, and `bronto version`.
- `-d` must be qualified (`prod/checkout-service`), because `checkout-service`
  exists in both collections.

## Internal links

Write internal links as absolute paths under `/cli` without a trailing
slash, for example `/cli/guides/searching` or
`/cli/guides/searching#filter-flags-instead-of-where`. `make docs-validate`
runs `mint broken-links --check-anchors`, which fails on broken links and
anchors.
