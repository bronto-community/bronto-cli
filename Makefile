.PHONY: build test lint check-spec spec-baseline lint-workflows check-actions release-dry snapshot coverage coverage-baseline it vuln

build:
	CGO_ENABLED=0 go build -o bronto ./cmd/bronto

test:
	go test ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run

# check-spec verifies api/openapi.yaml against the digest recorded in
# api/vendored.sha256 (--self-test first proves the gate can go red). This
# is the real spec-integrity gate; see scripts/spec-verify.sh for why
# check-generate could never fail.
check-spec:
	scripts/spec-verify.sh --self-test

# spec-baseline records the current vendored spec digest. Run it (and
# commit the diff) whenever api/openapi.yaml changes on purpose — the
# reviewable governance step, same pattern as coverage-baseline.
spec-baseline:
	scripts/spec-verify.sh --record

# lint-workflows enforces the version-pin policy on CI/release surfaces
# (workflows, Makefile tool invocations, Dockerfiles).
lint-workflows:
	scripts/workflow-lint.sh

# check-actions runs the two GitHub Actions analyzers: actionlint
# (correctness — bad expressions, shellcheck inside run: blocks) and zizmor
# (security — credential persistence, cache poisoning, template injection,
# excessive permissions). actionlint is a pinned Go tool; zizmor is a Rust
# tool developers install once (pipx install zizmor==1.5.2, or see
# https://docs.zizmor.sh). CI's repo-gates job installs the same pinned
# zizmor before running this.
check-actions:
	go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7
	@command -v zizmor >/dev/null 2>&1 || { echo "zizmor not installed: pipx install zizmor==1.5.2 (https://docs.zizmor.sh)"; exit 1; }
	zizmor .github/workflows/

# coverage runs the full coverage pipeline (unit tests + optional
# integration leg + covdata merge + filtering) and enforces the ratchet
# gate against .github/coverage-baseline.txt.
#
# BRONTO_IT_MGMT_KEY is explicitly neutralized (set to empty) here, even if
# it's exported in the invoking shell: scripts/coverage.sh only runs the
# integration leg when that variable is non-empty, and CI's `coverage` job
# (.github/workflows/ci.yml) never has live credentials. If a developer ran
# this locally with BRONTO_IT_MGMT_KEY set for unrelated reasons (e.g. also
# running `make it`), the gate must still compare against the same
# unit-only number CI reproduces — not a higher, integration-inflated one
# that would then fail in CI. Run `BRONTO_IT_MGMT_KEY=... scripts/coverage.sh`
# directly to include the integration leg on purpose.
coverage:
	BRONTO_IT_MGMT_KEY= scripts/coverage.sh
	scripts/coverage-gate.sh

# coverage-baseline recomputes the filtered total and writes it into
# .github/coverage-baseline.txt. Commit the diff as the reviewable
# governance step when intentionally raising or lowering the bar.
#
# Same BRONTO_IT_MGMT_KEY neutralization as `coverage`, and for the same
# reason: the committed baseline must always be the unit-only number CI
# reproduces on every PR, never a number inflated by whatever integration
# leg happened to run on the machine that computed it.
coverage-baseline:
	BRONTO_IT_MGMT_KEY= scripts/coverage.sh
	go tool cover -func=coverage.filtered.txt | tail -1 | awk '{gsub("%","",$$NF); print $$NF}' > .github/coverage-baseline.txt
	@echo "NOTE: computed with BRONTO_IT_MGMT_KEY unset (unit-only) — this is the number CI's coverage job reproduces on every PR, never live-account-dependent."
	@echo "baseline updated to $$(cat .github/coverage-baseline.txt)%"

# it runs the black-box integration suite. Without BRONTO_IT_MGMT_KEY set,
# every credentialed test skips itself visibly (and the hermetic checks —
# binary builds, --help — still run). With it set, it runs live against a
# Bronto test account (see integration/).
it:
	go test -count=1 -timeout 20m ./integration/

# release-dry validates the goreleaser config without building anything.
# Pinned to the same version release.yml runs, so what's validated locally
# is what the release executes (lint-workflows rejects floating versions).
release-dry:
	go run github.com/goreleaser/goreleaser/v2@v2.17.0 check

# snapshot runs a full local release build (all platforms) into dist/
# without publishing anything, for verifying packaging end-to-end.
snapshot:
	go run github.com/goreleaser/goreleaser/v2@v2.17.0 release --snapshot --clean --skip=publish

# vuln scans all packages against the Go vulnerability database.
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
