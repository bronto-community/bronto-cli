#!/usr/bin/env bash
# workflow-lint.sh — version-pin policy for the repo's privileged execution
# surfaces. GitHub Actions are already SHA-pinned repo-wide (StepSecurity),
# but three floating-version patterns survived that pass, and each runs in
# a context with credentials or release authority:
#
#   1. `go run <module>@latest` in a workflow or the Makefile — executes
#      whatever the module author last published (spec-sync's oasdiff runs
#      weekly with issues:write; the Makefile's goreleaser targets validate
#      a DIFFERENT goreleaser than the release workflow runs).
#   2. A floating `version: "~> vN"` for a tool action — the release
#      workflow's goreleaser downloads the latest v2.x at run time, inside
#      the most credential-dense job in the repo, while cosign two steps
#      earlier is exact-pinned for exactly this reason.
#   3. Docker FROM lines pinned to a mutable tag without a digest — the
#      published image is not reproducible and can change silently.
#
# A line may opt out with an inline marker and a reason:
#     ... # pin-lint:allow <why this one may float>
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0

scan() { # scan <extended-regex> <message> <files...>
  local re=$1 msg=$2
  shift 2
  local hits
  hits=$(grep -nE "$re" "$@" 2>/dev/null | grep -v 'pin-lint:allow' || true)
  if [ -n "$hits" ]; then
    printf 'workflow-lint: %s\n%s\n\n' "$msg" "$hits" >&2
    fail=1
  fi
}

scan '@latest' \
  'floating @latest module version in a privileged context — pin an exact version' \
  .github/workflows/*.yml Makefile

scan 'version:[[:space:]]*["'"'"']?~>' \
  'floating (~>) tool version in a workflow — pin an exact version' \
  .github/workflows/*.yml

scan '^FROM[[:space:]]+[^@[:space:]]+:[^@[:space:]]+([[:space:]]|$)' \
  'Docker base image pinned to a mutable tag without an @sha256 digest' \
  Dockerfile Dockerfile.distroless

if [ "$fail" -ne 0 ]; then
  echo "workflow-lint: FAILED — pin the versions above (or justify with '# pin-lint:allow <reason>')" >&2
  exit 1
fi
echo "workflow-lint: ok"
