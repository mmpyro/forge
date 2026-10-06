#!/usr/bin/env bash
# Helm plugin smoke test: for every helm binary in $HELMS, install this checkout
# as a plugin (the install hook downloads from a local fake release, so the
# download + checksum path runs for real), then check `helm forge` works.
# A corrupted checksums.txt must make the install fail.
# If the local registry (localhost:5001) is up, `helm forge dep update` must also
# produce the same charts/ and Chart.lock as standalone forge.
set -euo pipefail
# Native path: on Windows (Git Bash) helm needs C:/... rather than /c/...
npwd() { pwd -W 2>/dev/null || pwd; }
ROOT=$(cd "$(dirname "$0")/.." && npwd)
HELMS=${HELMS:-"helm $([ -x "$ROOT/.bin/helm3" ] && echo "$ROOT/.bin/helm3")"}
PORT=${PORT:-18765}
FIXTURE=${FIXTURE:-ranges}

version=$(sed -n 's/^version:[[:space:]]*"\{0,1\}\([^"[:space:]]*\)"\{0,1\}[[:space:]]*$/\1/p' "$ROOT/plugin.yaml")
goos=$(go env GOOS)
goarch=$(go env GOARCH)
ext=; [ "$goos" = windows ] && ext=.exe
asset=forge-$goos-$goarch$ext

work=$(cd "$(mktemp -d)" && npwd)
server=
cleanup() {
  if [ -n "$server" ]; then kill "$server" 2>/dev/null; wait "$server" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT

sha() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
fail() { echo "FAIL $*" >&2; exit 1; }

# Fake releases: good/v<version>/ and bad/v<version>/ (wrong checksum).
good=$work/release/good/v$version
bad=$work/release/bad/v$version
mkdir -p "$good" "$bad"
CGO_ENABLED=0 go build -trimpath -ldflags "-X main.Version=$version" -o "$good/$asset" "$ROOT/cmd/forge"
(cd "$good" && sha "$asset" | sed 's/ \*/  /' > checksums.txt)
cp "$good/$asset" "$bad/$asset"
printf '%064d  %s\n' 0 "$asset" > "$bad/checksums.txt"

python=
for p in python3 python; do "$p" -c '' 2>/dev/null && { python=$p; break; }; done
[ -n "$python" ] || fail "python3 is needed to serve the fake release"
"$python" -m http.server "$PORT" --bind 127.0.0.1 --directory "$work/release" >"$work/http.log" 2>&1 &
server=$!
for _ in $(seq 50); do
  curl -fs "http://127.0.0.1:$PORT/good/v$version/checksums.txt" >/dev/null && break
  sleep 0.2
done

registry_up=false
curl -fs http://localhost:5001/v2/ >/dev/null 2>&1 && registry_up=true

for helm in $HELMS; do
  hv=$("$helm" version --short)
  echo "== $hv"
  home=$work/$(echo "$hv" | tr -c 'a-zA-Z0-9.\n' _)
  # Plugins go to $HELM_DATA_HOME/plugins (Helm 4's installer ignores HELM_PLUGINS).
  unset HELM_PLUGINS
  export HELM_CACHE_HOME=$home/cache HELM_CONFIG_HOME=$home/config

  # Corrupted checksum: the hook must refuse the binary. Own data home, since
  # Helm 4 leaves the plugin dir behind when the install hook fails.
  if HELM_DATA_HOME=$home/data-bad HELM_FORGE_PLUGIN_URL=http://127.0.0.1:$PORT/bad \
    "$helm" plugin install "$ROOT" >"$home-bad.log" 2>&1; then
    cat "$home-bad.log"; fail "$hv: install succeeded with a wrong checksum"
  fi
  grep -q "checksum mismatch" "$home-bad.log" || { cat "$home-bad.log"; fail "$hv: no checksum error"; }
  echo "ok   checksum mismatch rejected"

  export HELM_DATA_HOME=$home/data
  HELM_FORGE_PLUGIN_URL=http://127.0.0.1:$PORT/good \
    "$helm" plugin install "$ROOT" >"$home-install.log" 2>&1 || { cat "$home-install.log"; fail "$hv: plugin install"; }
  echo "ok   plugin install"

  out=$("$helm" forge --version | head -1)
  [ "$out" = "forge $version" ] || fail "$hv: helm forge --version = '$out', want 'forge $version'"
  echo "ok   $out"

  cache=$("$helm" forge cache path | tr '\\' /)
  [ "$cache" = "$HELM_CACHE_HOME/forge" ] || fail "$hv: helm forge cache path = '$cache', want $HELM_CACHE_HOME/forge"
  echo "ok   cache at $cache"

  if $registry_up; then
    for who in plugin standalone; do
      mkdir -p "$home/$who"
      cp -R "$ROOT/testdata/charts/$FIXTURE" "$home/$who/"
    done
    "$helm" forge dep update --plain-http "$home/plugin/$FIXTURE" >/dev/null
    HELM_FORGE_CACHE=$home/standalone-cache "$ROOT/bin/forge$ext" dep update --plain-http "$home/standalone/$FIXTURE" >/dev/null
    for who in plugin standalone; do
      d=$home/$who/$FIXTURE
      { (cd "$d/charts" && sha ./*.tgz | LC_ALL=C sort -k2); grep -v '^generated:' "$d/Chart.lock"; } > "$home/$who.snap"
    done
    diff -u "$home/standalone.snap" "$home/plugin.snap" || fail "$hv: helm forge dep update differs from forge"
    echo "ok   helm forge dep update == forge dep update ($FIXTURE)"
  else
    echo "skip dep update comparison (no registry on localhost:5001; run 'make fixtures')"
  fi

  "$helm" plugin uninstall forge >/dev/null
done
echo "plugin smoke: all ok"
