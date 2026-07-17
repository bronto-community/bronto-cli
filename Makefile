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
coverage:
	scripts/coverage.sh
	scripts/coverage-gate.sh

# coverage-baseline recomputes the filtered total and writes it into
# .github/coverage-baseline.txt. Commit the diff as the reviewable
# governance step when intentionally raising or lowering the bar.
coverage-baseline:
	scripts/coverage.sh
	go tool cover -func=coverage.filtered.txt | tail -1 | awk '{gsub("%","",$$NF); print $$NF}' > .github/coverage-baseline.txt
	@echo "baseline updated to $$(cat .github/coverage-baseline.txt)%"

# it runs the black-box integration suite against a live Bronto test
# account (see integration/, added in a follow-up task). Requires
# BRONTO_IT_MGMT_KEY (and friends) to be set.
it:
	@if [ -d integration ]; then \
		go test -count=1 ./integration/; \
	else \
		echo "integration/ does not exist yet (coverage overhaul task 3); nothing to run"; \
	fi

# release-dry validates the goreleaser config without building anything.
release-dry:
	go run github.com/goreleaser/goreleaser/v2@latest check

# snapshot runs a full local release build (all platforms) into dist/
# without publishing anything, for verifying packaging end-to-end.
snapshot:
	go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=publish
