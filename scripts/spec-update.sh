#!/usr/bin/env bash
# spec-update.sh — re-vendor api/openapi.yaml from the published Bronto
# OpenAPI spec and redo everything that follows mechanically from it
# (make spec-update; the spec-sync workflow runs it and opens a PR).
#
#   1. download the upstream spec whole over api/openapi.yaml, unmodified
#   2. record api/upstream.sha256 and api/vendored.sha256
#   3. go generate ./internal/api (span-name URL templates)
#   4. make docs-reference (command reference + API coverage page)
#
# Usage: spec-update.sh [upstream.yaml]
#   With a file argument that file is vendored instead of downloading
#   $SPEC_URL (spec-sync has already fetched it; also handy for trying a
#   spec offline).
#
# Exit status:
#   0  done; review the diff and run `go test ./...`
#   2  re-vendored, but the spec has operations the API coverage map
#      (internal/cli/apicoverage.go) doesn't know, or entries the spec no
#      longer has. They are printed; each needs a human decision (build a
#      command, or list it as not supported yet), then `make docs-reference`.
#      The script does not add placeholder entries: a placeholder would
#      turn TestAPICoverage green and publish "not supported yet" in the
#      docs without anyone having decided that.
#   1  anything else. The tree may be half-updated:
#      `git checkout -- api internal/api docs` to undo.
set -euo pipefail
cd "$(dirname "$0")/.."

# Keep the default in sync with SPEC_URL in .github/workflows/spec-sync.yml.
url=${SPEC_URL:-https://s3.eu-west-1.amazonaws.com/docs.brontobytes.io/redocusaurus/brontobytes.yaml}

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
if [ $# -gt 0 ]; then
  cp "$1" "$tmp"
else
  echo "spec-update: downloading $url"
  curl -fsSL -o "$tmp" "$url"
fi
upstream_sha=$( (sha256sum "$tmp" 2>/dev/null || shasum -a 256 "$tmp") | cut -d' ' -f1)

cp "$tmp" api/openapi.yaml
printf '%s vendored-%s\n' "$upstream_sha" "$(date -u +%Y-%m-%d)" > api/upstream.sha256
scripts/spec-verify.sh --record
go generate ./internal/api

if ! out=$(make --no-print-directory docs-reference 2>&1); then
  printf '%s\n' "$out" >&2
  # Matched in the shell, not through `| grep -q`: under pipefail a large
  # report makes grep exit early, printf die of SIGPIPE, and this exit 1.
  case "$out" in
    *'API coverage map'*)
      echo "spec-update: the spec is re-vendored. Left to do: decide each line above in internal/cli/apicoverage.go, then run 'make docs-reference' and 'go test ./...'." >&2
      exit 2
      ;;
  esac
  exit 1
fi
echo "spec-update: done (upstream sha $upstream_sha). Review 'git diff --stat', then run 'go test ./...'."
