#!/usr/bin/env bash
# Starts the local registry (if needed) and pushes every fixture chart to it,
# then serves dep-a and dep-b from a classic chart repository as well.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
REGISTRY=${REGISTRY:-localhost:5001}
CHARTREPO=${CHARTREPO:-localhost:5002}
HELM=${HELM:-helm}

if ! docker ps --format '{{.Names}}' | grep -qx forge-registry; then
  docker run -d --rm -p 5001:5000 --name forge-registry registry:2 >/dev/null
fi
until curl -sf "http://$REGISTRY/v2/" >/dev/null; do sleep 0.2; done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

push() { # push <src-dir> <version>
  "$HELM" package "$1" --version "$2" --destination "$work" >/dev/null
  "$HELM" push "$work/$(basename "$1")-$2.tgz" "oci://$REGISTRY/charts" --plain-http >/dev/null 2>&1 ||
    { echo "push failed: $1 $2" >&2; exit 1; }
}

for v in 1.0.0 1.1.0 1.2.0 2.0.0; do push "$ROOT/testdata/src/dep-a" "$v"; done
for v in 0.1.0 0.2.0-rc.1; do push "$ROOT/testdata/src/dep-b" "$v"; done
push "$ROOT/testdata/src/dep-c" 1.0.0+build.1

# 40 small charts for the benchmark: bench-00 … bench-39.
for i in $(seq -w 0 39); do
  d="$work/src/bench-$i"
  mkdir -p "$d"
  cp -R "$ROOT/testdata/src/dep-a/." "$d"
  sed -i.bak "s/^name: dep-a$/name: bench-$i/" "$d/Chart.yaml" && rm "$d/Chart.yaml.bak"
  push "$d" 1.0.0
done

# Seed archive for the "stale" fixture (not pushed).
"$HELM" package "$ROOT/testdata/src/old-dep" --destination "$ROOT/testdata/seeds/stale" >/dev/null
echo "fixtures pushed to $REGISTRY"

# Classic chart repository on $CHARTREPO: index.yaml with relative URLs (so a
# proxy in front of it sees every download), served by nginx.
repo="$ROOT/.chartrepo"
# Empty it rather than recreate it: a running nginx bind-mounts this directory.
mkdir -p "$repo" && find "$repo" -mindepth 1 -delete
for v in 1.0.0 1.1.0 1.2.0 2.0.0; do "$HELM" package "$ROOT/testdata/src/dep-a" --version "$v" --destination "$repo" >/dev/null; done
for v in 0.1.0 0.2.0-rc.1; do "$HELM" package "$ROOT/testdata/src/dep-b" --version "$v" --destination "$repo" >/dev/null; done
"$HELM" repo index "$repo"
if ! docker ps --format '{{.Names}}' | grep -qx forge-chartrepo; then
  docker run -d --rm -p 5002:80 --name forge-chartrepo -v "$repo:/usr/share/nginx/html:ro" nginx:alpine >/dev/null
fi
until curl -sf "http://$CHARTREPO/index.yaml" >/dev/null; do sleep 0.2; done
echo "chart repository served on $CHARTREPO"
