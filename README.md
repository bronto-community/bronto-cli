# bronto-cli

[![CI](https://github.com/bronto-community/bronto-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/bronto-community/bronto-cli/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/bronto-community/bronto-cli)](https://github.com/bronto-community/bronto-cli/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/bronto-community/bronto-cli/badge)](https://scorecard.dev/viewer/?uri=github.com/bronto-community/bronto-cli)

A command-line client for the [Bronto](https://bronto.io) observability platform. One binary wraps Bronto's REST and ingestion APIs: search and tail logs, explore OpenTelemetry traces, send events, and manage resources such as datasets and monitors. It's built for scripts and agents, with JSONL when piped, typed errors, stable exit codes, and `--dry-run` on every mutating call.

bronto-cli is an open-source project from Bronto, maintained as a **community artifact**: free to use and open to contributions, but not covered by Bronto's product support. Questions, bugs, and feature requests go to [GitHub issues](https://github.com/bronto-community/bronto-cli/issues).

## Install

```sh
brew install --cask bronto-community/tap/bronto
```

or

```sh
curl -fsSL https://raw.githubusercontent.com/bronto-community/bronto-cli/main/scripts/install.sh | sh
```

The [install guide](https://bronto-cli.vercel.app/getting-started/install/) covers `go install`, Docker images, `.deb`/`.rpm` packages, release archives, and signature verification.

## Quickstart

```sh
bronto auth login          # paste a management API key; stored in the OS keychain
bronto ping                # check the connection
bronto datasets list       # see what data you have
bronto search "status >= 500" -d <dataset> --since 1h
bronto search "status >= 500" -d <dataset> --since 1h | jq .   # JSONL when piped
```

The [quickstart](https://bronto-cli.vercel.app/getting-started/quickstart/) walks through these steps in more detail.

## Documentation

The full documentation is at **https://bronto-cli.vercel.app**:

- [Authentication](https://bronto-cli.vercel.app/getting-started/authentication/): API keys, regions, profiles
- [Searching](https://bronto-cli.vercel.app/guides/searching/), [live tail](https://bronto-cli.vercel.app/guides/live-tail/), and [traces](https://bronto-cli.vercel.app/guides/traces/)
- [Managing resources](https://bronto-cli.vercel.app/guides/resources/): the shared `list | get | create | update | delete` pattern
- [Scripting and agents](https://bronto-cli.vercel.app/guides/scripting/) and [CI/CD](https://bronto-cli.vercel.app/guides/ci-cd/)
- [Configuration](https://bronto-cli.vercel.app/configuration/config-files/) and [troubleshooting](https://bronto-cli.vercel.app/troubleshooting/)
- [Command reference](https://bronto-cli.vercel.app/reference/commands/), generated from the CLI

For agents, [`skill.md`](./skill.md) is a short orientation and [`llms.txt`](./llms.txt) a 20-line summary. `bronto --help` and `bronto <command> --help` always match the installed binary. Platform concepts (datasets, the query language, monitors) are documented at [docs.bronto.io](https://docs.bronto.io).

## Contributing

```sh
git clone https://github.com/bronto-community/bronto-cli
cd bronto-cli
make build   # -> ./bronto
make test
```

See [CONTRIBUTING.md](./CONTRIBUTING.md) for the architecture map, test and lint expectations, and how to work on the docs.

## No telemetry

bronto-cli sends no telemetry, analytics, or usage data. The only network calls it makes are the ones you ask for, to the Bronto API and ingestion endpoints you've configured.

## License

MIT, see [LICENSE](./LICENSE).

The Bronto name and logo are trademarks of Bronto, used with permission and **not** covered by the MIT license. See [TRADEMARK.md](./TRADEMARK.md).
