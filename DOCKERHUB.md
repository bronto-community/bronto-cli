<!-- short-description: Community CLI for the Bronto observability platform: search logs, traces, manage resources -->
# bronto-cli

A command-line client for the [Bronto](https://bronto.io) observability platform. One binary wraps Bronto's REST and ingestion APIs: search and tail logs, explore OpenTelemetry traces, send events, and manage resources such as datasets and monitors. It's built for scripts and agents, with JSONL when piped, typed errors, stable exit codes, and `--dry-run` on every mutating call.

bronto-cli is an open-source project from Bronto, maintained as a **community artifact**: free to use and open to contributions, but not covered by Bronto's product support. Questions, bugs, and feature requests go to [GitHub issues](https://github.com/bronto-community/bronto-cli/issues).

## Usage

```sh
docker run --rm -e BRONTO_API_KEY -e BRONTO_REGION \
  brontoio/bronto-cli:latest search "status >= 500" --since 1h
```

The entrypoint is `/bronto`, so arguments go straight to the CLI. Neither image has a shell or a keychain, so pass credentials as environment variables. To feed a file into a command, pipe it to stdin with `docker run -i` and `--input -`.

## Tags

Every release publishes two images for `linux/amd64` and `linux/arm64`:

| Tag | Base |
| --- | --- |
| `latest`, `<version>` | `scratch` plus CA certificates |
| `latest-distroless`, `<version>-distroless` | distroless |

Pre-releases don't move `latest`. The same images, with the same digests, are on `ghcr.io/bronto-community/bronto-cli`. Both registries carry provenance and SBOM attestations.

## Documentation

- [Install](https://docs.bronto.io/cli/getting-started/install) and [CI/CD](https://docs.bronto.io/cli/guides/ci-cd), including the Docker images
- [Full documentation](https://docs.bronto.io/cli)
- [Source on GitHub](https://github.com/bronto-community/bronto-cli) (MIT license)
