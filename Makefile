.PHONY: build test lint generate check-generate release-dry snapshot

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

# release-dry validates the goreleaser config without building anything.
release-dry:
	go run github.com/goreleaser/goreleaser/v2@latest check

# snapshot runs a full local release build (all platforms) into dist/
# without publishing anything, for verifying packaging end-to-end.
snapshot:
	go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=publish
