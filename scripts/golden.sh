#!/usr/bin/env bash
# Regenerates testdata/golden/<fixture>/{Chart.yaml,Chart.lock} with real `helm dependency update`.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
HELM=${HELM:-helm}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export HELM_CACHE_HOME="$work/cache" HELM_CONFIG_HOME="$work/config"

for fx in "$ROOT"/testdata/charts/*/; do
  name=$(basename "$fx")
  [ "$name" = nodeps ] && continue
  cp -R "$fx" "$work/$name"
  "$HELM" dependency update --plain-http "$work/$name" >/dev/null
  mkdir -p "$ROOT/testdata/golden/$name"
  cp "$fx/Chart.yaml" "$work/$name/Chart.lock" "$ROOT/testdata/golden/$name/"
done
echo "golden locks written"
