#!/usr/bin/env bash
# sync-official-docs.sh copies the CLI docs (docs/cli/) into a local
# checkout of the official Bronto docs, where they are served under /cli.
#
#   scripts/sync-official-docs.sh [--ref <git-ref>] <path-to-docs-checkout>
#
# --ref picks the version to copy, normally the release tag (v1.4.0). Without
# it the script copies docs/cli/ from the working tree, uncommitted changes
# included, and says so.
#
# The target's cli/ folder is replaced as a whole, so pages deleted here are
# deleted there too. Never edit cli/ in the official docs: the next sync
# overwrites it. The script does not commit, push, or open a pull request;
# it prints a suggested commit message. On a release, the docs-sync
# workflow runs it and opens the pull request.
#
# One-time setup in the official docs: add the CLI tab to docs.json with
#
#   "navigation": { "tabs": [ ..., { "$ref": "cli/navigation.json" } ] }
#
# The script stops with that hint until the reference is there.
set -euo pipefail

usage() {
  sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

ref=""
while [ $# -gt 0 ]; do
  case "$1" in
    --ref) [ $# -ge 2 ] || usage; ref=$2; shift 2 ;;
    -h | --help) usage ;;
    -*) echo "sync-official-docs: unknown flag $1" >&2; usage ;;
    *) break ;;
  esac
done
[ $# -eq 1 ] || usage
target=$1

repo=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
if [ ! -f "$target/docs.json" ]; then
  echo "sync-official-docs: $target/docs.json not found; pass the root of the official docs checkout" >&2
  exit 1
fi
if [ "$(cd "$target" && pwd -P)" = "$(cd "$repo/docs" && pwd -P)" ]; then
  echo "sync-official-docs: $target is this repo's own docs/ directory" >&2
  exit 1
fi
if ! grep -q '"cli/navigation.json"' "$target/docs.json"; then
  cat >&2 <<'EOF'
sync-official-docs: the target's docs.json has no CLI tab yet. Add it once:

  "navigation": { "tabs": [ ..., { "$ref": "cli/navigation.json" } ] }
EOF
  exit 1
fi

src=$(mktemp -d)
trap 'rm -rf "$src"' EXIT
if [ -n "$ref" ]; then
  sha=$(git -C "$repo" rev-parse --verify --quiet "$ref^{commit}") || {
    echo "sync-official-docs: unknown git ref $ref" >&2
    exit 1
  }
  git -C "$repo" archive "$sha" docs/cli | tar -x -C "$src"
  from="$ref ($(git -C "$repo" rev-parse --short "$sha"))"
else
  mkdir -p "$src/docs"
  cp -R "$repo/docs/cli" "$src/docs/cli"
  from="working tree at $(git -C "$repo" rev-parse --short HEAD)"
  if [ -n "$(git -C "$repo" status --porcelain -- docs/cli)" ]; then
    from="$from, with uncommitted changes"
    echo "sync-official-docs: warning: docs/cli has uncommitted changes; pass --ref <tag> to copy a release" >&2
  fi
fi

rm -rf "$target/cli"
cp -R "$src/docs/cli" "$target/cli"

echo "sync-official-docs: copied docs/cli from $from to $target/cli"
git -C "$target" status --short -- cli | sed 's/^/  /' | head -n 20
echo
echo "Suggested commit message:"
echo "  docs(cli): sync the bronto CLI docs from $from"
