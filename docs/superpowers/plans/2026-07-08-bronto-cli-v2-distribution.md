# bronto-cli v2 — Plan 6: Distribution, Docs & Release Readiness

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship-ready: goreleaser (binaries, brew tap config, deb/rpm, scratch+distroless containers, install script), release + spec-sync CI workflows, `skill.md` + `llms.txt`, a real README, CONTRIBUTING.md — plus Plan-5 deferred cleanups. After this plan the repo is releasable with `git tag v0.1.0`.

## Global Constraints

- Module `github.com/bronto-community/bronto-cli`; Go `1.25.0`; every commit: `go test ./...` green, `CGO_ENABLED=0 go build ./...`, gofmt clean, `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run` 0 issues. NO new runtime deps. No telemetry, ever.
- Version injection: goreleaser ldflags must target `github.com/bronto-community/bronto-cli/internal/version.{Version,Commit,Date}` (verify the exact var names in internal/version/version.go).
- Docs are tested where feasible: README/skill command examples that can run offline get testscript coverage (`--help` forms, exit codes); no rot.
- Conventional commits.

---

### Task 1: Plan-5 deferred cleanups

**Files:** `internal/secrets/secrets.go`, `internal/cli/search.go`, `internal/cli/ping.go`, `internal/output/output.go` (+ tests).

1. `secrets.Get`/`fileGet`: surface a corrupt credentials file as `config_parse_error` (typed) instead of `ErrNotFound` — align with Store/Delete; callers: NewApp's lookup treats parse errors as "no key" but WARNS on stderr once (unless quiet); `auth status` shows the parse error in its status cell. Tests: corrupt file → Get returns typed error; auth status row shows error; NewApp still functions (env key path unaffected).
2. `search.go`: drop the duplicate `output.DetectFormat` call in printEvents — use `app.DetectFormat(true)`.
3. `ping.go`: `api_unreachable` → `network_error` retryable (align the machine contract everywhere; update tests + any hint text).
4. `--fields` affordance consistency: `-o raw` with `--fields` → `usage_invalid_flags` (like --jq); waterfall/table custom renderers (traces show/shape TTY) with `--fields` → also reject with a hint to use a machine format. Tests for both.

Commit: `refactor: plan-5 deferred cleanups (typed secret errors, contract consistency)`.

---

### Task 2: goreleaser + packaging

**Files:** Create `.goreleaser.yaml`, `Dockerfile` (scratch, multi-stage), `Dockerfile.distroless`, `scripts/install.sh`, `Makefile` additions (`release-dry`, `snapshot`).

- `.goreleaser.yaml` (v2 schema): builds: darwin/linux/windows × amd64/arm64, `CGO_ENABLED=0`, `-trimpath`, ldflags `-s -w -X <module>/internal/version.Version={{.Version}} -X ...Commit={{.ShortCommit}} -X ...Date={{.Date}}`; archives (tar.gz, zip for windows) with LICENSE+README; checksums; changelog from conventional commits (groups feat/fix); nfpms (deb+rpm, maintainer bronto-cli contributors, completions packaged via post-generate hooks — generate completion files in a before hook: `./bronto completion bash > ...` etc.); brews section targeting a `bronto-community/homebrew-tap` repo (skip_upload: auto — harmless placeholder until the tap exists); release: github, draft: false, prerelease: auto.
- `Dockerfile`: multi-stage — golang:1.25-alpine builder (CGO_ENABLED=0, trimpath, same ldflags with a build ARG VERSION) → `FROM scratch` + `COPY --from=alpine /etc/ssl/certs/ca-certificates.crt` + binary; ENTRYPOINT ["/bronto"]. `Dockerfile.distroless`: same builder → `gcr.io/distroless/static`.
- `scripts/install.sh`: detect OS/arch, download latest GitHub release archive, verify checksum, install to /usr/local/bin (or $BINDIR), POSIX sh, `set -eu`.
- Verification (binding): `go run github.com/goreleaser/goreleaser/v2@latest check` passes on the config; `go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=publish` builds all targets locally (this is the test — run it; it's slow but definitive; if the network-restricted environment can't fetch goreleaser, `docker build .` at least must succeed if docker exists, else document what was verified); `sh -n scripts/install.sh` syntax check.

Commit: `feat: goreleaser packaging, container images, install script`.

---

### Task 3: Release + spec-sync CI workflows

**Files:** Create `.github/workflows/release.yml`, `.github/workflows/spec-sync.yml`; modify `.github/workflows/ci.yml` (add testscript txtar run already covered by go test — verify; add a docs-examples job if Task 5 adds one).

