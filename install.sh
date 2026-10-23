#!/bin/sh
# Download and verify bl before replacing the installed binary.
set -eu
fail() { printf '%s\n' "$*" >&2; exit 1; }
system=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$system" in darwin|linux) ;; *) fail "Unsupported OS: $system" ;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) fail 'Unsupported architecture' ;; esac
version=${BL_VERSION:-latest}
case "$version" in latest) release=latest/download ;; v*) release=download/$version ;; *) fail 'BL_VERSION must be latest or a v-prefixed tag' ;; esac
base=${BL_RELEASE_BASE_URL:-https://github.com/brettinternet/backlog/releases}
destination=${BL_INSTALL_DIR:-$HOME/.local/bin}
archive=bl_${system}_${arch}.tar.gz
scratch=$(mktemp -d)
# Only remove files created by this process; no recursive variable-path deletion.
cleanup() { unlink "$scratch/$archive" 2>/dev/null || :; unlink "$scratch/checksums.txt" 2>/dev/null || :; unlink "$scratch/bl" 2>/dev/null || :; rmdir "$scratch"; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
curl --fail --silent --show-error --location "$base/$release/$archive" -o "$scratch/$archive"
curl --fail --silent --show-error --location "$base/$release/checksums.txt" -o "$scratch/checksums.txt"
expected=$(awk -v name="$archive" '$2 == name { print $1; count++ } END { if (count != 1) exit 1 }' "$scratch/checksums.txt") || fail 'Missing or duplicate checksum'
case "$expected" in *[!0-9a-f]*|'') fail 'Invalid checksum' ;; esac
[ "${#expected}" -eq 64 ] || fail 'Invalid checksum'
if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$scratch/$archive")
else
    actual=$(shasum -a 256 "$scratch/$archive")
fi
[ "${actual%% *}" = "$expected" ] || fail 'Checksum mismatch; nothing installed'
tar -xzf "$scratch/$archive" -C "$scratch" bl
[ -f "$scratch/bl" ] && [ ! -L "$scratch/bl" ] || fail 'Invalid binary'
mkdir -p "$destination"
install -m 755 "$scratch/bl" "$destination/bl"
printf 'Installed %s/bl\n' "$destination"
