#!/bin/sh
# completions.sh builds a throwaway host binary and asks cobra's built-in
# `completion` command to emit shell-completion scripts for bash/zsh/fish
# into ./completions. It runs as a goreleaser `before` hook (once, on the
# build host, before any cross-compiled targets are produced) so the
# archives/nfpms/brews entries can reference static files instead of trying
# to generate completions per-target (which would require running
# cross-compiled binaries — not generally possible on the build host).
set -eu

root_dir=$(cd "$(dirname "$0")/.." && pwd)
out_dir="$root_dir/completions"
tmp_bin="$root_dir/.completions-gen-bin"

mkdir -p "$out_dir"

cleanup() {
	rm -f "$tmp_bin"
}
trap cleanup EXIT

(cd "$root_dir" && CGO_ENABLED=0 go build -o "$tmp_bin" ./cmd/bronto)

"$tmp_bin" completion bash >"$out_dir/bronto.bash"
"$tmp_bin" completion zsh >"$out_dir/bronto.zsh"
"$tmp_bin" completion fish >"$out_dir/bronto.fish"
