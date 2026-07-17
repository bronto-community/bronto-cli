.PHONY: build test lint generate check-generate release-dry snapshot coverage coverage-baseline it

build:
	CGO_ENABLED=0 go build -o bronto ./cmd/bronto

test:
	go test ./...

lint:
	golangci-lint run

generate:
	go generate ./...

check-generate: generate
	git diff --exit-code -- internal/api api

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
	go test -count=1 ./integration/

# release-dry validates the goreleaser config without building anything.
release-dry:
	go run github.com/goreleaser/goreleaser/v2@latest check

# snapshot runs a full local release build (all platforms) into dist/
# without publishing anything, for verifying packaging end-to-end.
snapshot:
	go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=publish
