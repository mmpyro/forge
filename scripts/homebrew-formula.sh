#!/usr/bin/env bash
# Render the Homebrew formula for a release from its checksums.txt.
# Usage: scripts/homebrew-formula.sh VERSION CHECKSUMS_FILE > Formula/helm-forge.rb
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEMPLATE=$ROOT/packaging/homebrew/helm-forge.rb.tmpl

if [ $# -ne 2 ]; then
  echo "usage: $0 VERSION CHECKSUMS_FILE" >&2
  exit 2
fi
version=${1#v}
checksums=$2

if ! [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: version must be X.Y.Z (prereleases are not published to brew), got '$1'" >&2
  exit 2
fi

sha_for() {
  local sha
  sha=$(awk -v f="forge-$1" '$2 == f || $2 == "*"f { print $1 }' "$checksums")
  if ! [[ $sha =~ ^[0-9a-f]{64}$ ]]; then
    echo "error: no sha256 for forge-$1 in $checksums" >&2
    exit 1
  fi
  echo "$sha"
}

darwin_arm64=$(sha_for darwin-arm64)
darwin_amd64=$(sha_for darwin-amd64)
linux_arm64=$(sha_for linux-arm64)
linux_amd64=$(sha_for linux-amd64)

sed -e "s/@VERSION@/$version/" \
    -e "s/@SHA_DARWIN_ARM64@/$darwin_arm64/" \
    -e "s/@SHA_DARWIN_AMD64@/$darwin_amd64/" \
    -e "s/@SHA_LINUX_ARM64@/$linux_arm64/" \
    -e "s/@SHA_LINUX_AMD64@/$linux_amd64/" \
    "$TEMPLATE"
