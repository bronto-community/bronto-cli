#!/usr/bin/env bash
# spec-digest.sh — turn oasdiff JSON output into a readable markdown digest
# answering "how has the upstream API spec changed?": an inventory of
# added/removed/modified endpoints and schemas, plus breaking changes
# grouped by rule (the raw `oasdiff breaking` text repeats boilerplate like
# "removed required property message from the 4xx response" once per
# endpoint — hundreds of lines that drown the real story).
#
# Usage: spec-digest.sh <diff.json> <breaking.json> <out.md>
#   diff.json:     `oasdiff diff <base> <upstream> -f json`
#   breaking.json: `oasdiff breaking <base> <upstream> -f json`
# Missing/empty/invalid inputs degrade to a note rather than failing, so a
# partial digest still gets published.
set -euo pipefail

diff_json=$1
breaking_json=$2
out=$3

# list <file> <jq-expr> [cap] — newline list as markdown bullets, capped.
list() {
  local file=$1 expr=$2 cap=${3:-100}
  jq -r "$expr // [] | .[]" "$file" 2>/dev/null | LC_ALL=C sort | awk -v cap="$cap" '
    NR <= cap { print "- `" $0 "`" }
    END { if (NR > cap) printf "- … and %d more\n", NR - cap }'
}

count() {
  jq -r "$1 // [] | length" "$2" 2>/dev/null || echo 0
}

{
  if jq -e . "$diff_json" >/dev/null 2>&1; then
    pa=$(count '.paths.added' "$diff_json")
    pd=$(count '.paths.deleted' "$diff_json")
    pm=$(jq -r '.paths.modified // {} | length' "$diff_json")
    sa=$(count '.components.schemas.added' "$diff_json")
    sd=$(count '.components.schemas.deleted' "$diff_json")
    sm=$(jq -r '.components.schemas.modified // {} | length' "$diff_json")

    printf '#### How the spec changed\n\n'
    printf -- '- Endpoints: **%s added · %s removed · %s modified**\n' "$pa" "$pd" "$pm"
    printf -- '- Schemas: **%s added · %s removed · %s modified**\n\n' "$sa" "$sd" "$sm"
    printf '> A path "removed" here means removed from the *published spec*; the live\n'
    printf '> endpoint may still work (the published spec is documentation, not ground\n'
    printf '> truth). Check the integration suite before dropping CLI commands.\n\n'

    if [ "$pa" -gt 0 ]; then
      printf '<details><summary>Endpoints added (%s)</summary>\n\n' "$pa"
      list "$diff_json" '.paths.added'
      printf '\n</details>\n\n'
    fi
    if [ "$pd" -gt 0 ]; then
      printf '<details><summary>Endpoints removed from the spec (%s)</summary>\n\n' "$pd"
      list "$diff_json" '.paths.deleted'
      printf '\n</details>\n\n'
    fi
    if [ "$pm" -gt 0 ]; then
      printf '<details><summary>Endpoints modified (%s)</summary>\n\n' "$pm"
      list "$diff_json" '.paths.modified | keys'
      printf '\n</details>\n\n'
    fi
    if [ "$sa" -gt 0 ] || [ "$sd" -gt 0 ] || [ "$sm" -gt 0 ]; then
      printf '<details><summary>Schemas: %s added, %s removed, %s modified</summary>\n\n' "$sa" "$sd" "$sm"
      printf '**Added:**\n\n'
      list "$diff_json" '.components.schemas.added' 60
      printf '\n**Removed:**\n\n'
      list "$diff_json" '.components.schemas.deleted' 60
      printf '\n**Modified:**\n\n'
      list "$diff_json" '.components.schemas.modified | keys' 60
      printf '\n</details>\n\n'
    fi
  else
    printf '#### How the spec changed\n\n_oasdiff diff produced no usable JSON; see the workflow run artifacts/logs._\n\n'
  fi

  if jq -e 'type == "array"' "$breaking_json" >/dev/null 2>&1; then
    total=$(jq 'length' "$breaking_json")
    errors=$(jq '[.[] | select(.level == 3)] | length' "$breaking_json")
    warnings=$(jq '[.[] | select(.level == 2)] | length' "$breaking_json")
    printf '#### Breaking changes: %s (%s error, %s warning), grouped by rule\n\n' "$total" "$errors" "$warnings"
    # One bullet per rule: count, severity, description, and up to 6
    # affected operations (unique, sorted for determinism).
    jq -r '
      group_by(.id) | sort_by(-length) | .[] |
      (.[0].level | if . == 3 then "error" elif . == 2 then "warning" else "info" end) as $lvl |
      ([.[] | "\(.operation) \(.path)"] | unique | sort) as $ops |
      ($ops[:6] | map("`" + . + "`") | join(", ")) as $shown |
      (if ($ops | length) > 6 then " … +\(($ops | length) - 6) more" else "" end) as $more |
      "- **\(.[0].id)** ×\(length) (\($lvl)) — \(.[0].text)\n  \($shown)\($more)"
    ' "$breaking_json"
    printf '\n'
  else
    printf '#### Breaking changes\n\n_oasdiff breaking produced no usable JSON; see the workflow run artifacts/logs._\n\n'
  fi
} > "$out"
