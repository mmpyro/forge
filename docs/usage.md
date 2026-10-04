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
| `-o`, `--output` | `text` | `text` or `json` (see [JSON output](#json-output)) |

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

### JSON output

With `-o json`, `dep build` and `dep update` print one JSON document on
stdout instead of the `Saved …` line. They print it on success and on failure. Errors still go to
stderr as text, so `forge dep build -o json | jq` always gets valid JSON.
Exit codes do not change.

```json
{
  "schemaVersion": 1,
  "command": "dep build",
  "chart": "./my-chart",
  "success": false,
  "durationMs": 412,
  "summary": { "saved": 1, "cached": 1, "downloaded": 0, "failed": 1, "skipped": 0 },
  "lockWritten": false,
  "dependencies": [
    {
      "name": "redis",
      "alias": "cache",
      "version": "18.1.0",
      "constraint": "^18.0.0",
      "repository": "oci://ghcr.io/acme/charts",
      "status": "cached",
      "digest": "sha256:…",
      "sizeBytes": 104857,
      "durationMs": 3
    },
    {
      "name": "nginx",
      "version": "9.9.9",
      "constraint": "9.9.9",
      "repository": "oci://ghcr.io/acme/charts",
      "status": "failed",
      "durationMs": 41,
      "error": {
        "code": "not_found",
        "httpStatus": 404,
        "message": "404 not found",
        "hint": "Fix the version, or run 'forge dep update'"
      }
    }
  ],
  "registries": [
    { "host": "ghcr.io", "requests": 3, "retries": 0, "authRounds": 1 }
  ]
}
```

| Field | Meaning |
|---|---|
| `schemaVersion` | `1`. It is raised only for changes that could break a consumer. New fields can be added without raising it |
| `command` | `dep build` or `dep update` |
| `chart` | The `CHART` argument as given (default `.`) |
| `success` | `true` exactly when the exit code is `0` |
| `durationMs` | Wall time of the run |
| `summary` | Counts of `dependencies` by status. `saved` = `cached` + `downloaded`, meaning fetched into the cache. `charts/` is updated only when `success` is `true` |
| `lockWritten` | `true` when `Chart.lock` was written (by `dep update`, or by `dep build` without a lock) |
| `dependencies[]` | One per `Chart.yaml` dependency, in order. Empty when the run stopped before resolving, e.g. `Chart.lock` out of sync |
| `dependencies[].alias`, `.constraint` | From `Chart.yaml`. `constraint` is the version as written |
| `dependencies[].version` | The locked version. Missing when resolving failed |
| `dependencies[].digest`, `.sizeBytes` | The chart archive layer's digest and size |
| `dependencies[].placement` | How the archive got into `charts/`: `reflink`, `hardlink` or `copy`. Present only when `charts/` was updated |
| `dependencies[].durationMs` | Time spent fetching this chart (aliases of one chart share it). Missing when no fetch was attempted |
| `dependencies[].error` | Present when `status` is `failed` |
| `registries[]` | One per host contacted, sorted by host. `requests` counts every HTTP request, retries included. `retries` counts repeats after 408/429/5xx/network errors. `authRounds` counts `401` challenges. Token servers and blob-redirect hosts appear as hosts of their own. A warm run has none |
| `error` | Present for failures that no single dependency explains, e.g. a lock out of sync, a timeout, or an unreadable `Chart.yaml` |

`status` is one of:

| Status | Meaning |
|---|---|
| `cached` | Served from the cache without any request |
| `downloaded` | Needed at least one registry request |
| `failed` | See `error` |
| `skipped` | Not attempted because the run stopped first (another dependency had no matching version or an unsupported repository) |

Every `error` object has a stable `code`, a `message`, and optionally `httpStatus`
and `hint`. Match on `code`, not on `message`:

| Code | Meaning |
|---|---|
| `unauthorized` | `401`: no or expired credentials |
| `forbidden` | `403`: credentials lack pull access |
| `not_found` | `404`: the version tag does not exist |
| `not_a_chart` | The tag is a container image or another artifact |
| `digest_mismatch` | Downloaded content did not match its digest twice |
| `no_matching_version` | No tag satisfies the constraint. `hint` lists the nearest versions |
| `unsupported_repository` | Not an `oci://` repository |
| `timeout` | `--timeout` reached, or a request timed out |
| `lock_out_of_sync` | `Chart.lock` does not match `Chart.yaml` (top-level `error` only) |
| `interrupted` | SIGINT/SIGTERM (top-level `error` only) |
| `usage` | Usage error, exit code `2` (see below) |
| `unknown` | Anything else. Read `message` |

A usage error with `-o json` prints a smaller document:

```json
{
  "schemaVersion": 1,
  "command": "dep build",
  "success": false,
  "error": { "code": "usage", "message": "accepts at most 1 arg(s), received 2", "hint": "run 'forge dep build --help'" }
}
```

To turn failures into GitHub Actions annotations:

```sh
forge dep build -o json ./my-chart > forge.json || true
jq -r '.dependencies[] | select(.status == "failed")
  | "::error title=forge: \(.name)::\(.error.code): \(.error.message). \(.error.hint // "")"' forge.json
jq -e .success forge.json > /dev/null
```

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
