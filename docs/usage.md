# Usage

## Install

### Helm plugin (recommended)

Needs Helm 3.18+ or Helm 4. Works on macOS, Linux and Windows (amd64/arm64).
The install hook needs `curl` or `wget` and `sha256sum` or `shasum` (macOS and
Linux), or PowerShell (Windows).

**1. Install**, pinned to a release tag:

=== "Helm 4"

    ```sh
    helm plugin install https://github.com/mmpyro/forge --version v2.0.0 --verify=false
    ```

=== "Helm 3 (3.18+)"

    ```sh
    helm plugin install https://github.com/mmpyro/forge --version v2.0.0
    ```

Helm clones the repository at that tag, then runs the install hook
(`scripts/install-plugin.sh`, or `install-plugin.ps1` on Windows). The hook:

1. reads `version` from `plugin.yaml`;
2. downloads `forge-<os>-<arch>` and `checksums.txt` from that GitHub release;
3. checks the binary's sha256 and aborts the install on a mismatch;
4. places it in `$HELM_PLUGIN_DIR/bin/` and prints `forge: installed …`.

**2. Verify:**

```sh
helm plugin list              # NAME  VERSION  DESCRIPTION → forge  2.0.0  …
helm forge --version
helm forge cache path         # $HELM_CACHE_HOME/forge
```

**3. Use** `helm forge` wherever you used `helm dependency`:

```sh
helm forge dep update ./my-chart
helm forge dep build  ./my-chart --timeout 2m
helm template my-release ./my-chart
```

Notes:

- `helm forge …` takes the same commands and flags as `forge …`.
- Helm 4 verifies plugin signatures by default and can't verify a git source,
  so it needs `--verify=false`. Helm 3 has no such flag.
- Always pass `--version`: without it Helm installs from `main`, whose
  `plugin.yaml` may name a release that isn't published yet. Plugin installs
  are available from v2.0.0.
