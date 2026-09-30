# forge

<p align="center">
  <img src="images/logo.png" alt="forge logo" width="200">
</p>

`forge` replaces `helm dependency build` / `helm dependency update` for charts
whose dependencies live in OCI registries. It writes the same `charts/*.tgz`
and `Chart.lock` that Helm would, so `helm template`, `helm install` and
`helm package` keep working unchanged — they just get their dependencies
faster.

```sh
forge dep update ./my-chart   # resolve ranges, download, write Chart.lock
forge dep build  ./my-chart   # download exactly what Chart.lock pins
helm template my-release ./my-chart
```

## Downloads (latest release)

Prebuilt binaries and `checksums.txt` are attached to every
[GitHub release](https://github.com/mmarszalek/helm-forge/releases). The links
below always point at the latest one.

| Platform | Asset | Download |
|---|---|---|
| macOS (Apple Silicon) | `forge-darwin-arm64` | [Download](https://github.com/mmarszalek/helm-forge/releases/latest/download/forge-darwin-arm64) |
| macOS (Intel) | `forge-darwin-amd64` | [Download](https://github.com/mmarszalek/helm-forge/releases/latest/download/forge-darwin-amd64) |
| Linux (x86_64) | `forge-linux-amd64` | [Download](https://github.com/mmarszalek/helm-forge/releases/latest/download/forge-linux-amd64) |
| Linux (ARM64) | `forge-linux-arm64` | [Download](https://github.com/mmarszalek/helm-forge/releases/latest/download/forge-linux-arm64) |
| Windows (x86_64) | `forge-windows-amd64.exe` | [Download](https://github.com/mmarszalek/helm-forge/releases/latest/download/forge-windows-amd64.exe) |
| Windows (ARM64) | `forge-windows-arm64.exe` | [Download](https://github.com/mmarszalek/helm-forge/releases/latest/download/forge-windows-arm64.exe) |

Install on macOS / Linux:

```sh
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
curl -fsSL -o forge "https://github.com/mmarszalek/helm-forge/releases/latest/download/forge-$os-$arch"
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
- dependencies with `repository: oci://…`
- exact versions and semver ranges (same resolver rules as Helm)
- aliases, conditions/tags, prerelease and build-metadata versions

Not supported (forge exits before any network call):

- `https://` chart repositories, `file://` and `@alias` repositories
- `apiVersion: v1` charts

Not implemented yet: `push`/`pull`, `package`, adaptive concurrency, HTTP/3.

## License

MIT — see [LICENSE](LICENSE).
