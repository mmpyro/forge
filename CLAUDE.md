# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`forge` is a Go CLI that replaces `helm dependency build/update` for charts whose dependencies live in **OCI registries, classic http(s) chart repositories (index.yaml) or local `file://` directories**. It is used *alongside* helm: it writes a standard `charts/` dir and `Chart.lock` that `helm template/install` consume unchanged. Goal: fetch dozens of dependencies fast (parallel fetch, content-addressed cache, shared token cache, HTTP/2).

User and architecture docs: `README.md` and `docs/`. Later sub-projects (push/pull, package, adaptive concurrency/HTTP/3) are not implemented yet.

## Commands

```sh
make build                 # bin/forge, version via -ldflags "-X main.Version=$(VERSION)" (default 0.1.0)
make test                  # go test ./...  (unit, no network, no docker)
go test -race ./...        # what CI runs for unit tests
go test ./internal/resolve -run TestName -v   # single test

make test-integration      # starts registry:2 on localhost:5001 (docker), pushes fixtures, runs -tags integration
make compat                # Helm 3 + Helm 4 compatibility gate (scripts/compat.sh)
make golden                # regenerate testdata/golden/* with real `helm dependency update`
make bench                 # hyperfine: helm vs forge cold/warm, 40 deps behind toxiproxy +50ms
make registry-down         # stop the local registry container
```

- Integration, compat, golden and bench need `docker` and `helm` (v4) on PATH; bench also needs `hyperfine` and `jq`.
- `make helm3` downloads Helm 3 into `.bin/helm3` (used by compat alongside `helm`).
- Integration tests live in `test/integration/` behind the `integration` build tag; they talk to `localhost:5001` (OCI registry) and `localhost:5002` (nginx chart repository, `.chartrepo/`) through counting proxies and assert request counts.
- Local registry is plain HTTP → pass `--plain-http` when running forge against it (chart repositories use the URL's scheme).
- `HELM_FORGE_CACHE` overrides the cache root (default `~/.cache/helm-forge`).

## Architecture

Flow: `cmd/forge` → `internal/cli` (cobra, flags, exit codes) → `internal/engine` (Build/Update orchestration) → the packages below.

| Package | Role |
|---|---|
| `chartmeta` | Load `Chart.yaml`/`Chart.lock` via Helm v4 SDK (`pkg/chart/v2`), write `Chart.lock` |
| `lockdigest` | Re-implementation of Helm's lock digest (Helm's is in an unimportable `internal/` pkg) |
| `resolve` | Semver range → exact version via OCI `tags/list`, chart-repo `index.yaml`, or the local `file://` chart (Masterminds/semver, same as Helm) |
| `registry` | oras-go based OCI client: auth, one shared token cache; `registry.HTTP` = the run's shared HTTP/2 clients, global/per-host limits, retries |
| `chartrepo` | Classic chart repos: index.yaml parsed like Helm's `pkg/repo/v1` (copied, not imported — it drags in Helm's getter/plugin stack), chart URLs, basic auth/TLS |
| `repoconfig` | Helm's `repositories.yaml`: `@name`/`alias:name` → URL, credentials, TLS |
| `par` | `ByHost`: first request per host runs alone so the 401 challenge is answered once, then the rest fan out |
| `fetch` | Parallel scheduler, singleflight by digest, streams blob → sha256 → store |
| `store` | CAS cache: `blobs/sha256/<hex>` (0444), `refs/<registry>/<repo>/<version>`, `tmp/` |
| `materialize` | Place blobs into `charts/` via reflink → hardlink → copy (per-OS `reflink_*.go`), staging + swap |
| `testutil/fakeregistry` | In-memory OCI registry for unit tests; records requests, injects faults (429, 5xx, digest mismatch…) |
| `testutil/fakerepo` | In-memory chart repository for unit tests; request log (marks requests with credentials), basic auth, queued error statuses |

Invariants that span files:

- **Helm is the oracle.** `charts/*.tgz` bytes, `Chart.lock` (except `generated:`) and `helm template` output must match real helm 3 and 4. `testdata/golden/` is produced by real helm; a mismatch is a forge bug, don't edit goldens by hand — rerun `make golden`.
- **`charts/` is atomic.** Archives go to `<chart>/.forge-staging/` (at chart root, not in `charts/` — helm would load it as a subchart) and are swapped in only if *every* dependency succeeded. Stale-archive removal mirrors `helm dep build`.
- **Store is lock-free multi-process safe:** write to `tmp/`, then atomic rename. Cached tag→digest refs are trusted unless `--refresh`.
- **Request budget:** cold ≈ `2 × OCI charts + 1 per registry` + `1 × chart-repo charts + 1 index.yaml per repo`; `file://` = 0; warm = 0 requests. Integration tests assert this — changes to fetch/auth ordering can break them.
- `dep build` with no `Chart.lock` behaves like `dep update` (Helm behaviour); a digest mismatch fails with "run 'forge dep update'".
- `engine.prepare` validates every repository before any network call and rewrites `@name` to its URL (Helm hashes and locks the URL). No-repository deps, other schemes and `apiVersion: v1` charts are unsupported.
- Deliberate difference from Helm: `dep build` accepts chart-repo URLs never `helm repo add`-ed (fetched anonymously).
- Chart-repo archives keep their URL's file name in `charts/` (stored in `store.Ref.File`). `file://` charts are packaged with `chartutil.Save` every run (tar identical to Helm 4; gzip bytes depend on the Go version and Helm 3 stamps time.Now(), so compat compares them by member names + contents).
- Exit codes: `0` ok, `1` any dependency failed (`engine.DependencyError` lists each), `2` usage error (`cli.usageError`).
- Credentials come only from Helm's registry config (`$HELM_REGISTRY_CONFIG`) and `repositories.yaml` (`$HELM_REPOSITORY_CONFIG`, `--repository-config`); missing file = anonymous. Repo credentials go only to the repo's scheme+host unless `pass_credentials_all`.

## Test fixtures

- `testdata/src/` — dependency charts pushed to the local registry by `scripts/fixtures.sh` (plus 40 generated `bench-NN` charts).
- `testdata/charts/<fixture>/` — parent charts exercising one feature each (alias, ranges, prerelease, build-metadata, condition-tags, stale, …).
- `testdata/seeds/<fixture>/` — pre-existing `charts/` contents copied in before a run (e.g. `stale`).
- `testdata/golden/<fixture>/` — helm-generated `Chart.yaml` + `Chart.lock`.
