#!/bin/sh
# install.sh downloads and installs the latest (or a pinned) bronto-cli
# release from GitHub, verifying its checksum against the release's
# checksums.txt before installing.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/svrnm/bronto-cli/main/scripts/install.sh | sh
#
# Environment overrides:
#   BINDIR   install directory (default: /usr/local/bin)
#   VERSION  release tag to install, e.g. v0.1.0 (default: latest)
set -eu

REPO="svrnm/bronto-cli"
BINDIR="${BINDIR:-/usr/local/bin}"
VERSION="${VERSION:-}"

log() {
	printf '%s\n' "$*" >&2
}

fail() {
	log "install.sh: error: $*"
	exit 1
}

need_cmd() {
	if ! command -v "$1" >/dev/null 2>&1; then
		fail "required command '$1' not found"
	fi
}

need_cmd curl
need_cmd tar
need_cmd install

detect_os() {
	os=$(uname -s)
	case "$os" in
	Linux) echo "linux" ;;
	Darwin) echo "darwin" ;;
	MINGW* | MSYS* | CYGWIN*) echo "windows" ;;
	*) fail "unsupported OS: $os" ;;
	esac
}

detect_arch() {
	arch=$(uname -m)
	case "$arch" in
	x86_64 | amd64) echo "amd64" ;;
	arm64 | aarch64) echo "arm64" ;;
	*) fail "unsupported architecture: $arch" ;;
	esac
}

OS=$(detect_os)
ARCH=$(detect_arch)

if [ -z "$VERSION" ]; then
	log "Resolving latest release for $REPO..."
	VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
		grep '"tag_name"' | head -n1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
	[ -n "$VERSION" ] || fail "could not resolve latest release tag"
fi

# goreleaser's {{ .Version }} template strips the leading "v" from the tag.
VERSION_NO_V=$(printf '%s' "$VERSION" | sed 's/^v//')

if [ "$OS" = "windows" ]; then
	ARCHIVE_EXT="zip"
else
	ARCHIVE_EXT="tar.gz"
fi

ARCHIVE="bronto_${VERSION_NO_V}_${OS}_${ARCH}.${ARCHIVE_EXT}"
BASE_URL="https://github.com/$REPO/releases/download/$VERSION"

WORKDIR=$(mktemp -d)
cleanup() {
	rm -rf "$WORKDIR"
}
trap cleanup EXIT

log "Downloading $ARCHIVE ($VERSION)..."
curl -fsSL -o "$WORKDIR/$ARCHIVE" "$BASE_URL/$ARCHIVE"
curl -fsSL -o "$WORKDIR/checksums.txt" "$BASE_URL/checksums.txt"

log "Verifying checksum..."
(
	cd "$WORKDIR"
	if command -v sha256sum >/dev/null 2>&1; then
		grep " ${ARCHIVE}\$" checksums.txt | sha256sum -c -
	elif command -v shasum >/dev/null 2>&1; then
		grep " ${ARCHIVE}\$" checksums.txt | shasum -a 256 -c -
	else
		fail "no sha256sum or shasum available to verify checksum"
	fi
)

log "Extracting..."
if [ "$ARCHIVE_EXT" = "zip" ]; then
	need_cmd unzip
	unzip -o -q "$WORKDIR/$ARCHIVE" -d "$WORKDIR"
	BIN_NAME="bronto.exe"
else
	tar -xzf "$WORKDIR/$ARCHIVE" -C "$WORKDIR"
	BIN_NAME="bronto"
fi

[ -f "$WORKDIR/$BIN_NAME" ] || fail "extracted archive did not contain $BIN_NAME"
chmod +x "$WORKDIR/$BIN_NAME"

if [ -d "$BINDIR" ] && [ -w "$BINDIR" ]; then
	install -m 0755 "$WORKDIR/$BIN_NAME" "$BINDIR/$BIN_NAME"
elif mkdir -p "$BINDIR" 2>/dev/null && [ -w "$BINDIR" ]; then
	install -m 0755 "$WORKDIR/$BIN_NAME" "$BINDIR/$BIN_NAME"
else
	log "No write access to $BINDIR, retrying with sudo..."
	need_cmd sudo
	sudo mkdir -p "$BINDIR"
	sudo install -m 0755 "$WORKDIR/$BIN_NAME" "$BINDIR/$BIN_NAME"
fi

log "Installed $BINDIR/$BIN_NAME ($VERSION)"
"$BINDIR/$BIN_NAME" --version >&2 || true
