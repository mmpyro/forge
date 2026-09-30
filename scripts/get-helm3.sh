#!/usr/bin/env bash
# Downloads a Helm 3 binary to .bin/helm3 for the compatibility gate.
# Set HELM3_VERSION to the latest v3 release (https://github.com/helm/helm/releases).
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
HELM3_VERSION=${HELM3_VERSION:-v3.22.0}
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case $arch in x86_64) arch=amd64 ;; aarch64 | arm64) arch=arm64 ;; esac

if [ -x "$ROOT/.bin/helm3" ] && "$ROOT/.bin/helm3" version --short | grep -q "^$HELM3_VERSION"; then
  exit 0
fi
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "https://get.helm.sh/helm-$HELM3_VERSION-$os-$arch.tar.gz" | tar -xz -C "$tmp"
mkdir -p "$ROOT/.bin"
mv "$tmp/$os-$arch/helm" "$ROOT/.bin/helm3"
"$ROOT/.bin/helm3" version --short
