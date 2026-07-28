# Testing & verification status

This page is the honest account of what is and is not proven about
civitai-manager. It exists so you can calibrate your trust rather than take a
feature list at face value.

For contributor workflow (how to build, the release process), see
[`CONTRIBUTING.md`](../CONTRIBUTING.md).

## Status

The project is **v0.1.x**. It is used daily by its author against a real CivitAI
account and a real local ComfyUI, and the release line moves quickly. The
database schema and the internal API surface still change between releases;
migrations are applied automatically and in order, but treat the schema as
unstable.

`go build ./...`, `go vet ./...`, and `go test ./...` pass, and `gofmt -l .` is
empty. CI (`.github/workflows/ci.yml`) runs all of the above on every push to
`main` and every pull request, plus `go test -race` on the concurrency-sensitive
packages and a compile-only check of the build-tagged integration suite.

## What the automated tests cover

- **Version diff** — table-driven: given a seen-set and a fetched version list,
  exactly the right new version ids are detected; first-poll seeding does **not**
  enqueue; notify-only does not enqueue; the base-model filter is respected;
  `--backfill-latest` enqueues only the latest; creator-search raw parsing.
  Driven by an in-memory fake reader (no network).
- **Download worker** — SHA256 happy-path (case-insensitive), mismatch **fails
  the row and discards** the file, atomic rename, sidecar writes, local-file
  indexing, and HTTP-error retry/fail — via `httptest` and a fake downloader. A
  file the API gives **no hash** for is finalized but recorded as **UNVERIFIED**
  (never reported as "verified"). A download interrupted by a **graceful
  shutdown** is requeued, not failed, and completes on restart.
- **Anti-stampede** — auto-detected downloads get a per-instance random
  `not_before` offset within the `download_jitter` window and are not claimed
  before their time; manual/`--backfill-latest` downloads start immediately.
- **Size cap** — a version whose primary file exceeds `--max-file-size` is
  skipped (a `size_skip` event), not enqueued.
- **CSRF** — state-changing POSTs are rejected (403) without the per-process
  token and accepted with it; `PollAll` backs off on rate-limit errors.
- **Loopback gating** — path-taking endpoints reject non-local callers.
- **Store** — migrations apply in order; subscription CRUD and unique-target
  constraints; the seen-versions ledger; queue state transitions and the dedup
  guard.
- **Config** — flag > env > file precedence; token redaction; XDG resolution;
  duration and size parsing, including the `outputs_max_bytes` unit-mistake
  rejection.
- **Web** — every page and fragment renders without panic and with the expected
  elements; handlers return the right status and content.
- **Library scan/quarantine** — the incremental hash cache (unchanged files reuse
  the stored hash/match); duplicate/superseded/broken flagging (duplicates work
  offline; the keeper is the best-organized copy); and the quarantine mover's
  safety invariants — never leaves zero copies of a duplicate set, refuses
  unmatched / newest-version / out-of-root / changed-since-scan files, durable
  cross-filesystem move, reversible restore, root-qualified trash paths.
  Containment is verified against **real** (symlink-resolved) paths, so a
  mismatched recorded root grants no escape.
- **ComfyUI graph conversion** — subgraph expansion (including depth and
  node-budget limits), Get/Set teleport resolution, bypassed/muted node splicing,
  converted-widget slot handling, and the object-form `widgets_values` and
  array-typed input-slot cases.
- **Run parameters** — detection of the editable input set, the upstream-widget
  walk, override application against a copy, and the refusal to accept keys that
  are not in the detected set.
- **Output gallery** — capture, the params snapshot, re-run reconstruction, the
  graph-hash mismatch refusal, and oldest-first cap eviction.

## The limits of those tests

**Fake-reader unit tests can encode the wrong assumption about the real CivitAI
API — they pass green while the real integration is broken.** This has happened
repeatedly in this project: the models list API filters on `types` (plural) and
silently ignores the singular form; the apps API returns a ULID string id where
an int was assumed; `modelVersions[]` is ordered by the creator's index, not by
publish date. Every one of those was green in synthetic tests and broken against
reality.

The practical consequence: for anything touching the CivitAI API, the synthetic
tests are a regression net, **not** evidence that the integration works. Live
verification is what settles it.

## Live integration tests

These tests hit the **real** `api.civitai.com`. They are gated so ordinary
`go test ./...` and CI stay green offline: they compile only under the
`integration` build tag **and** skip unless `CIVITAI_INTEGRATION=1` is set (auth
tests also need `CIVITAI_TOKEN`).

```sh
# Read/metadata + by-hash + error classification + poller seed (no file bytes):
CIVITAI_INTEGRATION=1 CIVITAI_TOKEN=xxx \
  go test -tags integration ./internal/integration/ -run Integration -v

# ...plus the real authenticated file-download test (transfers real bytes):
CIVITAI_INTEGRATION=1 CIVITAI_INTEGRATION_DOWNLOAD=1 CIVITAI_TOKEN=xxx \
  go test -tags integration ./internal/integration/ -run Integration -v
```

Or via `make`:

```sh
make integration-test CIVITAI_TOKEN=xxx
make integration-test-download CIVITAI_TOKEN=xxx
```

They exercise real API response shapes beyond the SDK's typed structs, an actual
authenticated file download end-to-end (CivitAI's signed-redirect + auth flow,
with the downloaded bytes' SHA256 checked against the API hash), by-hash version
resolution, not-found classification, the poller seed/diff cycle against live
model data, and sidecar writing from live data.

The live targets default to long-lived public resources and are overridable:

| Env var | Default | Meaning |
| --- | --- | --- |
| `CIVITAI_TEST_MODEL_ID` | `4384` (DreamShaper) | Model used for metadata + poller tests |
| `CIVITAI_TEST_DOWNLOAD_VERSION_ID` | `9208` (EasyNegative embedding, ~25 KB) | **Small** file version for the real-download test |
| `CIVITAI_BASE_URL` | `https://civitai.com` | API base URL |

The download default is intentionally a **tiny** textual-inversion embedding, not
a multi-GB checkpoint, so the test transfers only tens of KB. If a default id has
since been removed upstream, override it. The download test refuses any primary
file larger than ~500 MB as a safety guard.

In CI the live suite runs via `.github/workflows/integration.yml` **manually**
(`workflow_dispatch`) or on a daily schedule. It never runs on ordinary
pushes/PRs, and self-skips with a notice rather than a failure when the token is
absent, so forks and secret-less runs are safe.

## Known gaps

- **Rate-limit backoff** is not exercised against the live throttle.
- **Creator polling** is not covered against a real creator's
  `/api/v1/models?username=` payload.
- **Browser-level interaction** (htmx DOM swaps, the ComfyUI helper's frontend
  script) is verified at the HTTP level — the request a control issues and the
  fragment the server returns — not in a real browser. That verifies the
  server-side effect of a click, not the DOM dispatch.
- **Byte-range resume** is not implemented: the SDK downloader takes only a URL,
  so an interrupted download is re-fetched whole rather than resumed. Interrupted
  rows are requeued on restart.
- **Search pagination** beyond the first page is not wired up.
