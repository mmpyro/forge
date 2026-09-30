#!/usr/bin/env bash
# Benchmark: helm vs forge (cold and warm cache) on 40 dependencies behind
# toxiproxy adding 50 ms to every registry response.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
FORGE=${FORGE:-$ROOT/bin/forge}
HELM=${HELM:-helm}
RUNS=${RUNS:-5}
for tool in docker hyperfine jq; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 1; }
done

# toxiproxy: localhost:5002 -> registry on localhost:5001, +50 ms per response.
if ! docker ps --format '{{.Names}}' | grep -qx forge-toxiproxy; then
  docker run -d --rm --name forge-toxiproxy --add-host=host.docker.internal:host-gateway \
    -p 5002:5002 -p 8474:8474 ghcr.io/shopify/toxiproxy:2.9.0 >/dev/null
  until curl -sf localhost:8474/version >/dev/null; do sleep 0.2; done
  curl -sf -X POST localhost:8474/proxies \
    -d '{"name":"registry","listen":"0.0.0.0:5002","upstream":"host.docker.internal:5001"}' >/dev/null
  curl -sf -X POST localhost:8474/proxies/registry/toxics \
    -d '{"type":"latency","stream":"downstream","attributes":{"latency":50}}' >/dev/null
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
chart="$work/bench40"
mkdir -p "$chart"
{
  printf 'apiVersion: v2\nname: bench40\nversion: 0.1.0\ndependencies:\n'
  for i in $(seq -w 0 39); do
    printf '  - name: bench-%s\n    version: 1.0.0\n    repository: oci://localhost:5002/charts\n' "$i"
  done
} >"$chart/Chart.yaml"

# Writes Chart.lock (which helm must accept) and fills the warm cache.
HELM_FORGE_CACHE="$work/fc" "$FORGE" dep update --plain-http "$chart" >/dev/null

export HELM_CONFIG_HOME="$work/hconf"
mkdir -p "$ROOT/bench"
hyperfine --runs "$RUNS" --export-json "$work/r.json" --export-markdown "$ROOT/bench/results.md" \
  -n helm --prepare "rm -rf $chart/charts $work/hc" \
  "HELM_CACHE_HOME=$work/hc $HELM dependency build --plain-http $chart" \
  -n forge-cold --prepare "rm -rf $chart/charts $work/fc-cold" \
  "HELM_FORGE_CACHE=$work/fc-cold $FORGE dep build --plain-http $chart" \
  -n forge-warm --prepare "rm -rf $chart/charts" \
  "HELM_FORGE_CACHE=$work/fc $FORGE dep build --plain-http $chart"

mean() { jq -r --arg n "$1" '.results[] | select(.command == $n) | .mean' "$work/r.json"; }
helm_s=$(mean helm)
cold_s=$(mean forge-cold)
warm_s=$(mean forge-warm)
speedup=$(awk -v h="$helm_s" -v c="$cold_s" 'BEGIN { print h / c }')
printf 'helm %.2fs | forge cold %.2fs (%.1fx) | forge warm %.3fs\n' "$helm_s" "$cold_s" "$speedup" "$warm_s"

ok=1
awk -v s="$speedup" 'BEGIN { exit !(s >= 5) }' || { echo "FAIL: cold run is less than 5x faster than helm"; ok=0; }
awk -v w="$warm_s" 'BEGIN { exit !(w < 1) }' || { echo "FAIL: warm run took 1s or more"; ok=0; }
[ "$ok" = 1 ] && echo "bench: targets met" || exit 1
