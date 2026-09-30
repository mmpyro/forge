# Architecture

## A run, end to end

```
cmd/forge ─► internal/cli ─► internal/engine
                               │
             ┌─────────────────┼──────────────────────────────┐
             ▼                 ▼                              ▼
         chartmeta          resolve ──► registry          fetch ──► registry
     (Chart.yaml/lock)    (ranges → tags)    ▲          (manifest + blob)
                                             │                 │
                                            par ◄──────────────┤
                                     (auth-aware fan-out)      ▼
                                                             store
                                                        (CAS cache)
                                                               │
                                                               ▼
                                                         materialize
                                                     (staging → charts/)
                                                               │
                                                               ▼
                                                   chartmeta.WriteLock
                                                   (update only, lockdigest)
```

`dep update`:

1. `chartmeta.Load` reads the chart with Helm's own loader (so dependency
   fields are sanitised exactly as Helm does before hashing). `apiVersion: v1`
   is rejected.
2. Non-`oci://` dependencies are rejected — before any network call.
3. `resolve.Resolve` turns ranges into versions. Exact versions skip the
   registry. Each range repository's `tags/list` is fetched once.
4. `install` fetches every locked dependency (see below). Any failure → stop,
   `charts/` untouched.
5. `lockdigest.Compute` hashes `[Chart.yaml deps, locked deps]`. If it equals
   the existing lock digest, `Chart.lock` is left alone; otherwise it is
   written byte-for-byte as Helm would.

`dep build`: load → reject unsupported → verify the lock digest → `install`.
No lock at all → run `update`.

## Packages

| Package | Role |
|---|---|
| `cli` | cobra commands, flags, exit codes (`usageError` → 2) |
| `engine` | `Build` / `Update` orchestration, `DependencyError` |
| `chartmeta` | Load `Chart.yaml`/`Chart.lock` via Helm v4 SDK, write `Chart.lock` |
| `lockdigest` | Re-implementation of Helm's lock digest (Helm's is in an unimportable `internal/` package) |
| `resolve` | Semver constraint → exact version via OCI `tags/list` |
| `registry` | oras-go client: credentials, shared token cache, HTTP/2, limits, retries, error messages |
| `par` | `ByHost`: first request per host alone, then the rest in parallel |
| `fetch` | Parallel scheduler, dedupe by repo:version and by digest, stream blob → store |
| `store` | Content-addressed cache |
| `materialize` | Place blobs into `charts/` via reflink → hardlink → copy, staging + swap |
| `testutil/fakeregistry` | In-memory OCI registry for unit tests, with request log and fault injection |

## Why it is fast

### One registry client per run

`registry.New` builds one `http.Client` and one oras `auth.Client` for the
whole process. All requests share connections (HTTP/2 when offered), the
token cache and the concurrency limits.

### One auth handshake per registry

Firing 40 requests at a cold registry would trigger 40 `401` challenges and
40 token requests. Two things prevent that:

- `registry.WithPullScopes` puts *every* repository's pull scope into the
  first token request for each host, so one token covers them all.
- `par.ByHost` runs the first item of each host alone. Its challenge is
  answered and the token cached; then the rest of that host's items fan out.
  Leaders of different hosts run concurrently.

### Request budget

| Run | Requests |
|---|---|
| Cold `dep build` | ≈ `2 × unique charts + 1 per registry` (manifest + blob per chart, one auth round) |
| Warm `dep build` | `0` |
| `dep update` | adds one `tags/list` per repository that has a range constraint |

The integration tests assert these numbers through a counting proxy. Changing
fetch or auth ordering can break them.

### Deduplication

- Items with the same `repo:version` (e.g. three aliases of one chart) become
  one job.
- Jobs sharing a layer digest download it once (`singleflight` by digest).
- A blob already in the store is never downloaded again, whichever chart or
  project asked for it first.

### Limits and retries

`limitTransport` holds a global slot and a per-host slot per request **until
the response body is closed** — the transfer is still running after
`RoundTrip` returns. It takes the host slot first so a request queued on a
busy host does not block a global slot another host could use. Per-host
counting also means blob redirects (ECR → S3, GHCR → pkg-containers) get
their own budget.

Retries wrap the limiter, so a retry waits for a free slot like any other
request. Policy: `408`, `429`, `5xx`, network errors; 3 retries;
200 ms × 2ⁿ with 20 % jitter, or `Retry-After` up to 30 s.

## Why it is safe

### The store

```
$HELM_FORGE_CACHE/
  blobs/sha256/<hex>                 chart archives, mode 0444
  refs/<registry>/<repo>/<version>   JSON: manifest digest, layer digest, size, fetched_at
  tmp/                               in-flight writes
```

- Every write goes to `tmp/` then is `rename`d into place, so concurrent forge
  processes share one cache without locks.
- Blobs are hashed while streaming; a mismatch is discarded and downloaded
  once more before failing.
- `Open` sweeps `tmp/` files older than an hour (left by crashed runs).
- A cached `refs/` entry is trusted unless `--refresh`. That is what makes a
  warm build zero requests.

### Atomic `charts/`

1. Archives are placed into `<chart>/.forge-staging/`. It sits at the chart
   root, not in `charts/`, because Helm would load a leftover directory in
   `charts/` as a broken subchart.
2. Only if **every** dependency succeeded, staged files are renamed into
   `charts/` and stale chart archives are removed (non-chart files and
   directories are kept, as Helm does).
3. On any error the staging directory is removed and `charts/` is untouched.
   A staging directory left by a killed run is wiped at the start of the next.

### Placing files

`place` tries, in order:

1. **reflink** — copy-on-write clone (`clonefile` on macOS/APFS,
   `FICLONE` on Linux btrfs/XFS). Independent file, no extra disk.
2. **hardlink** — shares the read-only cache inode. The archive in `charts/`
   is `0444`.
3. **copy** — plain `0644` copy, e.g. across filesystems.

## Helm is the oracle

forge's output must match real Helm, not a spec of Helm:

- `testdata/golden/` holds `Chart.lock` files produced by real
  `helm dependency update`. A mismatch is a forge bug — regenerate with
  `make golden`, never edit by hand.
- `make compat` runs every fixture through Helm 3, Helm 4 and forge and diffs
  `charts/` listings, archive sha256, `Chart.lock` (minus `generated:`) and
  `helm template` output.
- Resolver details copied from Helm: only strict semver tags count, `_` in a
  tag is read as `+`, exact versions are not looked up, highest match wins.
