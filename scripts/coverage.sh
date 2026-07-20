#!/usr/bin/env bash
# coverage.sh runs the full coverage pipeline for bronto-cli:
#   1. unit tests with -race, instrumented for coverage (covdata binary format)
#   2. an instrumented build of the CLI, used by the optional integration leg
#   3. the integration leg (integration/), which only runs when
#      BRONTO_IT_MGMT_KEY is set AND the integration/ package exists
#   4. merge of all covdata directories into a single text profile
#   5. filtering of the integration harness out of the profile
#   6. a filtered total + an HTML report
#
# Pinned pitfalls (do not "simplify" these away):
#   - -covermode=atomic on BOTH the test run and the build: -race forces
#     atomic counters, and go build -cover defaults to "set" mode, which
#     cannot be merged with atomic-mode covdata.
#   - never hand-merge text coverage profiles; stay in covdata binary
#     format across legs and only convert to text once, at the very end,
#     via `go tool covdata textfmt`.
#   - -count=1 is mandatory on every `go test` invocation here: cached
#     test results emit no covdata at all.
#   - -coverpkg=./... on both the unit test run and the instrumented
#     build, so both legs share an identical denominator (every package
#     is eligible to be covered by either leg).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

module="github.com/bronto-community/bronto-cli"

covdata_dir="$root/covdata"
unit_dir="$covdata_dir/unit"
int_dir="$covdata_dir/int"
bin="$root/bronto.itest"
raw_profile="$root/coverage.txt"
filtered_profile="$root/coverage.filtered.txt"
html_report="$root/coverage.html"

rm -rf "$covdata_dir" "$bin" "$raw_profile" "$filtered_profile" "$html_report"
mkdir -p "$unit_dir" "$int_dir"

echo "==> unit tests (-race, -covermode=atomic, -coverpkg=./...)"
go test -race -count=1 -covermode=atomic -coverpkg=./... ./... \
	-args -test.gocoverdir="$unit_dir"

echo "==> building instrumented binary (-cover, -covermode=atomic, -coverpkg=./...)"
go build -cover -covermode=atomic -coverpkg=./... -o "$bin" ./cmd/bronto

ran_integration=0
if [[ -n "${BRONTO_IT_MGMT_KEY:-}" ]]; then
	if [[ -d "$root/integration" ]]; then
		echo "==> integration tests (BRONTO_IT_MGMT_KEY set, integration/ present)"
		BRONTO_IT_BIN="$bin" GOCOVERDIR="$int_dir" go test -count=1 ./integration/
		ran_integration=1
	else
		echo "==> BRONTO_IT_MGMT_KEY is set but integration/ does not exist yet; skipping integration leg"
	fi
else
	echo "==> BRONTO_IT_MGMT_KEY not set; skipping integration leg"
fi

covdata_inputs="$unit_dir"
if [[ "$ran_integration" -eq 1 ]] && compgen -G "$int_dir"/covmeta.* >/dev/null; then
	covdata_inputs="$unit_dir,$int_dir"
fi

echo "==> merging covdata ($covdata_inputs) -> coverage.txt"
go tool covdata textfmt -i="$covdata_inputs" -o "$raw_profile"

echo "==> filtering generated code and test infrastructure -> coverage.filtered.txt"
# Excluded from accounting: the
# integration/ package itself (test harness, not product code — its live
# paths only execute with credentials and would dilute the ratchet).
{
	head -n1 "$raw_profile"
	grep -v -e "^${module}/integration/" "$raw_profile" | tail -n +2
} >"$filtered_profile"

echo "==> writing HTML report -> coverage.html"
go tool cover -html="$filtered_profile" -o "$html_report"

echo "==> filtered total coverage:"
go tool cover -func="$filtered_profile" | tail -1