- `release.yml`: on push tags `v*`: setup-go (go.mod), `goreleaser/goreleaser-action@v6` with `version: '~> v2'`, `args: release --clean`, GITHUB_TOKEN permissions `contents: write`. Also docker build+push of both Dockerfiles to ghcr.io/bronto-community/bronto-cli (`:latest` + `:{{version}}` + `-distroless` variants) — `docker/login-action`, `docker/build-push-action`, permissions `packages: write`.
- `spec-sync.yml`: weekly cron + workflow_dispatch: fetch the latest spec (document the canonical URL as a TODO-marked env var since Bronto's public spec URL must be confirmed — default to a repo variable `SPEC_URL`, skip gracefully with a notice when unset), diff against `api/openapi.yaml`; when changed: run `go run github.com/oasdiff/oasdiff@latest breaking old new` capturing output, regenerate (`make generate`), `go test ./...`, and open a PR via `peter-evans/create-pull-request@v7` with the oasdiff summary in the body; conformance test failures surface in the PR's CI.
- Verification: `actionlint` if available via `go run github.com/rhysd/actionlint/cmd/actionlint@latest` (0 errors); otherwise YAML-parse both files in a Go test-free way (`python3 -c 'import yaml,sys; yaml.safe_load(open(sys.argv[1]))' f` or `go run` a yaml check) — document which ran.

Commit: `feat: release and spec-sync workflows`.

---

### Task 4: skill.md + llms.txt

**Files:** Create `skill.md`, `llms.txt`.

- `skill.md` (the agent-facing usage guide, kept SHORT — the CLI's help is the real reference): frontmatter name/description (`Use when querying Bronto logs/traces, sending events, or managing Bronto resources from the command line`), then: auth quickstart (env vars for CI/agents: BRONTO_API_KEY+BRONTO_REGION; `bronto auth login` for humans; `bronto auth token` for scripting), the six workhorse commands with one example each (search/tail/traces/send/fields/context), machine-output contract (piped=JSONL, `-o json`, `--jq`, `--fields`, typed error envelope + exit-code table 0-5), resource commands pattern (`<resource> list|get|create|update|delete`, `-f k=v`, `--yes`), `bronto api` escape hatch, exports workflow, plugins note (env-key inheritance; keychain NOT inherited — use `bronto auth token`). Every stated flag/command MUST exist — verify against `./bronto <cmd> --help` while writing.
- `llms.txt`: 20-line summary + pointer to skill.md and `--help`.
- Verification: a `cmd/bronto/testdata/script/skilldoc.txtar` sanity test? Better (binding): a Go test `internal/cli/skilldoc_test.go` that parses skill.md for `bronto <token>` code-span occurrences and asserts each first token after "bronto" is a registered command or flag on the root (guards doc rot mechanically).

Commit: `docs: agent skill file and llms.txt`.

---

### Task 5: README + CONTRIBUTING

**Files:** Rewrite `README.md`; create `CONTRIBUTING.md`.

- README: what it is (community CLI for Bronto), install (brew tap placeholder, install.sh curl one-liner, go install, docker run ghcr images, releases page), quickstart (auth login → search → tail), the command tour (grouped: explore/search/tail/traces; manage/resources+exports+usage; pipe/send; ops/auth+config+plugins), scripting & agents section (JSONL/jq/fields/exit codes + link skill.md), configuration (profiles, precedence table, env vars incl. BRONTO_TIMEOUT/BRONTO_INGEST_URL/BRONTO_CONFIG_DIR), restricted environments (two env vars, scratch image), plugins (bronto-* on PATH, auth note), development (clone/build/test — no generation tooling needed), no-telemetry statement, license.
- CONTRIBUTING.md: 5-minute path, architecture map (one paragraph per package), how to add a resource command (registry entry + conformance test), TDD/lint expectations, conventional commits, regeneration (`make generate`/`check-generate`).
- Verification: every ``` `bronto …` ``` example in README must pass the same mechanical token check as skill.md (extend the Task 4 test to include README.md).

Commit: `docs: README and CONTRIBUTING for release readiness`.

---

### Task 6: End-of-plan verification + release dry-run

- Full gauntlet; `git tag -d`-safe snapshot: `go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=publish` → dist/ contains all platform archives; `./dist/bronto_*/bronto --version` (linux binary won't run on darwin — run the darwin one) reports the snapshot version (ldflags wiring proof); `docker build .` if docker available.
- Whole-branch review (controller), fix wave, merge. Then the controller tags nothing — tagging is the user's call.
