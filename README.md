# forge

<p align="center">
  <img src="images/logo.png" alt="forge logo" width="200">
</p>

`forge` replaces `helm dependency build` / `helm dependency update` for charts
whose dependencies live in OCI registries, classic chart repositories
(`https://…/index.yaml`) or local `file://` directories. It writes the same `charts/*.tgz`
and `Chart.lock` that Helm would, so `helm template`, `helm install` and
`helm package` keep working unchanged — they just get their dependencies
faster.

```sh
forge dep update ./my-chart   # resolve ranges, download, write Chart.lock
forge dep build  ./my-chart   # download exactly what Chart.lock pins
helm template my-release ./my-chart
```

## Install as a Helm plugin

```sh
# Helm 4 (git sources can't be signature-verified, so --verify=false is required)
helm plugin install https://github.com/mmpyro/forge --version v1.1.0 --verify=false
# Helm 3 (3.18+)
helm plugin install https://github.com/mmpyro/forge --version v1.1.0

helm forge dep update ./my-chart
helm forge dep build  ./my-chart
```

The install hook downloads the release binary for your OS/arch (macOS, Linux,
Windows; amd64/arm64) and checks it against the release's `checksums.txt`.
Upgrade with `helm plugin update forge`. Details in
[Usage → Install](docs/usage.md#install).

## Downloads (v1.0.0)

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

Install on macOS / Linux:

```sh
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
curl -fsSL -o forge "https://github.com/mmpyro/forge/releases/download/v1.0.0/forge-$os-$arch"
chmod +x forge && sudo mv forge /usr/local/bin/
forge --version
```

To build from source, see [Usage → Install](docs/usage.md#install).

## Guides

| Document | Read it when you want to… |
|---|---|
| [Usage](docs/usage.md) | install forge, run it, set flags, cache it in CI, read its errors |
| [Architecture](docs/architecture.md) | understand how a run works and why it is fast and safe |
| [Development](docs/development.md) | build, test, regenerate goldens, run the compat gate and benchmark |

## Why forge

- **Parallel.** Every dependency is fetched concurrently, with global and
  per-host limits.
- **One auth handshake per registry.** The first request to a host runs alone;
  the token it earns is reused by all the others.
- **Content-addressed cache.** Archives are stored by sha256 and shared across
  charts, aliases and processes. A warm `dep build` makes zero requests.
- **Atomic `charts/`.** Nothing in `charts/` changes unless every dependency
  succeeded.
- **Helm-identical output.** Archive bytes, `Chart.lock` (apart from
  `generated:`) and `helm template` output are checked against real Helm 3 and
  Helm 4 in CI.

## Scope

Supported:

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

Not supported (forge exits before any network call):

- dependencies without a `repository` (unpacked in `charts/`) and other
  schemes (`s3://`, plugin getters)
- `apiVersion: v1` charts

Not implemented yet: `push`/`pull`, `package`, adaptive concurrency, HTTP/3.

## License

MIT — see [LICENSE](LICENSE).
