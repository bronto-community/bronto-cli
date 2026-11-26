#!/usr/bin/env bash
# render.sh records the two 'bronto signup' PROTOTYPE demos:
#
#   docs/tapes/signup/render.sh [signup-cli|signup-agent ...]   (default: both)
#   make signup-recordings
#
# Each tape runs the real bronto binary against the fake sign-up backend
# (internal/tools/signupmock): no real account, no real Cognito, no real
# Bronto API. Output goes to recordings/signup/<name>.{mp4,gif,webm}, which
# is not committed.
#
# Requires vhs (with ttyd and ffmpeg), curl, jq and Go.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
out_dir="$repo/recordings/signup"

for tool in vhs ttyd ffmpeg curl jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "render.sh: $tool not found on PATH" >&2; exit 1; }
done
tapes=("$@")
[ "${#tapes[@]}" -gt 0 ] || tapes=(signup-cli signup-agent)

work=$(mktemp -d "${TMPDIR:-/tmp}/bronto-signup-tapes.XXXXXX")
mock_pid=""
# shellcheck disable=SC2317,SC2329
cleanup() {
  if [ -n "$mock_pid" ]; then kill "$mock_pid" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT

echo "render.sh: building bronto and the fake sign-up backend" >&2
mkdir -p "$work/bin" "$work/render" "$out_dir"
(cd "$repo" && CGO_ENABLED=0 go build -o "$work/bin/bronto" ./cmd/bronto \
  && go build -o "$work/bin/signupmock" ./internal/tools/signupmock)
cp "$here/ada-clicks.sh" "$work/bin/ada-clicks"
cp "$here/agent-session.sh" "$work/bin/agent-session"
chmod +x "$work/bin/ada-clicks" "$work/bin/agent-session"
cp "$repo/docs/tapes/settings.tape" "$here"/*.tape "$work/render/"

# The recording shell sees no BRONTO_* from the caller. BRONTO_NO_KEYCHAIN
# keeps the stored key in the throwaway config dir, never the OS keychain.
while IFS='=' read -r name _; do
  case "$name" in BRONTO_*) unset "$name" ;; esac
done < <(env)
export PATH="$work/bin:$PATH"
export TZ=UTC
export BRONTO_NO_KEYCHAIN=1

for name in "${tapes[@]}"; do
  [ -f "$work/render/$name.tape" ] || { echo "render.sh: no tape named '$name'" >&2; exit 2; }
  # A fresh backend and config per tape: each starts with no account.
  rm -f "$work/url"
  "$work/bin/signupmock" -url-file "$work/url" 2>"$work/mock.log" &
  mock_pid=$!
  for _ in $(seq 1 50); do [ -s "$work/url" ] && break; sleep 0.1; done
  [ -s "$work/url" ] || { echo "render.sh: the fake backend did not start" >&2; cat "$work/mock.log" >&2; exit 1; }
  mock_url=$(cat "$work/url")
  export BRONTO_CONFIG_DIR="$work/config-$name"
  export BRONTO_BASE_URL="$mock_url" BRONTO_COGNITO_URL="$mock_url" BRONTO_INGEST_URL="$mock_url/ingest"
  mkdir -p "$BRONTO_CONFIG_DIR"

  echo "render.sh: rendering $name" >&2
  (cd "$work/render" && vhs -q "$name.tape") || { echo "render.sh: vhs failed for $name" >&2; exit 1; }
  kill "$mock_pid" 2>/dev/null || true
  mock_pid=""
  for ext in mp4 gif webm; do
    cp "$work/render/$name.$ext" "$out_dir/$name.$ext"
    printf 'render.sh: wrote recordings/signup/%s.%s (%s bytes)\n' "$name" "$ext" "$(wc -c <"$out_dir/$name.$ext" | tr -d ' ')" >&2
  done
done
