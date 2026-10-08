---
hide:
  - navigation
  - toc
---

<div class="forge-hero" markdown>

![forge logo](images/logo.png){ width="160" }

# forge

**Fast, Helm-identical `helm dependency build` / `update`** for charts whose
dependencies live in OCI registries, classic chart repositories
(`https://…/index.yaml`) or local `file://` directories.

[Get started](#install){ .md-button .md-button--primary }
[Usage guide](usage.md){ .md-button }
[GitHub](https://github.com/mmpyro/forge){ .md-button }

</div>

forge writes the same `charts/*.tgz` and `Chart.lock` that Helm would, so
`helm template`, `helm install` and `helm package` keep working unchanged —
they just get their dependencies faster.

```sh
helm forge dep update ./my-chart   # resolve ranges, download, write Chart.lock
helm forge dep build  ./my-chart   # download exactly what Chart.lock pins
helm template my-release ./my-chart
```

`helm forge …` (the plugin) and `forge …` (the standalone binary) take the
same commands and flags.

## Why forge

<div class="grid cards" markdown>

-   :material-lightning-bolt:{ .lg .middle } **Parallel**

    ---

    Every dependency is fetched concurrently, with global and per-host limits.

    [:octicons-arrow-right-24: Limits and retries](architecture.md#limits-and-retries)

-   :material-key-chain:{ .lg .middle } **One auth handshake per registry**

    ---

    The first request to a host runs alone; the token it earns is reused by
    all the others.

    [:octicons-arrow-right-24: How it works](architecture.md#one-auth-handshake-per-registry)

-   :material-database-lock:{ .lg .middle } **Content-addressed cache**

    ---

    Archives are stored by sha256 and shared across charts, aliases and
    processes. A warm `dep build` makes **zero** requests.

    [:octicons-arrow-right-24: The store](architecture.md#the-store)

-   :material-atom:{ .lg .middle } **Atomic `charts/`**

    ---

    Nothing in `charts/` changes unless every dependency succeeded.

    [:octicons-arrow-right-24: Atomic charts/](architecture.md#atomic-charts)

-   :material-code-json:{ .lg .middle } **Machine-readable reports**

    ---

    `-o json` gives CI a stable error `code` per failed dependency instead of
    parsing text.

    [:octicons-arrow-right-24: JSON output](usage.md#json-output)

-   :material-scale-balance:{ .lg .middle } **Helm-identical output**

    ---

    Archive bytes, `Chart.lock` (apart from `generated:`) and `helm template`
    output are checked against real Helm 3 and Helm 4 in CI.

    [:octicons-arrow-right-24: Helm is the oracle](architecture.md#helm-is-the-oracle)

</div>

## Install

Pick how you run Helm. The choice is remembered across the docs.

=== "Helm 4"

    ```sh
    # git sources can't be signature-verified, so --verify=false is required
    helm plugin install https://github.com/mmpyro/forge --version v2.0.0 --verify=false
    helm forge --version
    ```

=== "Helm 3 (3.18+)"

    ```sh
    helm plugin install https://github.com/mmpyro/forge --version v2.0.0
    helm forge --version
    ```

=== "Binary (macOS / Linux)"

    ```sh
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    arch=$(uname -m); case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
    curl -fsSL -o forge "https://github.com/mmpyro/forge/releases/download/v2.0.0/forge-$os-$arch"
    chmod +x forge && sudo mv forge /usr/local/bin/
    forge --version
    ```

=== "Binary (Windows)"

    Download [`forge-windows-amd64.exe`](https://github.com/mmpyro/forge/releases/download/v2.0.0/forge-windows-amd64.exe)
    or [`forge-windows-arm64.exe`](https://github.com/mmpyro/forge/releases/download/v2.0.0/forge-windows-arm64.exe),
    rename it to `forge.exe` and put it in a directory on your `PATH`.

    ```powershell
    forge --version
    ```

=== "From source"

    ```sh
    make build            # → bin/forge (Go 1.25+)
    bin/forge --version
    ```

Mirrors, checksums, upgrades and troubleshooting: [Usage → Install](usage.md#install).

## What's new in 2.0

- **Chart repositories and `file://` dependencies.** Besides OCI registries,
  forge now fetches from classic `https://…/index.yaml` repositories (including
  `@name` references from `helm repo add`) and packages local `file://` charts.
- **`-o json` output.** `dep build` and `dep update` can print one JSON report:
  per-dependency status, digest, size and a classified error code, plus request
  counts per host. See [Usage → JSON output](usage.md#json-output).
- **Distributed as a Helm plugin.** Install with `helm plugin install`, run
  as `helm forge`. The Homebrew tap is discontinued.

## Benchmark

A parent chart with 50 subcharts (2.4 MiB in total): `forge dep build` took
**4.6 s**, `helm dependency update --skip-refresh` took **30.4 s** — about
**6.6× faster**. forge started with 14 of the 50 charts in its cache; helm was
mostly waiting on the network (11 % CPU).

![forge vs helm: 4.6 s vs 30.4 s for 50 subcharts](images/benchmark.png)

For a reproducible comparison (40 charts, +50 ms latency, cold and warm), run
`make bench`; see [Development → Benchmark](development.md#benchmark--scriptsbenchsh).

## Scope

<div class="grid" markdown>

!!! success "Supported"

    - `apiVersion: v2` charts
    - dependencies with `repository:`
        - `oci://…` — OCI registries
        - `https://…` / `http://…` — classic chart repositories, whether or not
          they were added with `helm repo add`
        - `@name` / `alias:name` — repositories from Helm's `repositories.yaml`
          (credentials and TLS settings are used too)
        - `file://…` — local chart directories, packaged like Helm does
    - exact versions and semver ranges (same resolver rules as Helm)
    - aliases, conditions/tags, prerelease and build-metadata versions

!!! failure "Not supported (forge exits before any network call)"

    - dependencies without a `repository` (unpacked in `charts/`) and other
      schemes (`s3://`, plugin getters)
    - `apiVersion: v1` charts

    **Not implemented yet:** `push`/`pull`, `package`, adaptive concurrency, HTTP/3.

</div>

## Guides

| Guide | Read it when you want to… |
|---|---|
| [Usage](usage.md) | install forge, run it, set flags, cache it in CI, read its errors, parse its `-o json` output |
| [Architecture](architecture.md) | understand how a run works and why it is fast and safe |
| [Development](development.md) | build, test, regenerate goldens, run the compat gate and benchmark |
