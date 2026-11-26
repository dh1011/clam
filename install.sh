#!/bin/sh
# Install clam: downloads the release binary for this OS and CPU, checks its
# SHA-256 checksum, and puts it in ~/.local/bin (or $CLAM_INSTALL_DIR).
#
#   curl -fsSL https://raw.githubusercontent.com/dh1011/clam/main/install.sh | sh
#
# Environment:
#   CLAM_VERSION      release tag to install, such as v0.1.0 (default: latest)
#   CLAM_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#   CLAM_REPO_URL     repository to download from (default: the GitHub repo)
set -eu

repo=${CLAM_REPO_URL:-https://github.com/dh1011/clam}
dir=${CLAM_INSTALL_DIR:-$HOME/.local/bin}

die() { printf 'clam install: %s\n' "$*" >&2; exit 1; }

case $(uname -s) in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    FreeBSD) os=freebsd ;;
    MINGW* | MSYS* | CYGWIN*) die "on Windows, download clam-windows-amd64.exe from $repo/releases" ;;
    *) die "unsupported OS: $(uname -s)" ;;
esac

case $(uname -m) in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    armv7* ) arch=arm ;;
    i386 | i686) arch=386 ;;
    *) die "unsupported CPU: $(uname -m)" ;;
esac
case $os/$arch in
    linux/*) ;;
    darwin/amd64 | darwin/arm64 | freebsd/amd64 | freebsd/arm64) ;;
    *) die "no prebuilt binary for $os/$arch; build from source with make" ;;
esac

if [ -n "${CLAM_VERSION-}" ]; then
    base=$repo/releases/download/$CLAM_VERSION
else
    base=$repo/releases/latest/download
fi
asset=clam-$os-$arch

if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -qO "$2" "$1"; }
else
    die "needs curl or wget"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

printf 'Downloading %s\n' "$base/$asset"
fetch "$base/$asset" "$tmp/$asset" || die "download failed: $base/$asset"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" || die "download failed: $base/SHA256SUMS"

want=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
[ -n "$want" ] || die "$asset is not listed in SHA256SUMS"
if command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$tmp/$asset" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
    got=$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')
elif command -v sha256 >/dev/null 2>&1; then
    got=$(sha256 -q "$tmp/$asset")
else
    die "needs sha256sum, shasum or sha256 to verify the download"
fi
[ "$got" = "$want" ] || die "checksum mismatch for $asset"

mkdir -p "$dir"
chmod +x "$tmp/$asset"
mv "$tmp/$asset" "$dir/clam"
printf 'Installed %s\n' "$("$dir/clam" version 2>/dev/null || echo clam) to $dir/clam"

case :$PATH: in
    *:"$dir":*) ;;
    *) printf '\n%s is not on your PATH. Add it in your shell startup file:\n  export PATH="%s:$PATH"\n' "$dir" "$dir" ;;
esac

case ${SHELL##*/} in
    zsh) rc='~/.zshrc' line='eval "$(clam init zsh)"' ;;
    *) rc='~/.bashrc' line='eval "$(clam init bash)"' ;;
esac
printf '\nNext:\n  1. export OPENAI_API_KEY=...   (or use another agent: clam agent)\n'
printf '  2. Add this line to %s, then open a new terminal:\n       %s\n' "$rc" "$line"
