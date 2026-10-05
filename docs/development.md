# Development

## Requirements

| For | Needs |
|---|---|
| build, unit tests | Go 1.25+ |
| integration, compat, golden | + `docker`, `helm` v4 on `PATH` |
| compat | + Helm 3 (`make helm3` downloads it to `.bin/helm3`) |
| bench | + `hyperfine`, `jq` |

## Commands

```sh
make build              # bin/forge, version via -ldflags "-X main.Version=$(VERSION)" (default 0.1.0)
make test               # go test ./... — no network, no docker
go test -race ./...     # what CI runs
go test ./internal/resolve -run TestName -v

make test-integration   # registry:2 on localhost:5001, push fixtures, -tags integration
make compat             # Helm 3 + Helm 4 compatibility gate
make golden             # regenerate testdata/golden/* with real helm
make bench              # hyperfine: helm vs forge cold/warm
make plugin-smoke       # helm plugin install of this checkout, for helm + .bin/helm3
make registry-down      # stop the local registry
```

`make plugin-smoke` (`scripts/plugin-smoke.sh`) builds the host binary into a
fake release served on `127.0.0.1:18765` and points the install hook at it via
`HELM_FORGE_PLUGIN_URL`. Then, for each helm in `$HELMS`, it checks:

- a corrupted `checksums.txt` makes the install fail
- `helm forge --version` and `helm forge cache path` work
- if the registry is up (`make fixtures`), `helm forge dep update` gives the
  same `charts/` and `Chart.lock` as standalone forge

Helm 4's installer ignores `HELM_PLUGINS`, so the script isolates plugins with
`HELM_DATA_HOME` instead.

The local registry is plain HTTP — pass `--plain-http` when running forge
against it by hand:

```sh
make fixtures
bin/forge dep update --plain-http testdata/charts/ranges
```

Use a throwaway cache so you don't pollute `~/.cache/helm-forge`:

```sh
HELM_FORGE_CACHE=$(mktemp -d) bin/forge dep build --plain-http ./chart
```

## Test layers

### Unit tests

No network. Registry behaviour comes from `internal/testutil/fakeregistry`,
an in-memory OCI registry that records every request and can inject faults
(429, 5xx, digest mismatch, …). Chart repositories come from
`internal/testutil/fakerepo`: an `index.yaml` plus archives, with a request
log (marking requests that carried credentials), basic auth and queued error
statuses. `internal/testutil.WriteChartTgz` builds a
real chart archive for store/materialize tests.

### Integration tests — `test/integration/`

Build tag `integration`. They run forge against the local registry
(`localhost:5001`) and chart repository (`localhost:5002`) through counting
reverse proxies, with no `repositories.yaml`, and assert:

- cold `dep build` makes at most `2 × unique OCI charts + 1 × unique
  chart-repository charts + 1 per source` requests;
- warm `dep build` makes **zero** requests;
- `charts/` has one entry per unique chart.

