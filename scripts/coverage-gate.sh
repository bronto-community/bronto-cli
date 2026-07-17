#!/usr/bin/env bash
# coverage-gate.sh enforces a coverage ratchet: the filtered total
# (coverage.filtered.txt, produced by scripts/coverage.sh) must not fall
# more than TOLERANCE percentage points below the committed baseline in
# .github/coverage-baseline.txt. A 0.3-point tolerance absorbs
# integration-timing branch jitter without letting real regressions
# through. When coverage climbs more than NUDGE points above the
# baseline, print an advisory nudge to ratchet the baseline up.
set -euo pipefail

# Force a locale with '.' as the decimal separator for awk's numeric
# formatting, regardless of the invoking shell's locale.
export LC_NUMERIC=C

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

baseline_file="${BASELINE_FILE:-$root/.github/coverage-baseline.txt}"
coverage_file="${COVERAGE_FILE:-$root/coverage.filtered.txt}"
tolerance="0.3"
nudge="1.0"

if [[ ! -f "$coverage_file" ]]; then
	echo "coverage-gate: $coverage_file not found; run scripts/coverage.sh first" >&2
	exit 1
fi

if [[ ! -f "$baseline_file" ]]; then
	echo "coverage-gate: $baseline_file not found" >&2
	exit 1
fi

baseline="$(tr -d '[:space:]' <"$baseline_file")"

total_line="$(go tool cover -func="$coverage_file" | tail -1)"
total="$(awk '{gsub("%","",$NF); print $NF}' <<<"$total_line")"

if [[ -z "$total" ]]; then
	echo "coverage-gate: could not parse total coverage from: $total_line" >&2
	exit 1
fi

echo "coverage-gate: baseline=${baseline}% current=${total}% (tolerance=${tolerance})"

awk -v total="$total" -v baseline="$baseline" -v tol="$tolerance" -v nudge="$nudge" 'BEGIN {
	min = baseline - tol
	if (total < min) {
		printf "coverage-gate: FAIL: current coverage %.1f%% is below baseline %.1f%% (tolerance %.1f%%)\n", total, baseline, tol > "/dev/stderr"
		printf "coverage-gate: if this drop is intentional, run `make coverage-baseline` and commit the updated .github/coverage-baseline.txt; otherwise add tests to close the gap\n" > "/dev/stderr"
		exit 1
	}
	if (total > baseline + nudge) {
		printf "coverage-gate: advisory: current coverage %.1f%% exceeds baseline %.1f%% by more than %.1f points; consider running `make coverage-baseline` to ratchet the baseline up\n", total, baseline, nudge
	}
	printf "coverage-gate: PASS\n"
}'
