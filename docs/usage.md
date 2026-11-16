# Usage

## Install

### Helm plugin (recommended)

You need Helm 3.18+ or Helm 4. The plugin works on macOS, Linux and Windows
(amd64/arm64). On macOS and Linux, the install hook needs `curl` or `wget`,
and `sha256sum` or `shasum`. On Windows, it needs PowerShell.

**1. Install** a pinned release tag:

=== "Helm 4"

    ```sh
    helm plugin install https://github.com/mmpyro/forge --version v2.1.0 --verify=false
    ```

=== "Helm 3 (3.18+)"

    ```sh
    helm plugin install https://github.com/mmpyro/forge --version v2.1.0
    ```

Helm clones the repository at that tag. Then it runs the install hook
(`scripts/install-plugin.sh`, or `install-plugin.ps1` on Windows). The hook
does these steps:

1. It reads `version` from `plugin.yaml`.
2. It downloads `forge-<os>-<arch>` and `checksums.txt` from that GitHub
   release.
3. It checks the sha256 of the binary. If the checksum does not match, the
   install stops.
4. It puts the binary in `$HELM_PLUGIN_DIR/bin/` and prints
   `forge: installed …`.

**2. Verify:**

```sh
helm plugin list              # NAME  VERSION  DESCRIPTION → forge  2.1.0  …
helm forge --version
helm forge cache path         # $HELM_CACHE_HOME/forge
```

**3. Use** `helm forge` in all places where you used `helm dependency`:

```sh
helm forge dep update ./my-chart
helm forge dep build  ./my-chart --timeout 2m
helm template my-release ./my-chart
```

Notes:

- `helm forge …` takes the same commands and flags as `forge …`.
- By default, Helm 4 verifies plugin signatures. It cannot verify a git
  source, so it needs `--verify=false`. Helm 3 does not have this flag.
- Always give `--version`. Without it, Helm installs from `main`. The
  `plugin.yaml` on `main` can name a release that is not published yet. Plugin
  installs are available from v2.0.0.
