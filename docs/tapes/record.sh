#!/usr/bin/env bash
# record.sh renders the docs' VHS tapes (docs/tapes/*.tape).
#
#   docs/tapes/record.sh [--check] [tape ...]      (default: every tape)
#
# DOCS_TARGET=mock (default) runs the tapes against the deterministic docs
# mock server (internal/tools/docsmock) and writes:
#   docs/public/tapes/<name>.webm   the video the docs embed
#   docs/public/tapes/<name>.png    a poster frame (the tape's Screenshot)
#   docs/tapes/golden/<name>.txt    the settled screens as text (see
#                                   internal/tools/tapegolden)
#
# --check renders into a temp dir instead and only compares the text
# goldens: it changes nothing in the tree and exits 1 with a diff when a
# golden is stale. Mock mode only.
#
# DOCS_TARGET=live records against a real Bronto account instead — the CI
# test environment (BRONTO_IT_MGMT_KEY, optional BRONTO_IT_INGEST_KEY,
# BRONTO_IT_REGION, default eu). It writes videos and posters only; the
# goldens stay mock-based. Live limitations:
#   - The account has none of the mock world's data, so record.sh first
#     sends the fixture log events (re-timestamped to the last 30 minutes)
#     into the same collection/dataset names (prod/checkout-service, ...).
#     Set DOCS_LIVE_SEED=0 to skip that. Ingestion takes a while to become
#     searchable; DOCS_LIVE_SEED_WAIT (default 90 seconds) is the pause.
#   - Only DOCS_LIVE_TAPES are recorded (default: quickstart search tail
#     scripting). traces needs OTLP spans the CLI cannot send, and
#     resources would show the account's real users and monitors.
#   - tail gets live traffic from a background sender while it records.
#
# Requires vhs (https://github.com/charmbracelet/vhs) with ttyd and ffmpeg
# on PATH, and Go to build bronto and the mock.
set -euo pipefail

repo=$(cd "$(dirname "$0")/../.." && pwd)
tapes_dir="$repo/docs/tapes"
out_dir="$repo/docs/public/tapes"
golden_dir="$tapes_dir/golden"
target=${DOCS_TARGET:-mock}
check=0

args=()
for a in "$@"; do
  case "$a" in
    --check) check=1 ;;
    -h|--help) sed -n '2,32p' "$0"; exit 0 ;;
    *) args+=("$a") ;;
  esac
done

for tool in vhs ttyd ffmpeg; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "record.sh: $tool not found on PATH." >&2
    echo "  macOS: brew install vhs   (pulls in ttyd and ffmpeg)" >&2
    echo "  Linux: see https://github.com/charmbracelet/vhs#installation (vhs, ttyd, ffmpeg)" >&2
    exit 1
  fi
done

case "$target" in
  mock) ;;
  live)
    if [ "$check" = 1 ]; then
      echo "record.sh: --check compares the mock-mode goldens; it cannot run with DOCS_TARGET=live" >&2
      exit 2
    fi
    if [ -z "${BRONTO_IT_MGMT_KEY:-}" ]; then
      echo "record.sh: DOCS_TARGET=live needs BRONTO_IT_MGMT_KEY (the CI test account's management key)" >&2
      exit 2
    fi
    ;;
  *) echo "record.sh: DOCS_TARGET must be mock or live, got '$target'" >&2; exit 2 ;;
esac

if [ "${#args[@]}" -eq 0 ]; then
  if [ "$target" = live ]; then
    read -r -a args <<<"${DOCS_LIVE_TAPES:-quickstart search tail scripting}"
  else
    for f in "$tapes_dir"/*.tape; do
      n=$(basename "$f" .tape)
      [ "$n" = settings ] || args+=("$n")
    done
  fi
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/bronto-tapes.XXXXXX")
mock_pid=""
sender_pid=""
# Invoked by the EXIT trap. Older shellcheck reports that as unreachable
# code (SC2317), newer as an unused function (SC2329).
# shellcheck disable=SC2317,SC2329
cleanup() {
  if [ -n "$sender_pid" ]; then kill "$sender_pid" 2>/dev/null || true; fi
  if [ -n "$mock_pid" ]; then kill "$mock_pid" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT

echo "record.sh: building bronto and the docs mock" >&2
mkdir -p "$work/bin" "$work/home" "$work/render"
(cd "$repo" && CGO_ENABLED=0 go build -o "$work/bin/bronto" ./cmd/bronto \
  && go build -o "$work/bin/docsmock" ./internal/tools/docsmock \
  && go build -o "$work/bin/tapegolden" ./internal/tools/tapegolden)

# The recording shell sees only what we set here: no BRONTO_* from the
# caller's shell, a throwaway HOME (on macOS that also keeps the login
# keychain out of reach), and the freshly built bronto first on PATH.
while IFS='=' read -r name _; do
  case "$name" in BRONTO_IT_*) ;; BRONTO_*) unset "$name" ;; esac
done < <(env)
export HOME="$work/home"
export BRONTO_CONFIG_DIR="$work/home/.config"
export PATH="$work/bin:$PATH"
export TZ=UTC
export DBUS_SESSION_BUS_ADDRESS=disabled:
mkdir -p "$BRONTO_CONFIG_DIR"

if [ "$target" = mock ]; then
  "$work/bin/docsmock" -addr 127.0.0.1:0 -url-file "$work/url" -dir "$repo/docs/testdata/mock" 2>"$work/mock.log" &
  mock_pid=$!
  for _ in $(seq 1 50); do [ -s "$work/url" ] && break; sleep 0.1; done
  [ -s "$work/url" ] || { echo "record.sh: docs mock did not start" >&2; cat "$work/mock.log" >&2; exit 1; }
  mock_url=$(cat "$work/url")
  export BRONTO_BASE_URL="$mock_url"
  export BRONTO_INGEST_URL="$mock_url/ingest"
  export BRONTO_API_KEY=bronto_docs_example_key
  export BRONTO_REGION=eu
else
  export BRONTO_API_KEY="$BRONTO_IT_MGMT_KEY"
  export BRONTO_REGION="${BRONTO_IT_REGION:-eu}"
fi

# live_send <collection/dataset> [extra jq filter] — re-timestamps the
# fixture events so the newest lands "now" and sends their raw lines.
fixture_end_s=1784453400 # 2026-07-19 09:30 UTC, the end of the frozen window
live_send() {
  local ds=$1 coll name now
  coll=${ds%%/*}
  name=${ds#*/}
  now=$(date -u +%s)
  jq -c --argjson now "$now" --argjson end "$fixture_end_s" \
    '(."@raw" | fromjson) + {timestamp: (($now - ($end - (.metadata.timestamp / 1000 | floor))) | todate)}' \
    "$repo/docs/testdata/mock/events/$coll/$name.jsonl" |
    BRONTO_API_KEY="${BRONTO_IT_INGEST_KEY:-$BRONTO_IT_MGMT_KEY}" bronto send --quiet --collection "$coll" -d "$name"
}

