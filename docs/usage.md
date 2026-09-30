# Usage

## Install

### Homebrew (macOS / Linux)

```sh
brew tap mmpyro/forge https://github.com/mmpyro/forge
brew install helm-forge
forge --version
```

Upgrade with `brew update && brew upgrade helm-forge`.

### Prebuilt binaries

Prebuilt binaries and `checksums.txt` are attached to every
[GitHub release](https://github.com/mmpyro/forge/releases). The links
below point at v1.0.0.

| Platform | Asset | Download |
|---|---|---|
| macOS (Apple Silicon) | `forge-darwin-arm64` | [Download](https://github.com/mmpyro/forge/releases/download/v1.0.0/forge-darwin-arm64) |
| macOS (Intel) | `forge-darwin-amd64` | [Download](https://github.com/mmpyro/forge/releases/download/v1.0.0/forge-darwin-amd64) |
| Linux (x86_64) | `forge-linux-amd64` | [Download](https://github.com/mmpyro/forge/releases/download/v1.0.0/forge-linux-amd64) |
| Linux (ARM64) | `forge-linux-arm64` | [Download](https://github.com/mmpyro/forge/releases/download/v1.0.0/forge-linux-arm64) |
| Windows (x86_64) | `forge-windows-amd64.exe` | [Download](https://github.com/mmpyro/forge/releases/download/v1.0.0/forge-windows-amd64.exe) |
| Windows (ARM64) | `forge-windows-arm64.exe` | [Download](https://github.com/mmpyro/forge/releases/download/v1.0.0/forge-windows-arm64.exe) |

On macOS / Linux:

```sh
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
curl -fsSL -o forge "https://github.com/mmpyro/forge/releases/download/v1.0.0/forge-$os-$arch"
chmod +x forge && sudo mv forge /usr/local/bin/
forge --version
```

On Windows, download the `.exe`, rename it to `forge.exe` and put it in a
directory on your `PATH`.

### From source

Build from source (Go 1.25+):

```sh
make build            # → bin/forge, version from `git describe`
bin/forge --version
```

Or without the Makefile:

```sh
go build -o bin/forge ./cmd/forge
```

Put `bin/forge` on your `PATH`. Helm itself is still needed for everything
other than fetching dependencies.

## Commands

### `forge dep update [CHART]`

Resolves the constraints in `Chart.yaml`, downloads the charts into
`charts/` and writes `Chart.lock`.

- `CHART` defaults to `.`.
- Exact versions (`1.2.0`) are locked as written, without asking the registry
  — same as Helm.
- Ranges (`^1.0.0`, `>=0.1.0 <1.0.0`) pick the highest matching tag. Each
  repository's tag list is fetched once, even if several dependencies use it.
- `Chart.lock` is rewritten only when its digest changes, so re-running
  `update` with nothing new leaves the file (and its `generated:` time) alone.

### `forge dep build [CHART]`

Downloads exactly the versions pinned in `Chart.lock`.

- With no `Chart.lock`, behaves like `dep update` (Helm does the same).
- If `Chart.lock` no longer matches `Chart.yaml`, fails with Helm's message:
  *"the lock file (Chart.lock) is out of sync with the dependencies file
  (Chart.yaml). Please update the dependencies"*. Fix: run `forge dep update`.

`dependency` is an alias of `dep`, so `forge dependency build` also works.

### `forge cache path`

Prints the cache directory. Useful for CI cache steps.

### `forge cache clean`

Deletes the cache directory.

## Flags (`dep build` and `dep update`)

| Flag | Default | Meaning |
|---|---|---|
| `--concurrency` | `16` | Max requests in flight in total |
| `--per-host` | `8` | Max requests in flight per host |
| `--refresh` | `false` | Re-check tags with the registry instead of trusting the cached tag→digest mapping |
| `--plain-http` | `false` | Use plain HTTP (e.g. a local `registry:2`) |
| `--timeout` | `5m` | Time limit for the whole run |
| `--registry-config` | Helm's path | Helm's registry credentials file |

Per-request timeout is 60 s; failed requests are retried (see
[Errors and retries](#errors-and-retries)).

## Environment

| Variable | Effect |
|---|---|
| `HELM_FORGE_CACHE` | Cache root. Default `~/.cache/helm-forge` |
| `HELM_REGISTRY_CONFIG` | Credentials file, same as Helm. Default: Helm's `registry/config.json` |

## Authentication

forge reads credentials only from Helm's registry config. Log in the usual way:

```sh
helm registry login ghcr.io
forge dep build ./my-chart
```

A missing credentials file means anonymous access.

## Output

On success forge prints one line:

```
Saved 12 charts (9 cached, 3 downloaded) in 412ms
```

Aliases of one chart count separately in `Saved`, but the archive is
downloaded only once.

## What ends up in `charts/`

- One `<name>-<version>.tgz` per dependency — byte-identical to what Helm
  downloads.
- Stale chart archives (no longer a dependency) are removed, as
  `helm dep build` does. Non-chart files and unpacked subchart directories are
  kept.
- Archives are reflinked, hardlinked or copied from the cache (cheapest first).
  A hardlinked archive is read-only (`0444`); that is expected.

A run that fails leaves `charts/` exactly as it was.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Something failed — each failed dependency is listed |
| `2` | Usage error (bad flag, too many arguments, `--concurrency 0`, …) |

## Errors and retries

forge retries `408`, `429`, `5xx` and network errors up to 3 times, backing off
200 ms × 2ⁿ with jitter, or honouring `Retry-After` (max 30 s).

When dependencies still fail, all of them are reported together:

```
Error: 2 dependencies failed:
  ✗ redis 18.1.0 (oci://ghcr.io/acme/charts): 401 unauthorized; run 'helm registry login ghcr.io'
  ✗ nginx 9.9.9 (oci://ghcr.io/acme/charts): 404 not found
```

| Message | Cause | Fix |
|---|---|---|
| `401 unauthorized; run 'helm registry login <host>'` | No or expired credentials | `helm registry login <host>` |
| `403 forbidden` | Credentials lack pull access | Check repository permissions |
| `404 not found` | Version tag does not exist | Fix the version, or `forge dep update` |
| `… is not a Helm chart (layer media types: …)` | The tag is a container image or other artifact | Point the dependency at a chart |
| `downloaded content did not match its digest twice` | Corruption or a proxy rewriting responses | Check proxies; retry |
| `can't get a valid version for N subchart(s)` | No tag satisfies the range | forge lists up to 5 available versions; adjust the constraint |
| `forge supports only oci:// dependencies` | A non-OCI repository in `Chart.yaml` | Use `helm dependency build` for that chart |
| `timed out after 5m0s` | `--timeout` reached | Raise `--timeout` or check the network |

## Caching in CI

The cache is safe to share between concurrent forge processes and to restore
from a CI cache. Example for GitHub Actions:

```yaml
- id: forge-cache
  run: echo "dir=$(forge cache path)" >> "$GITHUB_OUTPUT"
- uses: actions/cache@v4
  with:
    path: ${{ steps.forge-cache.outputs.dir }}
    key: forge-${{ hashFiles('**/Chart.lock') }}
    restore-keys: forge-
- run: forge dep build ./charts/my-app
```

With a warm cache, `forge dep build` makes no registry requests at all.
Cached tag→digest mappings are trusted; pass `--refresh` if tags in your
registry are overwritten (e.g. re-pushed `1.0.0`).