- Helm gives its settings to the plugin. `--registry-config` /
  `HELM_REGISTRY_CONFIG` and `--repository-config` / `HELM_REPOSITORY_CONFIG`
  work as they do in Helm. Thus credentials from `helm registry login` and
  `helm repo add`, and `@name` repositories, work without more setup. The
  default cache is `$HELM_CACHE_HOME/forge` (see [Environment](#environment)).
- To download from a mirror of the GitHub releases, set
  `HELM_FORGE_PLUGIN_URL`. Its value replaces
  `https://github.com/mmpyro/forge/releases/download`, and the hook adds
  `/v<version>/<asset>`. Use it behind a proxy or on an air-gapped network:

  ```sh
  HELM_FORGE_PLUGIN_URL=https://mirror.example.com/forge \
    helm plugin install https://github.com/mmpyro/forge --version v2.1.0
  ```

**Upgrade or remove.** The plugin stays at the tag that you installed. To
change the version, install the plugin again:

```sh
helm plugin uninstall forge
helm plugin install https://github.com/mmpyro/forge --version vX.Y.Z   # + --verify=false on Helm 4
```

A reinstall keeps the cache in `$HELM_CACHE_HOME/forge`. To remove the cache,
run `helm forge cache clean` before you uninstall the plugin.

**Install troubleshooting:**

| Symptom | Cause | Fix |
|---|---|---|
| Signature or verification error (Helm 4) | By default, Helm 4 verifies signatures | Add `--verify=false` |
| `forge: download failed: …/v<version>/forge-…` | The release is not published, or there is no network access to GitHub | Give the `--version` of a published release, or set `HELM_FORGE_PLUGIN_URL` to a mirror |
| `forge: checksum mismatch for …` | The download is corrupt, or a proxy changed it | Try again and examine your proxies. The hook did not install anything |
| `forge: unsupported OS` / `architecture` | The system is not macOS, Linux or Windows on amd64/arm64 | Build from source (see below) |
| `plugin already exists` | An earlier install is still there | Run `helm plugin uninstall forge` first |
| The hook does not run (Helm < 3.18) | `platformHooks` needs Helm 3.18+ | Upgrade Helm, or use the standalone binary |

**Move from Homebrew.** The Homebrew tap is discontinued. Remove it and
install the plugin:

```sh
brew uninstall helm-forge && brew untap mmpyro/forge
```

### Prebuilt binaries

Each [GitHub release](https://github.com/mmpyro/forge/releases) has prebuilt
binaries and a `checksums.txt` file. The links below go to
[v2.1.0](https://github.com/mmpyro/forge/releases/tag/v2.1.0)
([`checksums.txt`](https://github.com/mmpyro/forge/releases/download/v2.1.0/checksums.txt)).

| Platform | Asset | Download |
|---|---|---|
| macOS (Apple Silicon) | `forge-darwin-arm64` | [Download](https://github.com/mmpyro/forge/releases/download/v2.1.0/forge-darwin-arm64) |
| macOS (Intel) | `forge-darwin-amd64` | [Download](https://github.com/mmpyro/forge/releases/download/v2.1.0/forge-darwin-amd64) |
| Linux (x86_64) | `forge-linux-amd64` | [Download](https://github.com/mmpyro/forge/releases/download/v2.1.0/forge-linux-amd64) |
| Linux (ARM64) | `forge-linux-arm64` | [Download](https://github.com/mmpyro/forge/releases/download/v2.1.0/forge-linux-arm64) |
| Windows (x86_64) | `forge-windows-amd64.exe` | [Download](https://github.com/mmpyro/forge/releases/download/v2.1.0/forge-windows-amd64.exe) |
| Windows (ARM64) | `forge-windows-arm64.exe` | [Download](https://github.com/mmpyro/forge/releases/download/v2.1.0/forge-windows-arm64.exe) |

=== "Binary (macOS / Linux)"

    ```sh
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    arch=$(uname -m); case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
    curl -fsSL -o forge "https://github.com/mmpyro/forge/releases/download/v2.1.0/forge-$os-$arch"
    chmod +x forge && sudo mv forge /usr/local/bin/
    forge --version
    ```

=== "Binary (Windows)"

    Download the `.exe` file. Rename it to `forge.exe` and put it in a
    directory on your `PATH`.

### From source

Build from source (Go 1.25+):

```sh
make build            # → bin/forge, version from `git describe`
bin/forge --version
```

You can also build without the Makefile:

```sh
go build -o bin/forge ./cmd/forge
```

Put `bin/forge` on your `PATH`. You still need Helm for all tasks other than
the dependency fetch.

## Commands

### `forge dep update [CHART]`

This command resolves the constraints in `Chart.yaml`. It downloads the charts
into `charts/` and writes `Chart.lock`.

- The default `CHART` is `.`.
- forge locks exact versions (`1.2.0`) as written and does not ask the
  registry. Helm does the same.
- For ranges (`^1.0.0`, `>=0.1.0 <1.0.0`), forge selects the highest tag that
  matches. It fetches the tag list of each repository one time, also when
  many dependencies use that repository.
- forge writes `Chart.lock` again only when its digest changes. If you run
  `update` again and nothing is new, the file (and its `generated:` time) does
  not change.

### `forge dep build [CHART]`

This command downloads exactly the versions that `Chart.lock` pins.

- If there is no `Chart.lock`, it does the same as `dep update`. Helm does the
  same.
- If `Chart.lock` does not match `Chart.yaml`, the command fails with Helm's
  message: *"the lock file (Chart.lock) is out of sync with the dependencies
  file (Chart.yaml). Please update the dependencies"*. To fix it, run
  `forge dep update`.

### `forge dep tree [CHART]`

This command shows each dependency in `Chart.yaml`. For each one, it shows the
constraint, the version that `Chart.lock` pins and the source. It also shows
if `dep build` can get the dependency from the cache. The command reads only
`Chart.yaml`, `Chart.lock`, `repositories.yaml` and the cache. It sends **no
network requests**.

```
my-chart 1.0.0
├── redis (cache)  ^18.0.0  ->  18.1.0  oci://ghcr.io/acme/charts   104 KiB  cached
├── nginx          >=1 <2   ->  1.27.3  https://charts.example.com  -        not cached
└── common         ~0.3.0   ->  0.3.0   file://../common            -        local
```

The states are:

- `cached`: `dep build` sends no request for it.
- `not cached`: `dep build` downloads it.
- `local`: a `file://` chart. forge packages it on each run.
- `no lock`: there is no `Chart.lock` entry. Run `forge dep update`.

If `Chart.lock` does not match `Chart.yaml`, the command shows this below the
tree. It then matches the entries by name and repository. The command shows
only the dependencies of the chart, not their dependencies.

### `forge dep why NAME [CHART]`

This command explains one dependency. Give the chart name or the alias. If you
give the chart name, the command explains all aliases of that chart. It shows
the `Chart.yaml` entry (constraint, repository, condition, tags) and the
locked version. It also shows the cached archive (digest, size, fetch time,
last use, path) and if `charts/` has the archive. It sends no network
requests.

```
redis (cache)
  Chart.yaml:  ^18.0.0 from oci://ghcr.io/acme/charts
  condition:   cache.enabled
  Chart.lock:  18.1.0
  cache:       cached, sha256:…, 104 KiB, fetched 2026-10-01T09:12:44Z, last used 3h ago
               /home/me/.cache/helm-forge/blobs/sha256/…
  charts/:     redis-18.1.0.tgz (present)
```

An unknown name is a usage error (exit code `2`). The error lists the
dependencies of the chart.

### `forge dep outdated [CHART]`

This command compares `Chart.lock` with the versions that registries and chart
repositories offer now. Like `dep update`, it sends one `tags/list` request
for each OCI repository and gets one `index.yaml` for each chart repository.
It downloads no charts and writes no files.

```
NAME           CONSTRAINT  LOCKED  WANTED  LATEST  STATE
redis (cache)  ^18.0.0     18.1.0  18.4.2  19.0.1  update in constraint
nginx          1.27.3      1.27.3  1.27.3  1.28.0  newer outside constraint
common         ~0.3.0      0.3.0   0.3.0   0.3.0   up to date
2 of 3 dependencies are not up to date
```

- `WANTED` is the version that `forge dep update` would lock now. `LATEST` is
  the highest available version.
- The states are:
  - `up to date`
  - `update in constraint`: `dep update` would select `WANTED`.
  - `newer outside constraint`: to get `LATEST`, make the constraint in
    `Chart.yaml` wider.
  - `no match`: no version satisfies the constraint now.
  - `no lock`: there is no `Chart.lock` entry.
- A prerelease counts as `LATEST` only with `--include-prerelease`, or when
  the constraint itself selects a prerelease.

| Flag | Default | Meaning |
|---|---|---|
| `--include-prerelease` | `false` | Count prerelease versions as the latest |
| `--fail-if-outdated` | `false` | Exit with code `1` if a dependency is not `up to date` (JSON: `success: false`, error code `outdated`) |

The command also takes `--concurrency`, `--per-host`, `--plain-http`,
`--timeout`, `--registry-config` and `--repository-config`, as `dep update`
does. If forge cannot list a repository, the full command fails. It gives the
same classified error as `dep update`.

### JSON output of `tree`, `why` and `outdated`

With `-o json`, the three commands print one document. The document has
`schemaVersion`, `command`, `chart` (the argument), `success`, `chartName`,
`chartVersion`, `hasLock`, `lockInSync` and `dependencies`. Each dependency
has `name`, `alias`, `constraint`, `repository`, `condition`, `tags`,
`locked`, `state` (`cached`, `not_cached`, `local`, `no_lock`), `digest`,
`sizeBytes`, `fetchedAt`, `lastUsed`, `blobPath`, `fileName` and `inCharts`.

`dep why` lists only the dependencies that match. `dep outdated` adds
`durationMs`, `summary` (`total`, `outdated`) and `registries` (as for
`dep build`). For each dependency, it also adds `wanted`, `latest` and
`freshness` (`up_to_date`, `update_in_constraint`,
`newer_outside_constraint`, `no_match`, `no_lock`).

To use it as a CI gate:

```sh
forge dep outdated ./my-chart -o json > outdated.json || true
jq -r '.dependencies[] | select(.freshness != "up_to_date") | "\(.name): \(.locked) → \(.latest) (\(.freshness))"' outdated.json
```

`dependency` is an alias of `dep`. Thus `forge dependency build` also works.

### `forge cache path`

This command prints the cache directory. Use it in CI cache steps.

### `forge cache clean`

This command deletes the cache directory.

### `forge cache ls`

This command lists all cached charts. For each chart, it shows the name,
version, size, last use and source. It sends no requests. If the cache does
not exist, the command does not create it.

```
CHART  VERSION  SIZE     LAST-USED  SOURCE
dep-a  1.0.0    2.0 KiB  3h ago     oci://ghcr.io/acme/charts
nginx  1.27.3   41 KiB   12d ago    https://charts.example.com
2 charts, 43 KiB in /home/me/.cache/helm-forge
```

| Flag | Default | Meaning |
|---|---|---|
| `--sort` | `name` | `name`, `size` (largest first) or `age` (least recently used first) |
| `-o`, `--output` | `text` | `text` or `json` |

If the archive of a chart is not there, the size column shows `missing`.
Many charts can share one archive (for example, the same chart in two
registries). The total counts that archive one time.

### `forge cache gc`

This command removes charts from the cache. It removes the least recently used
charts first:

```sh
forge cache gc --older-than 30d          # not used for 30 days
forge cache gc --max-size 2GiB           # keep at most 2 GiB
forge cache gc --older-than 30d --max-size 2GiB --dry-run
```

1. It removes the charts that were not used within `--older-than`.
2. Then, while the cache is larger than `--max-size`, it removes the least
   recently used archives. It also removes all charts that point to these
   archives.
3. It always removes entries that point to a missing archive, entries that it
   cannot read, and archives that no entry points to.

"Used" means fetched **or served from the cache**. Each cache hit in
`dep build` or `dep update` marks the chart as used. Thus the cache keeps the
charts that a CI job needs each day, also if forge downloaded them long ago.

| Flag | Default | Meaning |
|---|---|---|
| `--older-than` | None | Remove charts that were not used for this time: `30d`, `2w`, `12h`, `90m` |
| `--max-size` | None | Keep at most this size: bytes, `500MB` (powers of 1000) or `2GiB` (powers of 1024) |
| `--grace` | `1h` | Never remove items that were used or written within this time. Thus a `dep build` that runs at the same time keeps its charts |
| `--dry-run` | `false` | Print what the command would remove. Do not remove anything |
| `-v`, `--verbose` | `false` | List the removed charts |
| `-o`, `--output` | `text` | `text` or `json` |

You must give `--older-than`, `--max-size` or both. If you give neither, the
exit code is `2`. To remove all of the cache, use `forge cache clean`. `gc`
takes no lock. You can run it while other forge processes use the cache. If
`gc` removes an archive that a run needs, the run downloads it again.

The freed space is the size of the removed archives. An archive can be
hardlinked into the `charts/` of a chart. That copy uses disk space until you
also delete it.

With `-o json`, both commands print one document. It has `schemaVersion`,
`command` (`cache ls` / `cache gc`), `cache` (the directory) and `success`.
`cache ls` adds `summary` (`charts`, `sizeBytes`) and `entries`. `cache gc`
adds `dryRun`, `summary` (`chartsRemoved`, `blobsRemoved`, `bytesFreed`,
`blobsKept`, `bytesKept`) and `removed`. Each entry has `name`, `version`,
`repository`, `source` (`oci` or `repo`), `state` (`ok`, `missing` or
`invalid`), `digest`, `sizeBytes`, `lastUsed` and `fetchedAt`.

## Flags (`dep build` and `dep update`)

| Flag | Default | Meaning |
|---|---|---|
| `--concurrency` | `16` | Maximum number of requests in flight in total |
| `--per-host` | `8` | Maximum number of requests in flight for each host |
| `--refresh` | `false` | Ask the registries and repositories for the versions again. Do not trust the cached version→digest mapping |
| `--plain-http` | `false` | Use plain HTTP for OCI registries (for example, a local `registry:2`). Chart repositories use the scheme in their URL |
| `--timeout` | `5m` | Time limit for the full run |
| `--registry-config` | Helm's path | Helm's registry credentials file |
| `--repository-config` | Helm's path | Helm's `repositories.yaml` (`@name` repositories, credentials, TLS) |
| `-o`, `--output` | `text` | `text` or `json` (see [JSON output](#json-output)) |

The timeout for each request is 60 s. forge tries failed requests again (see
[Errors and retries](#errors-and-retries)).

## Environment

| Variable | Effect |
|---|---|
| `HELM_FORGE_CACHE` | Cache root. The default is `~/.cache/helm-forge`, or `$HELM_CACHE_HOME/forge` when you run `helm forge` |
| `HELM_REGISTRY_CONFIG` | Credentials file, as in Helm. The default is Helm's `registry/config.json` |
| `HELM_REPOSITORY_CONFIG` | Chart repositories file, as in Helm. The default is Helm's `repositories.yaml` |
| `HELM_FORGE_PLUGIN_URL` | Only for the plugin install hook: the base URL for release downloads (a mirror) |

## Authentication

forge reads credentials only from the files of Helm. Log in as you do for
Helm.

OCI registries use Helm's registry config:

```sh
helm registry login ghcr.io
forge dep build ./my-chart
```

Chart repositories use `repositories.yaml`. forge applies its username and
password, CA file, client certificate and `insecure_skip_tls_verify` to each
dependency whose URL matches the repository. It also applies them to `@name`
references:

```sh
helm repo add acme https://charts.acme.example --username me --password-stdin
forge dep build ./my-chart
```

As in Helm, forge sends credentials only to the scheme and host of the
repository. If `index.yaml` points archives to a different host, add
`--pass-credentials` to `helm repo add`. forge then sends the credentials to
that host too.

`helm dependency build` needs `helm repo add` for a chart repository URL.
forge does not. If there is no entry for the URL, forge uses anonymous access.
If the file does not exist, forge uses anonymous access for all repositories.

## Output

If the run succeeds, forge prints one line:

```
Saved 12 charts (9 cached, 3 downloaded) in 412ms
```

Each alias of a chart counts separately in `Saved`, but forge downloads the
archive only one time. If forge packages charts from `file://` directories, it
adds `, N local` to the counts.

### JSON output

With `-o json`, `dep build` and `dep update` print one JSON document on
stdout. This document replaces the `Saved …` line. The commands print it when
the run succeeds and when it fails. Errors still go to stderr as text. Thus
`forge dep build -o json | jq` always gets valid JSON. The exit codes do not
change.

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

1.  This number goes up only for changes that can break a consumer. New fields
    can appear without a change to this number.
2.  `true` only when the exit code is `0`. Use `jq -e .success` as the CI gate.
3.  The number of `dependencies` for each status. `saved` = `cached` +
    `downloaded` + `local`. forge updates `charts/` only when `success` is
    `true`.
4.  `true` when forge wrote `Chart.lock`. `dep update` writes it, and
    `dep build` writes it when there is no lock.
5.  One entry for each `Chart.yaml` dependency, in order. It is empty when the
    run stopped before it resolved the versions (for example, when
    `Chart.lock` is out of sync).
6.  The version exactly as written in `Chart.yaml`. `version` is the locked
    version.
7.  `cached`: served from the cache without a request. `downloaded`: needed
    one or more requests. See the [status table](#status-values).
8.  Packaged from a `file://` directory on each run, with no cache and no
    request. `file://` dependencies have `sizeBytes` but no `digest`.
9.  Present when `status` is `failed`. Match on `code`, never on `message`.
    See the [error codes](#error-codes).
10. One entry for each host that forge contacted, sorted by host. `requests`
    counts all HTTP requests, retries included. `retries` counts the repeats
    after 408, 429, 5xx and network errors. `authRounds` counts the `401`
    challenges. A warm run has no entries.

| Field | Meaning |
|---|---|
| `schemaVersion` | `1`. It goes up only for changes that can break a consumer. New fields can appear without a change to this number |
| `command` | `dep build` or `dep update` |
| `chart` | The `CHART` argument as given (default `.`) |
| `success` | `true` only when the exit code is `0` |
| `durationMs` | Wall time of the run |
| `summary` | The number of `dependencies` for each status. `saved` = `cached` + `downloaded` + `local`. forge updates `charts/` only when `success` is `true` |
| `lockWritten` | `true` when forge wrote `Chart.lock` (`dep update`, or `dep build` without a lock) |
| `dependencies[]` | One entry for each `Chart.yaml` dependency, in order. Empty when the run stopped before it resolved the versions (for example, `Chart.lock` out of sync) |
| `dependencies[].alias`, `.constraint` | From `Chart.yaml`. `constraint` is the version as written |
| `dependencies[].version` | The locked version. Not present when the resolve step failed |
| `dependencies[].digest`, `.sizeBytes` | The digest and size of the chart archive. `file://` dependencies have only `sizeBytes` |
| `dependencies[].placement` | How forge put the archive into `charts/`: `reflink`, `hardlink` or `copy`. Present only when forge updated `charts/` |
| `dependencies[].durationMs` | The time to fetch this chart (or, for `file://`, to package it). Aliases of one chart share this value. Not present when forge did not try a fetch |
| `dependencies[].error` | Present when `status` is `failed` |
| `registries[]` | One entry for each host that forge contacted (OCI registries and chart repositories), sorted by host. `requests` counts all HTTP requests, retries included. `retries` counts the repeats after 408, 429, 5xx and network errors. `authRounds` counts the `401` challenges. Token servers and blob-redirect hosts have their own entries. A warm run has no entries |
| `error` | Present for failures that no single dependency explains. Examples: a lock out of sync, a timeout, or a `Chart.yaml` that forge cannot read |

#### Status values

`status` has one of these values:

| Status | Meaning |
|---|---|
| `cached` | Served from the cache without a request |
| `downloaded` | Needed one or more requests |
| `local` | Packaged from a `file://` directory (on each run, with no cache and no request) |
| `failed` | See `error` |
| `skipped` | Not tried, because the run stopped first. Another dependency had no matching version or an unsupported repository |

#### Error codes

Each `error` object has a stable `code` and a `message`. It can also have
`httpStatus` and `hint`. Match on `code`, not on `message`:

| Code | Meaning |
|---|---|
| `unauthorized` | `401`: the credentials are missing or expired |
| `forbidden` | `403`: the credentials do not give pull access |
| `not_found` | `404`: the version tag or archive does not exist, or the chart repository index does not have that chart or version |
| `not_a_chart` | The tag is a container image or a different artifact |
| `digest_mismatch` | Two times, the downloaded content did not match its digest |
| `no_matching_version` | No tag, index entry or local `file://` chart satisfies the constraint. `hint` lists the nearest versions |
| `unsupported_repository` | The repository is not `oci://`, `http(s)://`, `file://` or `@alias` |
| `timeout` | The run reached `--timeout`, or a request timed out |
| `lock_out_of_sync` | `Chart.lock` does not match `Chart.yaml` (top-level `error` only) |
| `interrupted` | SIGINT or SIGTERM (top-level `error` only) |
| `outdated` | `dep outdated --fail-if-outdated` found a dependency that is not up to date (top-level `error` only) |
| `usage` | Usage error, exit code `2` (see below) |
| `unknown` | All other errors. Read `message` |

A usage error with `-o json` prints a smaller document:

```json
{
  "schemaVersion": 1,
  "command": "dep build",
  "success": false,
  "error": { "code": "usage", "message": "accepts at most 1 arg(s), received 2", "hint": "run 'forge dep build --help'" }
}
```

To show failures as GitHub Actions annotations:

```sh
forge dep build -o json ./my-chart > forge.json || true
jq -r '.dependencies[] | select(.status == "failed")
  | "::error title=forge: \(.name)::\(.error.code): \(.error.message). \(.error.hint // "")"' forge.json
jq -e .success forge.json > /dev/null
```

## What ends up in `charts/`

- One `<name>-<version>.tgz` for each dependency. The bytes are the same as
  the file that Helm downloads. Archives from chart repositories keep the file
  name of their URL in `index.yaml`, as in Helm.
- forge packages `file://` dependencies from their directory on each run. It
  uses Helm's own packaging code, with no cache and no network. The contents
  of the archive match Helm's. `dep build` fails if the chart version in the
  directory does not match `Chart.lock`.
- forge removes stale chart archives (archives that are no longer a
  dependency), as `helm dep build` does. It keeps files that are not charts
  and unpacked subchart directories.
- forge puts the archives from the cache into `charts/` with a reflink, a
  hardlink or a copy, and tries the cheapest method first. A hardlinked
  archive is read-only (`0444`). This is correct.

If a run fails, `charts/` does not change.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | A failure. forge lists each failed dependency. This code also means that `dep outdated --fail-if-outdated` found outdated dependencies |
| `2` | Usage error (bad flag, too many arguments, `--concurrency 0`, unknown name for `dep why`, `cache gc` without limits, …) |

## Errors and retries

forge tries `408`, `429`, `5xx` and network errors again, up to 3 times. The
wait time is 200 ms × 2ⁿ with jitter. If the server sends `Retry-After`, forge
uses that value (max 30 s).

If dependencies still fail, forge reports all of them together:

```
Error: 2 dependencies failed:
  ✗ redis 18.1.0 (oci://ghcr.io/acme/charts): 401 unauthorized; run 'helm registry login ghcr.io'
  ✗ nginx 9.9.9 (oci://ghcr.io/acme/charts): 404 not found
```

| Message | Cause | Fix |
|---|---|---|
| `401 unauthorized; run 'helm registry login <host>'` | The credentials are missing or expired | `helm registry login <host>` |
| `403 forbidden` | The credentials do not give pull access | Examine the repository permissions |
| `404 not found` | The version tag does not exist | Correct the version, or run `forge dep update` |
| `… is not a Helm chart (layer media types: …)` | The tag is a container image or a different artifact | Point the dependency to a chart |
| `downloaded content did not match its digest twice` | Corrupt data, or a proxy that changes responses | Examine your proxies and try again |
| `can't get a valid version for N subchart(s)` | No tag satisfies the range | forge lists up to 5 available versions. Change the constraint |
| `401 unauthorized; add credentials with 'helm repo add …'` | The chart repository needs credentials | `helm repo add <name> <url> --username …` |
| `404 not found: <url>` | An archive in `index.yaml` is missing | Correct the repository, or run `forge dep update` |
| `<chart> chart not found in repo <url>` | `index.yaml` does not have that chart | Correct the name or the repository |
| `no repository definition for @name` | `@name` or `alias:name` is not in `repositories.yaml` | `helm repo add name <url>` |
| `directory … not found` | The `file://` path does not exist (the path is relative to the chart) | Correct the path |
| `can't get a valid version for dependency <name>` | The version of the `file://` chart changed after `Chart.lock` was written | `forge dep update` |
| `forge supports oci://, http(s)://, file:// and @alias repositories` | There is no `repository`, or it has a different scheme | Use `helm dependency build` for that chart |
| `timed out after 5m0s` | The run reached `--timeout` | Increase `--timeout`, or examine the network |

## Caching in CI

Concurrent forge processes can share the cache safely. You can also restore
the cache from a CI cache. The example below is for GitHub Actions with the
Helm plugin. The plugin keeps its cache in the cache directory of Helm, so get
the path from `helm forge cache path`:

```yaml
- uses: azure/setup-helm@v4
- run: helm plugin install https://github.com/mmpyro/forge --version v2.1.0 --verify=false
- id: forge-cache
  run: echo "dir=$(helm forge cache path)" >> "$GITHUB_OUTPUT"
- uses: actions/cache@v4
  with:
    path: ${{ steps.forge-cache.outputs.dir }}
    key: forge-${{ hashFiles('**/Chart.lock') }}
    restore-keys: forge-
- run: helm forge dep build ./charts/my-app -o json > forge.json
# Keep the saved cache bounded: drop what this and earlier jobs stopped using.
- run: helm forge cache gc --older-than 30d --max-size 2GiB
```

If the runner has Helm 3, remove `--verify=false`. With the standalone binary,
use `forge cache path` and `forge dep build`. To show a failed run as
annotations, see [JSON output](#json-output).

With a warm cache, `forge dep build` sends no requests. (forge packages
`file://` dependencies locally, without the network.) forge trusts the cached
tag→digest mappings. If your registry overwrites tags (for example, a
re-pushed `1.0.0`), use `--refresh`.
