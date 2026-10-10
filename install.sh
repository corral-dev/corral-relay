#!/bin/sh
# corral-relay installer: builds and installs from the public source.
# Needs only sh, git, and Go (>= 1.25). Dependency-free otherwise.
#
# Usage:
#   sh install.sh                        # user-local install to $HOME/.local/bin (no sudo)
#   PREFIX=/usr/local sh install.sh      # system-wide install
#   PREFIX=$HOME/.local sh install.sh    # explicit user-local install
#   VERSION=v0.1.0 sh install.sh         # pin a version tag
#   curl -fsSL https://raw.githubusercontent.com/corral-dev/corral-relay/main/install.sh | sh
set -eu

REPO="${REPO:-https://github.com/corral-dev/corral-relay.git}"
VERSION="${VERSION:-main}"
PREFIX="${PREFIX:-$HOME/.local}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "install.sh: required command '$1' not found" >&2
    exit 1
  }
}

need git
need go

TMPDIR_BASE="${TMPDIR:-/tmp}"
WORK="$(mktemp -d "$TMPDIR_BASE/corral-relay-install.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT INT TERM

echo "install.sh: cloning $REPO ($VERSION)"
git clone --quiet --depth 1 ${VERSION:+--branch "$VERSION"} "$REPO" "$WORK/src"
cd "$WORK/src"

echo "install.sh: building"
go build -o corral-relay ./cmd/corral-relay

BIN="$PREFIX/bin"
mkdir -p "$BIN"
cp corral-relay "$BIN/corral-relay"
chmod 755 "$BIN/corral-relay"

echo "install.sh: installed to $BIN/corral-relay"
"$BIN/corral-relay" --help >/dev/null 2>&1 || true
case ":$PATH:" in
  *":$BIN:"*) ;;
  *) echo "install.sh: note: $BIN is not on your PATH" >&2 ;;
esac
