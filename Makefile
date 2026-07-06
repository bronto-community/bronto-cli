.PHONY: build test lint generate check-generate

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
