#!/usr/bin/env bash
# mutation.sh — advisory mutation testing (gremlins) over the core-logic
# packages. This is NOT a gate; it complements the coverage ratchet by
# estimating whether the tests would CATCH a bug, not merely run the code
# (see CONTRIBUTING). Settings come from .gremlins.yaml (single worker for
# determinism, generous timeout).
#
# Skipped on purpose:
#   - timerange, query        fuzz targets slow every per-mutant test run
#   - cli                     ~3.6k stmts of mostly command wiring; too slow
#                             for a signal, and covered by golden/txtar tests
#   - coerce, version, cichecks, cmd/bronto, tools  trivial or entry points
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

version="v0.6.0"
bindir="$(mktemp -d)"
trap 'rm -rf "$bindir"' EXIT
echo "==> installing gremlins ${version}"
GOBIN="$bindir" go install "github.com/go-gremlins/gremlins/cmd/gremlins@${version}"

packages=(output traces bronto patterns api config secrets ingest clierr)

rc=0
for p in "${packages[@]}"; do
  printf '\n==> internal/%s\n' "$p"
  # gremlins gathers coverage via `go test -cover`; a WARM test cache makes
  # Go return the cached result without re-instrumenting, so gremlins sees
  # zero coverage and reports a bogus 0%. Clear it before each package.
  go clean -testcache
  "$bindir/gremlins" unleash "./internal/${p}/" || rc=1
done

echo
echo "mutation.sh: done (advisory — a low score is a nudge to tighten assertions, not a failure)"
exit "$rc"
