#!/bin/sh
# Helm plugin install/update hook: download the forge release binary that
# matches plugin.yaml's version and verify it against the release's checksums.txt.
# HELM_FORGE_PLUGIN_URL overrides the release base URL (mirrors, CI smoke test).
set -eu

dir=${HELM_PLUGIN_DIR:-$(cd "$(dirname "$0")/.." && pwd)}
version=$(sed -n 's/^version:[[:space:]]*"\{0,1\}\([^"[:space:]]*\)"\{0,1\}[[:space:]]*$/\1/p' "$dir/plugin.yaml")
if [ -z "$version" ]; then
  echo "forge: no version in $dir/plugin.yaml" >&2
  exit 1
fi
base=${HELM_FORGE_PLUGIN_URL:-https://github.com/mmpyro/forge/releases/download}
base=${base%/}/v$version

ext=
case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW* | MSYS* | CYGWIN*) os=windows ext=.exe ;;
  *) echo "forge: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "forge: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
asset=forge-$os-$arch$ext

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    echo "forge: need curl or wget to download $1" >&2
    return 1
  fi
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "forge: downloading $asset v$version"
fetch "$base/$asset" "$tmp/$asset" || { echo "forge: download failed: $base/$asset" >&2; exit 1; }
fetch "$base/checksums.txt" "$tmp/checksums.txt" || { echo "forge: download failed: $base/checksums.txt" >&2; exit 1; }

want=$(awk -v f="$asset" '$2 == f || $2 == "*"f { print $1 }' "$tmp/checksums.txt")
if [ -z "$want" ]; then
  echo "forge: $asset is not listed in checksums.txt" >&2
  exit 1
fi
got=$(sha256 "$tmp/$asset")
if [ "$got" != "$want" ]; then
  echo "forge: checksum mismatch for $asset: got $got, want $want" >&2
  exit 1
fi

mkdir -p "$dir/bin"
chmod +x "$tmp/$asset"
mv -f "$tmp/$asset" "$dir/bin/forge$ext"
echo "forge: installed $("$dir/bin/forge$ext" --version)"