If you change fetch or auth ordering and these fail, the request budget
regressed — see [Architecture → Request budget](architecture.md#request-budget).

### Compatibility gate — `scripts/compat.sh`

For every fixture × {`update`, `build`} × {Helm 4, Helm 3}, runs Helm and forge
on separate copies and diffs:

- `charts/` listing
- sha256 of every downloaded archive; for archives packaged from `file://`
  directories, a hash of member names and contents (gzip bytes depend on the
  Go version each binary was built with, and Helm 3 stamps tar entries with
  the current time)
- `Chart.lock` without `generated:`
- `helm template` output

Fixtures using the chart repository get `helm repo add fixtures …` in Helm's
isolated config first, because `helm dependency build` refuses unregistered
repositories; forge runs with an empty config.

Override binaries with `FORGE=…` and `HELMS="helm /path/to/helm3"`.

### Benchmark — `scripts/bench.sh`

40 dependencies (`bench-00` … `bench-39`) behind toxiproxy adding 50 ms to
every registry response. Compares `helm dependency build`, cold forge and warm
forge with hyperfine; results go to `bench/results.md`.

Targets (the script fails if missed):

- cold forge ≥ 5× faster than helm
- warm forge < 1 s

`RUNS=10 make bench` changes the number of runs.

## Fixtures

| Path | Contents |
|---|---|
| `testdata/src/` | Dependency charts pushed by `scripts/fixtures.sh`: `dep-a` (1.0.0, 1.1.0, 1.2.0, 2.0.0), `dep-b` (0.1.0, 0.2.0-rc.1), `dep-c` (1.0.0+build.1), plus 40 generated `bench-NN`. `dep-a` and `dep-b` are also served from a chart repository (`.chartrepo/`, nginx on `localhost:5002`) |
| `testdata/charts/<fixture>/` | Parent charts, one feature each: `alias`, `build-metadata`, `condition-tags`, `exact`, `file-local` (chart in `local-dep/`), `http-mixed`, `http-ranges`, `nodeps`, `prerelease`, `ranges`, `stale` |
| `testdata/seeds/<fixture>/` | Pre-existing `charts/` contents copied in before a run (e.g. `stale`) |
| `testdata/golden/<fixture>/` | Helm-generated `Chart.yaml` + `Chart.lock` |
| `internal/cli/testdata/json/` | `-o json` output goldens (durations, paths and placement normalised). Regenerate with `go test ./internal/cli -update` and review the diff |

### Adding a fixture

1. Create `testdata/charts/<name>/Chart.yaml` pointing at
   `oci://localhost:5001/charts` or `http://localhost:5002`. A `file://`
   dependency must live inside the fixture directory (not in `charts/`),
   because the scripts copy one fixture directory at a time. Helm cannot take
   one chart name from both an OCI registry and a chart repository.
2. If it needs new dependency versions, add them to `scripts/fixtures.sh`.
3. If it needs pre-existing `charts/` content, put it in `testdata/seeds/<name>/`.
4. Run `make golden` — real Helm writes `testdata/golden/<name>/`.
5. Run `make compat` — forge must match.
6. Add it to the fixture list in `test/integration/e2e_test.go` if it should
   be covered by request-count checks.

Goldens are Helm's output. Never edit them by hand; if forge disagrees,
forge is wrong.

## CI

`.github/workflows/ci.yml` runs on pushes and PRs to `main`, manually
(`workflow_dispatch`), and as the first stage of a release (`workflow_call`):

| Job | Runs on | Does |
|---|---|---|
| `unit` | ubuntu, macos | `go test -race ./...` via gotestsum → JUnit test report; on ubuntu also cross-compiles for linux/darwin/windows × amd64/arm64 |
| `integration` | ubuntu | `make fixtures`, integration tests → JUnit test report, `make compat` → summary table, `make plugin-smoke` |
| `plugin` | ubuntu, macos, windows × Helm 3.22 / 4.1 | `scripts/plugin-smoke.sh`: install this checkout as a Helm plugin, checksum rejection, `helm forge --version` |
| `summary` | ubuntu | Job results in the run summary; fails if any job failed |

## Releasing

`.github/workflows/release.yml` runs when a `v*.*.*` tag is pushed:

1. `plugin-version` — `plugin.yaml`'s `version` must equal the tag (without
   `v`); `ci` — the full CI workflow above. Nothing is built if either fails.
2. `build` — matrix on ubuntu, macos and windows runners: `CGO_ENABLED=0`
   binaries for linux/darwin/windows × amd64/arm64, version set to the tag.
   The runner-native binary is smoke-tested (`forge --version` must print the
   tag).
3. `release` — `checksums.txt`, release notes from commits since the previous
   tag, GitHub release with all binaries attached. Tags containing `-`
   (e.g. `v1.0.0-rc.1`) become prereleases. The run summary lists files,
   sizes, sha256 and changes.
4. `plugin-smoke` — on ubuntu, macos and windows × Helm 3 / Helm 4:
   `helm plugin install https://github.com/mmpyro/forge --version <tag>`
   (`--verify=false` on Helm 4), then `helm forge --version` must print the tag.

To cut a release:

1. Set `version:` in `plugin.yaml` to the new version and merge that to `main`.
2. Tag and push:

```sh
git tag v1.1.0
git push origin v1.1.0
```

Asset names (`forge-<os>-<arch>`, `.exe` on Windows) are what the download
links in `README.md` and the plugin install hooks
(`scripts/install-plugin.sh`, `scripts/install-plugin.ps1`) use. Keep them in sync.

## Invariants to keep in mind

- `charts/` changes only if every dependency succeeded.
- Staging lives at `<chart>/.forge-staging/`, never inside `charts/`.
- Store writes: `tmp/` then rename. No locks.
- Unsupported repositories (none, other schemes, unknown `@name`, missing
  `file://` directory) fail before any network call.
- Exit codes: `0` ok, `1` failure (`engine.DependencyError` lists each), `2`
  usage (`cli.usageError`).
- Credentials only from Helm's registry config and `repositories.yaml`.
