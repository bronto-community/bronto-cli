# bronto CLI docs

The bronto CLI documentation, written for [Mintlify](https://mintlify.com).
A Mintlify dev site deploys this directory from `main`. Releases are copied
into the official docs at https://docs.bronto.io/cli with
`scripts/sync-official-docs.sh`.

Read [AGENTS.md](./AGENTS.md) before you write or change pages. It covers the
layout, who owns which files, components, code block conventions (tested
`console` snippets), VHS recordings, and the docs mock world.

## Local development

Requires the Node version in [`.nvmrc`](./.nvmrc).

```sh
make docs-dev       # local preview on http://localhost:3000
make docs-validate  # strict build + link and anchor check
make docs-check     # everything CI runs for docs
```

## Layout

| Path | Purpose |
| --- | --- |
| `docs.json` | Dev-site config: theme, logos, and the CLI tab via `"$ref"` |
| `cli/` | The pages, recordings, and `navigation.json`. This folder is what gets copied to docs.bronto.io. |
| `.mintignore` | Keeps the tooling below out of the site |
| `tapes/`, `testdata/`, `mk/` | VHS sources and goldens, docs mock fixtures, make fragments |

## Publishing to docs.bronto.io

The official docs `docs.json` references `cli/navigation.json` once. For each
release, from a local checkout of the official docs:

```sh
scripts/sync-official-docs.sh --ref v1.2.3 <path-to-official-docs-checkout>
```

The script replaces `cli/` there and prints a commit message. Commit, open a
pull request in that repository, and check its Mintlify preview.
