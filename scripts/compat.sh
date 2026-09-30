#!/usr/bin/env bash
# Compatibility gate: for every fixture, forge must leave the same charts/,
# Chart.lock (ignoring `generated:`) and `helm template` output as helm does,
# for every helm binary in $HELMS.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
FORGE=${FORGE:-$ROOT/bin/forge}
HELMS=${HELMS:-"helm $ROOT/.bin/helm3"}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failures=0

sha() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }

prepare() { # prepare <fixture> <mode> <dest>
  local name=$1 mode=$2 dest=$3
  mkdir -p "$(dirname "$dest")"
  cp -R "$ROOT/testdata/charts/$name" "$dest"
  if [ -d "$ROOT/testdata/seeds/$name" ]; then
    mkdir -p "$dest/charts"
    cp -R "$ROOT/testdata/seeds/$name/." "$dest/charts/"
  fi
  if [ "$mode" = build ] && [ -f "$ROOT/testdata/golden/$name/Chart.lock" ]; then
    cp "$ROOT/testdata/golden/$name/Chart.lock" "$dest/"
  fi
}

snapshot() { # snapshot <helm> <chart-dir>
  local helm=$1 dir=$2
  echo "## charts/"
  [ -d "$dir/charts" ] && (cd "$dir/charts" && ls -A | LC_ALL=C sort)
  echo "## sha256"
  if compgen -G "$dir/charts/*.tgz" >/dev/null; then (cd "$dir/charts" && sha ./*.tgz | LC_ALL=C sort -k2); fi
  echo "## Chart.lock"
  [ -f "$dir/Chart.lock" ] && grep -v '^generated:' "$dir/Chart.lock"
  echo "## template"
  "$helm" template fixture "$dir" 2>&1
}

for helm in $HELMS; do
  hv=$("$helm" version --short)
  for fx in "$ROOT"/testdata/charts/*/; do
    name=$(basename "$fx")
    for mode in update build; do
      base="$work/$hv/$name/$mode"
      prepare "$name" "$mode" "$base/helm/$name"
      prepare "$name" "$mode" "$base/forge/$name"
      if ! HELM_CACHE_HOME="$base/hc" HELM_CONFIG_HOME="$base/hconf" \
        "$helm" dependency "$mode" --plain-http "$base/helm/$name" >"$base/helm.log" 2>&1; then
        echo "FAIL helm errored: $hv $name $mode"; cat "$base/helm.log"; failures=$((failures + 1)); continue
      fi
      if ! HELM_FORGE_CACHE="$base/fc" "$FORGE" dep "$mode" --plain-http "$base/forge/$name" >"$base/forge.log" 2>&1; then
        echo "FAIL forge errored: $hv $name $mode"; cat "$base/forge.log"; failures=$((failures + 1)); continue
      fi
      if diff -u <(snapshot "$helm" "$base/helm/$name") <(snapshot "$helm" "$base/forge/$name") >"$base/diff"; then
        echo "ok   $hv $name $mode"
      else
        echo "FAIL $hv $name $mode"; cat "$base/diff"; failures=$((failures + 1))
      fi
    done
  done
done

if [ "$failures" -eq 0 ]; then echo "compat: all fixtures match"; else echo "compat: $failures failure(s)"; exit 1; fi