- Helm passes its settings to the plugin: `--registry-config` /
  `HELM_REGISTRY_CONFIG` and `--repository-config` / `HELM_REPOSITORY_CONFIG`
  work as with Helm itself, so `helm registry login` and `helm repo add`
  credentials and `@name` repositories just work. The cache defaults to
  `$HELM_CACHE_HOME/forge` (see [Environment](#environment)).
- Set `HELM_FORGE_PLUGIN_URL` to download from a mirror of the GitHub releases
  (it replaces `https://github.com/mmpyro/forge/releases/download`; the hook
  appends `/v<version>/<asset>`). Useful behind a proxy or air-gapped:

  ```sh
  HELM_FORGE_PLUGIN_URL=https://mirror.example.com/forge \
    helm plugin install https://github.com/mmpyro/forge --version v2.0.0
  ```

**Upgrade or remove.** The plugin is pinned to the tag it was installed from,
so move to another version by reinstalling:

```sh
helm plugin uninstall forge
helm plugin install https://github.com/mmpyro/forge --version vX.Y.Z   # + --verify=false on Helm 4
```

The cache in `$HELM_CACHE_HOME/forge` survives reinstalls; remove it with
`helm forge cache clean` before uninstalling if you want it gone.

**Install troubleshooting:**

| Symptom | Cause | Fix |
|---|---|---|
| Signature or verification error (Helm 4) | Helm 4 verifies signatures by default | Add `--verify=false` |
| `forge: download failed: …/v<version>/forge-…` | Release not published, or no network access to GitHub | Pass `--version` of a published release; or set `HELM_FORGE_PLUGIN_URL` to a mirror |
| `forge: checksum mismatch for …` | Corrupted download or a proxy rewriting it | Retry; check proxies. Nothing was installed |
| `forge: unsupported OS` / `architecture` | Not macOS/Linux/Windows on amd64/arm64 | Build from source (below) |
| `plugin already exists` | An earlier install is still there | `helm plugin uninstall forge` first |
| Hook not run (Helm < 3.18) | `platformHooks` needs Helm 3.18+ | Upgrade Helm, or use the standalone binary |

**Moving from Homebrew.** The Homebrew tap is discontinued. Remove it and
install the plugin:

```sh
brew uninstall helm-forge && brew untap mmpyro/forge
```

### Prebuilt binaries

Prebuilt binaries and `checksums.txt` are attached to every
[GitHub release](https://github.com/mmpyro/forge/releases). The links
below point at v2.0.0.

| Platform | Asset | Download |
|---|---|---|
| macOS (Apple Silicon) | `forge-darwin-arm64` | [Download](https://github.com/mmpyro/forge/releases/download/v2.0.0/forge-darwin-arm64) |
| macOS (Intel) | `forge-darwin-amd64` | [Download](https://github.com/mmpyro/forge/releases/download/v2.0.0/forge-darwin-amd64) |
| Linux (x86_64) | `forge-linux-amd64` | [Download](https://github.com/mmpyro/forge/releases/download/v2.0.0/forge-linux-amd64) |
| Linux (ARM64) | `forge-linux-arm64` | [Download](https://github.com/mmpyro/forge/releases/download/v2.0.0/forge-linux-arm64) |
| Windows (x86_64) | `forge-windows-amd64.exe` | [Download](https://github.com/mmpyro/forge/releases/download/v2.0.0/forge-windows-amd64.exe) |
| Windows (ARM64) | `forge-windows-arm64.exe` | [Download](https://github.com/mmpyro/forge/releases/download/v2.0.0/forge-windows-arm64.exe) |

=== "Binary (macOS / Linux)"

    ```sh
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    arch=$(uname -m); case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
    curl -fsSL -o forge "https://github.com/mmpyro/forge/releases/download/v2.0.0/forge-$os-$arch"
    chmod +x forge && sudo mv forge /usr/local/bin/
    forge --version
    ```

=== "Binary (Windows)"

    Download the `.exe`, rename it to `forge.exe` and put it in a
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
| `--refresh` | `false` | Re-check versions with registries and repositories instead of trusting the cached version→digest mapping |
| `--plain-http` | `false` | Use plain HTTP for OCI registries (e.g. a local `registry:2`). Chart repositories use the scheme in their URL |
| `--timeout` | `5m` | Time limit for the whole run |
| `--registry-config` | Helm's path | Helm's registry credentials file |
| `--repository-config` | Helm's path | Helm's `repositories.yaml` (`@name` repositories, credentials, TLS) |
| `-o`, `--output` | `text` | `text` or `json` (see [JSON output](#json-output)) |

Per-request timeout is 60 s; failed requests are retried (see
[Errors and retries](#errors-and-retries)).

## Environment

| Variable | Effect |
|---|---|
| `HELM_FORGE_CACHE` | Cache root. Default `~/.cache/helm-forge`, or `$HELM_CACHE_HOME/forge` when run as `helm forge` |
| `HELM_REGISTRY_CONFIG` | Credentials file, same as Helm. Default: Helm's `registry/config.json` |
| `HELM_REPOSITORY_CONFIG` | Chart repositories file, same as Helm. Default: Helm's `repositories.yaml` |
| `HELM_FORGE_PLUGIN_URL` | Plugin install hook only: base URL for release downloads (mirror) |

## Authentication

forge reads credentials only from Helm's own files, so log in the usual way.

OCI registries use Helm's registry config:

```sh
helm registry login ghcr.io
forge dep build ./my-chart
```

Chart repositories use `repositories.yaml`. Its username/password, CA file,
client certificate and `insecure_skip_tls_verify` apply to any dependency whose
URL matches the repository, and to `@name` references:

```sh
helm repo add acme https://charts.acme.example --username me --password-stdin
forge dep build ./my-chart
```

As in Helm, credentials go only to the repository's own scheme and host. If
`index.yaml` points archives at another host, add `--pass-credentials` to
`helm repo add` to send them there too.

Unlike `helm dependency build`, forge does not require `helm repo add` for a
chart repository URL; without an entry, access is anonymous. A missing file
means anonymous access everywhere.

## Output

On success forge prints one line:

```
Saved 12 charts (9 cached, 3 downloaded) in 412ms
```

Aliases of one chart count separately in `Saved`, but the archive is
downloaded only once. Charts packaged from `file://` directories add
`, N local` to the counts.

### JSON output

With `-o json`, `dep build` and `dep update` print one JSON document on
stdout instead of the `Saved …` line. They print it on success and on failure. Errors still go to
stderr as text, so `forge dep build -o json | jq` always gets valid JSON.
Exit codes do not change.

!!! tip "Interactive"
    On the docs site, click the :material-plus-circle: markers to see what each field means.

```json
{
  "schemaVersion": 1, // (1)!
  "command": "dep build",
  "chart": "./my-chart",
  "success": false, // (2)!
  "durationMs": 412,
  "summary": { "saved": 2, "cached": 1, "downloaded": 0, "local": 1, "failed": 1, "skipped": 0 }, // (3)!
  "lockWritten": false, // (4)!
  "dependencies": [ // (5)!
    {
      "name": "redis",
      "alias": "cache",
      "version": "18.1.0",
      "constraint": "^18.0.0", // (6)!
      "repository": "oci://ghcr.io/acme/charts",
      "status": "cached", // (7)!
      "digest": "sha256:…",
      "sizeBytes": 104857,
      "durationMs": 3
    },
    {
      "name": "common",
      "version": "0.3.0",
      "constraint": "~0.3.0",
      "repository": "file://../common",
      "status": "local", // (8)!
      "sizeBytes": 2048,
      "durationMs": 4
    },
    {
      "name": "nginx",
      "version": "9.9.9",
      "constraint": "9.9.9",
      "repository": "oci://ghcr.io/acme/charts",
      "status": "failed",
      "durationMs": 41,
      "error": { // (9)!
        "code": "not_found",
        "httpStatus": 404,
        "message": "404 not found",
        "hint": "Fix the version, or run 'forge dep update'"
      }
    }
  ],
  "registries": [ // (10)!
    { "host": "ghcr.io", "requests": 3, "retries": 0, "authRounds": 1 }
  ]
}
```

1.  Raised only for changes that could break a consumer. New fields can be
    added without raising it.
2.  `true` exactly when the exit code is `0`. Use `jq -e .success` as the CI gate.
3.  Counts of `dependencies` by status. `saved` = `cached` + `downloaded` + `local`.
    `charts/` is updated only when `success` is `true`.
4.  `true` when `Chart.lock` was written — by `dep update`, or by `dep build`
    without a lock.
5.  One entry per `Chart.yaml` dependency, in order. Empty when the run stopped
    before resolving, e.g. `Chart.lock` out of sync.
6.  The version exactly as written in `Chart.yaml`. `version` is what got locked.
7.  `cached`: served from the cache without any request. `downloaded`: needed at
    least one request. See the [status table](#status-values).
8.  Packaged from a `file://` directory on every run — no cache, no request.
    `file://` dependencies have `sizeBytes` but no `digest`.
9.  Present when `status` is `failed`. Match on `code`, never on `message`.
    See the [error codes](#error-codes).
10. One per host contacted, sorted by host. `requests` counts every HTTP request
    (retries included), `retries` repeats after 408/429/5xx/network errors,
    `authRounds` `401` challenges. A warm run has none.

| Field | Meaning |
|---|---|
| `schemaVersion` | `1`. It is raised only for changes that could break a consumer. New fields can be added without raising it |
| `command` | `dep build` or `dep update` |
| `chart` | The `CHART` argument as given (default `.`) |
| `success` | `true` exactly when the exit code is `0` |
| `durationMs` | Wall time of the run |
| `summary` | Counts of `dependencies` by status. `saved` = `cached` + `downloaded` + `local`. `charts/` is updated only when `success` is `true` |
| `lockWritten` | `true` when `Chart.lock` was written (by `dep update`, or by `dep build` without a lock) |
| `dependencies[]` | One per `Chart.yaml` dependency, in order. Empty when the run stopped before resolving, e.g. `Chart.lock` out of sync |
| `dependencies[].alias`, `.constraint` | From `Chart.yaml`. `constraint` is the version as written |
| `dependencies[].version` | The locked version. Missing when resolving failed |
| `dependencies[].digest`, `.sizeBytes` | The chart archive's digest and size. `file://` dependencies have only `sizeBytes` |
| `dependencies[].placement` | How the archive got into `charts/`: `reflink`, `hardlink` or `copy`. Present only when `charts/` was updated |
| `dependencies[].durationMs` | Time spent fetching (or, for `file://`, packaging) this chart (aliases of one chart share it). Missing when no fetch was attempted |
| `dependencies[].error` | Present when `status` is `failed` |
| `registries[]` | One per host contacted (OCI registries and chart repositories), sorted by host. `requests` counts every HTTP request, retries included. `retries` counts repeats after 408/429/5xx/network errors. `authRounds` counts `401` challenges. Token servers and blob-redirect hosts appear as hosts of their own. A warm run has none |
| `error` | Present for failures that no single dependency explains, e.g. a lock out of sync, a timeout, or an unreadable `Chart.yaml` |

#### Status values

`status` is one of:

| Status | Meaning |
|---|---|
| `cached` | Served from the cache without any request |
| `downloaded` | Needed at least one request |
| `local` | Packaged from a `file://` directory (every run; no cache, no request) |
| `failed` | See `error` |
| `skipped` | Not attempted because the run stopped first (another dependency had no matching version or an unsupported repository) |

#### Error codes

Every `error` object has a stable `code`, a `message`, and optionally `httpStatus`
and `hint`. Match on `code`, not on `message`:

| Code | Meaning |
|---|---|
| `unauthorized` | `401`: no or expired credentials |
| `forbidden` | `403`: credentials lack pull access |
| `not_found` | `404`: the version tag or archive does not exist, or the chart repository index has no such chart/version |
| `not_a_chart` | The tag is a container image or another artifact |
| `digest_mismatch` | Downloaded content did not match its digest twice |
| `no_matching_version` | No tag, index entry or local `file://` chart satisfies the constraint. `hint` lists the nearest versions |
| `unsupported_repository` | Not an `oci://`, `http(s)://`, `file://` or `@alias` repository |
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
  downloads. Chart repository archives keep the file name of their URL in
  `index.yaml`, as in Helm.
- `file://` dependencies are packaged from their directory on every run, with
  Helm's own packaging code (no cache, no network); the archive's contents
  match Helm's. `dep build` fails if the
  directory's chart version no longer matches `Chart.lock`.
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
| `401 unauthorized; add credentials with 'helm repo add …'` | Chart repository needs credentials | `helm repo add <name> <url> --username …` |
| `404 not found: <url>` | Archive listed in `index.yaml` is missing | Fix the repository, or `forge dep update` |
| `<chart> chart not found in repo <url>` | `index.yaml` has no such chart | Fix the name or repository |
| `no repository definition for @name` | `@name` / `alias:name` not in `repositories.yaml` | `helm repo add name <url>` |
| `directory … not found` | `file://` path does not exist (relative to the chart) | Fix the path |
| `can't get a valid version for dependency <name>` | `file://` chart's version changed since `Chart.lock` | `forge dep update` |
| `forge supports oci://, http(s)://, file:// and @alias repositories` | No `repository`, or another scheme | Use `helm dependency build` for that chart |
| `timed out after 5m0s` | `--timeout` reached | Raise `--timeout` or check the network |

## Caching in CI

The cache is safe to share between concurrent forge processes and to restore
from a CI cache. Example for GitHub Actions with the Helm plugin (its cache
lives under Helm's cache dir, so ask `helm forge cache path`):

```yaml
- uses: azure/setup-helm@v4
- run: helm plugin install https://github.com/mmpyro/forge --version v2.0.0 --verify=false
- id: forge-cache
  run: echo "dir=$(helm forge cache path)" >> "$GITHUB_OUTPUT"
- uses: actions/cache@v4
  with:
    path: ${{ steps.forge-cache.outputs.dir }}
    key: forge-${{ hashFiles('**/Chart.lock') }}
    restore-keys: forge-
- run: helm forge dep build ./charts/my-app -o json > forge.json
```

Drop `--verify=false` if the runner has Helm 3. With the standalone binary, use
`forge cache path` and `forge dep build` instead. To turn a failed run into
annotations, see [JSON output](#json-output).

With a warm cache, `forge dep build` makes no requests at all (`file://`
dependencies are packaged locally, which needs no network).
Cached tag→digest mappings are trusted; pass `--refresh` if tags in your
registry are overwritten (e.g. re-pushed `1.0.0`).