if [ "$target" = live ] && [ "${DOCS_LIVE_SEED:-1}" = 1 ]; then
  command -v jq >/dev/null 2>&1 || { echo "record.sh: live seeding needs jq" >&2; exit 1; }
  echo "record.sh: seeding the live account with the docs world's log events" >&2
  for ds in prod/checkout-service prod/payments-api prod/web-frontend staging/checkout-service; do
    live_send "$ds"
  done
  sleep "${DOCS_LIVE_SEED_WAIT:-90}"
fi

cp "$tapes_dir"/*.tape "$work/render/"
fail=0
for name in "${args[@]}"; do
  tape="$work/render/$name.tape"
  [ -f "$tape" ] || { echo "record.sh: no tape named '$name' in docs/tapes" >&2; exit 2; }
  if [ -n "$mock_pid" ]; then
    curl -fsS -X POST "$mock_url/__mock/reset" >/dev/null
  fi
  if [ "$target" = live ] && [ "$name" = tail ]; then
    # tail only shows events newer than its window: trickle one in a second.
    ( while :; do
        jq -c '(."@raw" | fromjson)' "$repo/docs/testdata/mock/events/prod/checkout-service.jsonl" |
          while IFS= read -r line; do
            printf '%s\n' "$line" | BRONTO_API_KEY="${BRONTO_IT_INGEST_KEY:-$BRONTO_IT_MGMT_KEY}" \
              bronto send --quiet --collection prod -d checkout-service || true
            sleep 1
          done
      done ) &
    sender_pid=$!
  fi
  echo "record.sh: rendering $name ($target)" >&2
  (cd "$work/render" && vhs -q "$name.tape") || { echo "record.sh: vhs failed for $name" >&2; exit 1; }
  if [ -n "$sender_pid" ]; then kill "$sender_pid" 2>/dev/null || true; sender_pid=""; fi
  "$work/bin/tapegolden" <"$work/render/$name.txt" >"$work/render/$name.golden.txt"

  if [ "$check" = 1 ]; then
    if [ ! -f "$golden_dir/$name.txt" ]; then
      echo "record.sh: missing golden docs/tapes/golden/$name.txt — run 'make docs-tapes'" >&2
      fail=1
    elif ! diff -u --label "golden/$name.txt (committed)" --label "golden/$name.txt (rendered now)" \
        "$golden_dir/$name.txt" "$work/render/$name.golden.txt"; then
      echo "record.sh: $name.tape output changed — run 'make docs-tapes' and commit the updated golden and video" >&2
      fail=1
    else
      echo "record.sh: $name ok" >&2
    fi
    continue
  fi

  mkdir -p "$out_dir"
  cp "$work/render/$name.webm" "$out_dir/$name.webm"
  if [ -f "$work/render/$name.png" ]; then cp "$work/render/$name.png" "$out_dir/$name.png"; fi
  if [ "$target" = mock ]; then
    mkdir -p "$golden_dir"
    cp "$work/render/$name.golden.txt" "$golden_dir/$name.txt"
  fi
  printf 'record.sh: wrote docs/public/tapes/%s.webm (%s bytes)\n' "$name" "$(wc -c <"$out_dir/$name.webm" | tr -d ' ')" >&2
done

if [ -n "$mock_pid" ] && grep -q 'unhandled' "$work/mock.log"; then
  echo "record.sh: the mock saw requests it has no route for:" >&2
  grep 'unhandled' "$work/mock.log" >&2
  fail=1
fi
exit "$fail"
