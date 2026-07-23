# Contributing to bronto-cli

## 5-minute path

```sh
git clone https://github.com/bronto-community/bronto-cli
cd bronto-cli
make build   # -> ./bronto (CGO_ENABLED=0 go build -o bronto ./cmd/bronto)
make test    # go test ./...
make lint    # golangci-lint run
./bronto --help
```

No code generation, network access, or Bronto account is needed to build and run the test suite — `internal/*_test.go` files exercise a fake `httptest.Server` rather than the real API.

## Architecture map

- **`internal/cli`** — the Cobra command tree (`root.go` builds it; one file per command or command family). This is where flag parsing, plugin-exec dispatch (`bronto-<name>` on `PATH`), and wiring between config/auth/output happen. Most new features touch this package.
- **`internal/bronto`** — the typed client for the two things almost every command needs: running a search (`search.go`) and polling for new events (`tail.go`), built on top of `internal/api`'s generated transport.
- **`internal/traces`** — the trace explorer: span model, field literals, and the aggregation/waterfall/shape algorithms that turn raw `.traces`-logset search results into `traces show|list|services|operations|aggregate|shape` output. Field literals and formulas intentionally match the v1 CLI exactly (see `docs/superpowers/specs/2026-07-07-v1-traces-extraction.md`).
- **`internal/ingest`** — sends events to Bronto's ingestion host, which is a separate host from the REST API: NDJSON request bodies, routing headers, optional gzip. Backs `bronto send`.
- **`internal/secrets`** — stores API keys in the OS keychain (macOS Keychain, Linux Secret Service, Windows Credential Manager) with a `0600` credentials-file fallback for headless environments. Backs `bronto auth login|logout|token|status`.
- **`internal/config`** — resolves configuration with precedence flags > env > project `.bronto.toml` > user config > defaults, tracking the source of every value so `bronto config list` can show its provenance. `api_key` is deliberately excluded from both file formats; secrets only ever come from the keychain or `BRONTO_API_KEY`. Host-naming keys (`base_url`, `ingest_url`) are further restricted to trusted sources — `projectFileKeys` omits them so a discovered `.bronto.toml` can't redirect where the key is sent — and `region` is validated as a slug for the same reason (`validateRegion`). See `config_security_test.go`.
- **`internal/output`** — the single output engine used by every command: format detection (table on a TTY, JSONL when piped, or an explicit `-o`), the `--fields` column/key filter, and `--jq` (gojq) post-processing. If a command needs to print anything, it goes through this package rather than rolling its own formatting.
- **`internal/timerange`** — converts the CLI's relative/absolute time flags (`--since`, `--from`/`--to`) into the search API's mutually-exclusive `time_range` string or `from_ts`/`to_ts` unix-millisecond bounds.
- **`internal/clierr`** — typed errors: stable machine-readable codes, human-facing hints, and the exit-code contract (0 success, 1 unexpected, 2 usage/config, 3 auth, 4 not found, 5 retryable). Every user-facing error in the codebase should be a `*clierr.Error`, not a bare `fmt.Errorf`.
- **`internal/api`** — the hand-written HTTP layer: the retrying auth transport (`transport.go`), `--debug` tracing (`debug.go`), and status-to-typed-error mapping. The vendored OpenAPI spec (`api/openapi.yaml`) is a conformance/drift reference for `resourcespec_test.go` and spec-sync, not a codegen source.

## Adding a resource command

Most Bronto management resources (`datasets`, `monitors`, `dashboards`, `parsers`, `exports`, `api-keys`, `saved-searches`) share one shape: `list | get <id> | create | update <id> | delete <id>`. Rather than hand-writing five near-identical commands per resource, `internal/cli/resources.go` has a single generic factory driven by a descriptor registry.

To add a new one:

1. Add an entry to `resourceRegistry` in `internal/cli/resources.go` — a `resourceDesc{Name, Base, ...}` giving the subcommand name and its collection path (e.g. `/monitors`), plus any overrides (`IDBase`, `CreatePath`, `UpdateMethod`, `Columns`, `NoCreate`/`NoUpdate`/`NoDelete`/`NoGet` for partial resources).
2. Run `go test ./internal/cli/...` — `resourcespec_test.go` parses `api/openapi.yaml` and asserts your descriptor's `Base`/`CreatePath`/`IDBase` correspond to real paths in the vendored spec. A typo or a stale endpoint fails the build instead of silently 404ing at runtime. If your resource genuinely deviates from the vendored spec snapshot (a real, documented endpoint the spec doesn't capture), add it to `specCreatePathExceptions` with a comment explaining why.
3. Add a short registration test alongside `resources_test.go` if the resource has any non-default behavior (custom columns, disabled verbs).
4. Add the new resource's name to `skill.md`'s resource list and the README command tour — `TestSkillDocCoversAllCommands` fails the build if a registered command is absent from `skill.md`, so agents always learn about new commands. (An earlier "only document workhorse commands" policy let eleven resources ship undocumented; the test now prevents that.)

## TDD and lint expectations

Write the failing test first. Every package in `internal/` has a corresponding `_test.go` file exercising it against fakes (`httptest.Server` for HTTP, an injectable `Getenv`/`WorkDir` for config, etc.) — no test in this repo talks to the real Bronto API. Match that pattern for new code: red test, minimal implementation, green, then simplify.

Run `make lint` (config in `.golangci.yml`) before opening a PR — CI enforces it.

## Conventional commits

Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org/): `feat:`, `fix:`, `test:`, `docs:`, `chore:`, `refactor:`, etc., e.g. `feat: config resolution with precedence, source tracking, profiles`. The release changelog (`.goreleaser.yaml`) groups `feat:`/`fix:` commits into their own sections and excludes `docs:`/`test:`/`chore:` entirely, so an inaccurate prefix will misfile (or hide) your change in release notes.

## The vendored spec

`api/openapi.yaml` is a vendored snapshot of Bronto's published OpenAPI spec (`api/upstream.sha256` records which). It is a *reference*, not a codegen source: `resourcespec_test.go` asserts every registry path still exists in it, and the weekly spec-sync workflow diffs it against upstream and files a CLI-impact digest. To re-vendor, follow the checklist spec-sync puts in its drift issue.

`make check-spec` (CI: `repo-gates`) guards the vendored spec: `api/openapi.yaml` must match the digest recorded in `api/vendored.sha256`. Any intentional spec change (re-vendor or patch) must run `make spec-baseline` and commit the updated record in the same PR — that diff is the reviewable governance step, same pattern as the coverage baseline. `make lint-workflows` (same CI job) enforces exact version pins in workflows, the Makefile's tool invocations, and Dockerfile base images.

**Required status checks.** `.github/required-status-checks.txt` mirrors the branch-protection ruleset's required checks, and the `cichecks` tripwire (in the `test` job) fails if any entry names a job that `.github/workflows/ci.yml` no longer produces. When you rename or remove a CI job that is a required check — or change which checks the ruleset requires — update **both** the ruleset and this file in the same change. This is what prevents a renamed job from silently wedging every PR on a required context that never reports (as `generate-clean` → `repo-gates` once did).
