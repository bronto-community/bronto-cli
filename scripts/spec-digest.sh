#!/usr/bin/env bash
# spec-digest.sh — turn oasdiff JSON output into a readable markdown digest
# answering "how has the upstream API spec changed?": an inventory of
# added/removed/modified endpoints and schemas, plus breaking changes
# grouped by rule (the raw `oasdiff breaking` text repeats boilerplate like
# "removed required property message from the 4xx response" once per
# endpoint — hundreds of lines that drown the real story).
#
# Usage: spec-digest.sh <diff.json> <breaking.json> <out.md> [cli-endpoints.json]
#   diff.json:          `oasdiff diff <base> <upstream> -f json`
#   breaking.json:      `oasdiff breaking <base> <upstream> -f json`
#   cli-endpoints.json: `go run ./internal/tools/endpointmap` — when given,
#                       a "CLI impact" section classifies added/removed/
#                       modified endpoints by which bronto command (if any)
#                       calls them.
# Missing/empty/invalid inputs degrade to a note rather than failing, so a
# partial digest still gets published.
set -euo pipefail

diff_json=$1
breaking_json=$2
out=$3
cli_json=${4:-}

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
  # --- CLI impact: the section maintainers act on. Classifies every
  # added/removed/modified endpoint by whether a bronto command calls it
  # (param names normalized: {monitorId} == {*} == {}; keep the `norm`
  # helper in sync with normalizeParams in resourcespec_test.go).
  if [ -n "$cli_json" ] && jq -e 'type == "array"' "$cli_json" >/dev/null 2>&1 \
      && jq -e . "$diff_json" >/dev/null 2>&1; then
    printf '#### CLI impact\n\n'
    jq -r --slurpfile pats "$cli_json" '
      def norm: gsub("\\{[^}]*\\}"; "{}");
      ($pats[0] | map({p: (.pattern | norm), c: .command})) as $P |
      def cmds($x): [$P[] | select(.p == ($x | norm)) | .c] | unique | join(", ");
      (.paths.added // [] | sort) as $add |
      (.paths.deleted // [] | sort) as $del |
      (.paths.modified // {} | keys | sort) as $mod |
      ($add | map(select(cmds(.) == ""))) as $newUncov |
      ($del | map({path: ., c: cmds(.)} | select(.c != ""))) as $delCov |
      ($mod | map({path: ., c: cmds(.)} | select(.c != ""))) as $modCov |
      (if ($delCov | length) > 0 then
        "**⚠ Removed from the spec but used by the CLI (\($delCov | length))** — verify these still work live (integration suite) before re-vendoring; if one 404s, drop the command:\n\n"
        + ($delCov | map("- `\(.path)` — \(.c)") | join("\n")) + "\n"
      else "No removed endpoint is used by the CLI.\n" end),
      (if ($newUncov | length) > 0 then
        "\n**New endpoints with no CLI coverage (\($newUncov | length))** — candidates for new commands; reachable today via `bronto api <method> <path>`:\n\n"
        + ($newUncov | map("- `\(.)`") | join("\n")) + "\n"
      else "" end),
      (if ($modCov | length) > 0 then
        "\n**Modified endpoints backing existing commands (\($modCov | length))** — re-check request/response shapes:\n\n"
        + ($modCov | map("- `\(.path)` — \(.c)") | join("\n")) + "\n"
      else "" end)
    ' "$diff_json"
    printf '\n'
  elif [ -n "$cli_json" ]; then
    printf '#### CLI impact\n\n_endpointmap or oasdiff produced no usable JSON; see the workflow run artifacts/logs._\n\n'
  fi

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
