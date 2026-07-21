# civitai-manager

[![CI](https://github.com/ZacxDev/civitai-manager/actions/workflows/ci.yml/badge.svg)](https://github.com/ZacxDev/civitai-manager/actions/workflows/ci.yml)
[![Release](https://github.com/ZacxDev/civitai-manager/actions/workflows/release.yml/badge.svg)](https://github.com/ZacxDev/civitai-manager/actions/workflows/release.yml)

A single-binary tool that subscribes to CivitAI **models** and **creators**,
polls for new versions, and auto-queues downloads of anything new — with a local
web UI (Tailwind + htmx + [gomponents](https://maragu.dev/gomponents)) for
managing subscriptions, searching CivitAI, and watching the download queue.

> **Status: MVP.** The core logic (version diffing, the download-verify
> pipeline, config, storage) is covered by tests, but the app has **not been
> validated end-to-end against the live CivitAI API**. See
> [Status & verification](#status--verification) for exactly what is and is not
> proven.

## What it does

- **Subscribe** to a model (`civitai.com/models/<id>`) or a creator (`@username`).
- **Poll** each subscription on an interval and **diff** the current version list
  against a per-subscription ledger of already-seen versions.
- On a genuinely new version: record an activity event and — when the
  subscription auto-downloads — **enqueue a download**.
- **Download** files with streaming SHA256 verification, atomic finalize, and
  `.civitai.info` / `.preview.png` sidecars.
- A local **web UI** for subscriptions, search, model/creator pages, the
  activity feed, live queue progress, and a **Library page** whose "Scan now"
  form accepts extra scan paths (one absolute directory per line or
  comma-separated) so cross-directory duplicates outside `model_root` — the same
  reach as the CLI `scan --path` — surface as quarantinable candidates in the UI.
  The web scan is **confined**: it refuses `/`, system directories (`/etc`,
  `/proc`, `/usr`, …), and your `$HOME` root itself; it is bounded by a deadline
  (`--web-scan-timeout`, default 2m) and a model-file cap (default 50k) so an
  over-broad path aborts with a "narrow the path" message; and the extra-path
  input is **only available when the server is bound to loopback** (see the
  security note below). By default a web scan runs **offline** (local
  duplicate/broken analysis only) and does not send file hashes to CivitAI —
  tick "Match against CivitAI" to opt into by-hash matching for that scan.
- **Search** CivitAI from the CLI or UI (first page / `--limit`).
- **Library management** — `scan` an existing model directory (hash, match to
  CivitAI, flag superseded/duplicate/broken deletion candidates, read-only), then
  `library quarantine`/`restore`/`trash` to soft-delete candidates into a trash
  dir with a reversible undo manifest (nothing is ever hard-deleted).

First-poll behaviour is deliberately conservative: subscribing **seeds** the
ledger with the current back-catalog *without downloading it*, so a new
subscription never retro-downloads everything. Pass `--backfill-latest` to also
grab the current newest version on subscribe.

## Install

`civitai-manager` is a single static binary (SQLite is the pure-Go
`modernc.org/sqlite` driver, so builds are `CGO_ENABLED=0` — no C toolchain,
trivially cross-compiled). Pick one:

**1. Prebuilt binary (GitHub Releases).** Download the archive for your
OS/arch from the [Releases page](https://github.com/ZacxDev/civitai-manager/releases/latest),
verify it against `checksums.txt`, extract, and put the binary on your `PATH`:

```sh
# Example: Linux x86_64. Swap the OS/arch for darwin_amd64, darwin_arm64,
# linux_arm64, or the windows_*.zip archive as appropriate.
VERSION=0.1.0
curl -sSLO "https://github.com/ZacxDev/civitai-manager/releases/download/v${VERSION}/civitai-manager_${VERSION}_linux_amd64.tar.gz"
curl -sSLO "https://github.com/ZacxDev/civitai-manager/releases/download/v${VERSION}/checksums.txt"
sha256sum --check --ignore-missing checksums.txt
tar -xzf "civitai-manager_${VERSION}_linux_amd64.tar.gz"
sudo install civitai-manager /usr/local/bin/
civitai-manager --version
```

**2. `go install`** (needs Go 1.25+):

```sh
go install github.com/ZacxDev/civitai-manager@latest
```

**3. Build from source** — see [Build & run](#build--run) below.

> The `go.mod` currently pins `github.com/civitai/cli` at a pseudo-version
> (a commit on its public `main`) because the module hasn't cut a tagged
> release that includes `pkg/civitai`. Source builds resolve it fine. Once
> upstream tags such a release, bump the dependency to that tag.

## Build & run

Requires **Go 1.25+**.

```sh
go build -o civitai-manager .

# Run the web UI + poller + download worker (binds loopback by default):
./civitai-manager serve
# → open http://localhost:8787

# To expose the UI on your LAN, bind a non-loopback interface explicitly:
./civitai-manager serve --addr 0.0.0.0:8787
```

> **Security note:** the UI binds `127.0.0.1:8787` by default, so it is not
> reachable from other machines. The UI has **no login**; its only protection is
> a per-process CSRF token on the state-changing forms. Binding `--addr` to a
> non-loopback interface exposes an **unauthenticated** UI (CSRF-token-only) to
> anyone who can reach that interface — only do so on a trusted network.
>
> Because the Library "Scan now" form can walk and hash arbitrary host
> directories, that **extra-scan-path capability is disabled on a non-loopback
> bind**: a LAN-exposed server may only scan `model_root` and configured
> `library_paths`, the extra-path input is not rendered, and any submitted
> `scan_paths` is rejected. On loopback the web scan is still confined (no `/`,
> no system dirs, not `$HOME` itself) and bounded by `--web-scan-timeout` and a
> model-file cap.

### CLI

```sh
# Subscribe to a model (by id or full URL):
civitai-manager subscribe 4201
civitai-manager subscribe https://civitai.com/models/4201/realistic-vision

# Subscribe to a creator:
civitai-manager subscribe --creator someartist

# Subscription options:
#   --notify-only        record new versions but don't download
#   --no-auto            create the subscription with auto-download off
#   --backfill-latest    download the current latest version now, synchronously,
#                        before the command returns (plain subscribe only seeds
#                        the ledger and downloads nothing)
#   --base-model SDXL    only download versions matching this base model
#   --file-type Model    prefer this file type when a version has several

# Search CivitAI from the CLI (first page / --limit; --json for the raw body):
civitai-manager search "realistic vision"
civitai-manager search --username someartist --type Checkpoint --limit 20
civitai-manager search anime --tag style --nsfw --json

# Global flags (apply to serve/check/subscribe):
#   --max-file-size 2GB     skip auto-downloads whose primary file exceeds this
#                           size (e.g. 500MB, 2GB; 0/empty = unlimited)
#   --download-jitter 15m   anti-stampede: schedule each AUTO-detected download
#                           at a random point in [0, dur) so a fleet of installs
#                           doesn't hit CivitAI's download endpoint in unison
#                           when a popular model publishes (0 = start at once).
#                           Manual/--backfill-latest downloads always start now.

civitai-manager list
civitai-manager unsubscribe <id>

# One-shot poll of every subscription (for cron); add --download to also
# fetch queued files immediately instead of leaving them for `serve`:
civitai-manager check
civitai-manager check --download

# Library: scan model directories (read-only: hash, match, flag deletion
# candidates), then quarantine acts on the flags. --path adds extra directories
# to scan (repeatable) on top of model_root:
civitai-manager scan
civitai-manager scan --path ~/ComfyUI/models --path ~/A1111/models/Lora

# `scan` RECORDS, per file, the root it was found under, so a candidate flagged
# under an extra `scan --path <dir>` stays actionable by a later `quarantine`
# WITHOUT re-specifying <dir>. A file may only be quarantined if it lies inside
# model_root, a root that was actually scanned (recorded at scan time), or an
# explicit --path — never an arbitrary path.
#
# Note on persistence: the extra scan *paths* (CLI --path / the web form's extra
# paths) are NOT saved as config — you re-supply them each run. But each scanned
# file's `scan_root` IS persisted on its index row, so a file that MATCHED a
# CivitAI version while found under an extra path stays quarantine-eligible
# afterwards without re-passing that path. Safety bound: only files that MATCH a
# CivitAI version can ever be quarantined — an unmatched host file scanned via an
# extra path is recorded/inventoried but can NEVER be moved.
civitai-manager library candidates
civitai-manager library quarantine --all              # dry-run over all candidates
civitai-manager library quarantine --reason duplicate --apply
civitai-manager library quarantine --id 12 --apply
# Standalone quarantine of a directory not recorded by a prior scan: union it in
# explicitly with --path (repeatable; unioned with model_root + recorded roots):
civitai-manager library quarantine --path ~/loose-loras --all --apply
civitai-manager library restore <batchID>             # undo a quarantine batch
```

By default the one-shot download commands (`subscribe --backfill-latest`,
`check --download`) print clean friendly progress/summary lines; add `-v` to see
the detailed structured worker/poller logs. Each completed download prints a
per-file verification line so you can see, at a glance, that the bytes were
hash-checked against the API's expected SHA256:

```
✓ easynegative.safetensors (sha256 c74b4e810b03 verified)
⚠ some-model.safetensors (unverified — no hash from API)
```

A `⚠ unverified` line means the API supplied no hash for that file, so it was
downloaded but could not be checksum-verified (it is never reported as
"verified").

`unsubscribe <id>` fully removes the subscription's state — its seen-version
ledger AND its download-queue rows — so re-subscribing to the same target later
is a clean slate that re-enqueues and re-downloads (rather than being deduped
against a stale completed row).

## Configuration & authentication

The CivitAI **API token** and other settings resolve by precedence:

1. command-line flag (`--token`, `--base-url`, `--model-root`, `--db`)
2. environment variable **`CIVITAI_TOKEN`** (token only)
3. config file (below)
4. the official [`civitai` CLI](https://github.com/civitai/cli)'s config, if
   present — `~/.config/civitai/config.yaml`, the `token:` field (token only,
   lowest precedence)
5. built-in defaults

The token is **never logged** — diagnostic output redacts it to `****abcd`.
The public read endpoints work anonymously; a token is required to download most
files.

> **Already using the official `civitai` CLI?** Its login token lives in
> `~/.config/civitai/config.yaml` under `token:`. civitai-manager reads that as a
> last-resort fallback automatically, so you may not need to configure a token at
> all. To be explicit instead, copy that value into `CIVITAI_TOKEN`, pass
> `--token`, or set `token:` in `~/.config/civitai-manager/config.yaml`. A
> missing or unreadable official-CLI config is ignored.

Config file location honours `XDG_CONFIG_HOME`, defaulting to
`~/.config/civitai-manager/config.yaml`:

```yaml
token: "your-civitai-api-key"      # or set CIVITAI_TOKEN
base_url: "https://civitai.com"
model_root: "~/civitai-models"
default_poll_interval: "1h"        # floored at 15m (API edge-caches ~5m)
download_jitter: "15m"             # anti-stampede window; "0" = start at once
max_file_size: ""                  # e.g. "2GB"; empty/"0" = unlimited
addr: "127.0.0.1:8787"             # loopback by default; set a LAN host to expose
web_scan_timeout: "2m"             # deadline for a web "Scan now" walk/hash
web_scan_max_files: 50000          # model-file cap for a web scan; over → aborts
# db_path: "~/.config/civitai-manager/civitai-manager.db"
```

The web-scan bounds only apply to the browser "Scan now" button (the
network-reachable surface). The CLI `scan` is unbounded — you typed the path
knowingly — though it is equally context-cancellable (Ctrl-C aborts the walk
promptly, not just after it finishes).

Downloaded files are laid out as
`<model_root>/<type>/<creator>/<model>/<versionName>.<ext>` with sanitized path
components, plus `.civitai.info` (raw version JSON) and `.preview.png` sidecars.

## SDK dependency

This app imports the official client SDK `github.com/civitai/cli/pkg/civitai`
(promoted to a public, importable package in civitai/cli #172). `go.mod` pins the
merged commit as a pseudo-version; once civitai/cli cuts a tagged release
containing `pkg/civitai`, bump the `require` to that tag.

## Architecture

```
main.go                 cobra entrypoint
internal/
  config/               flag/env/file resolution, token redaction
  store/                SQLite (modernc.org/sqlite, pure Go) + embedded migrations
  civitai/              thin wrapper over the SDK Reader/Downloader (test seams,
                        URL parsing, path layout, file selection)
  poller/               version-diff (pure) + scheduler + subscribe/seed logic
  queue/                streaming download worker (verify → atomic rename → sidecars)
  web/                  gomponents pages + htmx handlers + embedded Tailwind/htmx
  hashutil/             SHA256 file digest + compare
  library/              read-only scan/match/analyze pipeline + quarantine mover
  cli/                  cobra commands: serve, subscribe, search, list,
                        unsubscribe, check, scan, library (candidates/quarantine/
                        restore/trash)
```

SQLite uses the **pure-Go** `modernc.org/sqlite` driver (no cgo), so the binary
cross-compiles cleanly. The schema is applied via embedded, ordered `.sql`
migrations tracked in a `schema_migrations` table.

The web UI is **fully self-contained**: Tailwind's `output.css` and `htmx.min.js`
are embedded via `go:embed` — no external CDN, works offline. Regenerate the CSS
after editing any template's classes:

```sh
cd internal/web
nix-shell -p tailwindcss --run \
  "tailwindcss -c tailwind.config.js -i input.css -o assets/output.css --minify"
```

## Status & verification

`go build ./...`, `go vet ./...`, `go test ./...` all pass and `gofmt -l .` is
empty.

**Tested (automated):**

- **Version diff** — table-driven: given a seen-set and a fetched version list,
  exactly the right new version ids are detected; first-poll seeding does **not**
  enqueue; notify-only does not enqueue; base-model filter is respected;
  `--backfill-latest` enqueues only the latest; creator-search raw parsing.
  Driven by an in-memory fake `civitai.Reader` (no network).
- **Download worker** — SHA256 happy-path (case-insensitive), mismatch **fails
  the row and discards** the file, atomic rename, sidecar writes, `local_files`
  indexing, and HTTP-error retry/fail — via `httptest` + a fake downloader. A
  file the API gives **no hash** for is finalized but recorded as
  **UNVERIFIED** (never reported as "verified"). A download interrupted by a
  **graceful shutdown** is requeued (not failed) and completes on restart.
- **Anti-stampede** — auto-detected downloads get a per-instance random
  `not_before` start offset (within the `download_jitter` window) and are not
  claimed before their time; manual/`--backfill-latest` downloads start
  immediately.
- **Size cap** — a version whose primary file exceeds `--max-file-size` is
  skipped (a `size_skip` event), not enqueued.
- **CSRF** — state-changing POSTs are rejected (403) without the per-process
  token and accepted with it; `PollAll` (`check`) backs off on `ErrRateLimited`.
- **Store** — migration applies to version 1; subscription CRUD + unique-target
  constraints; seen-versions ledger; queue state transitions + dedup guard.
- **Config** — flag > env > file precedence; token redaction; XDG; duration parse.
- **Web** — every page/fragment renders without panic with expected elements;
  dashboard/asset/subscribe handlers return 200 with the right content.
- **Library scan/quarantine** — incremental hash cache (unchanged files reuse the
  stored hash/match); duplicate/superseded/broken flagging (duplicates work
  offline; the keeper is the best-organized copy); the quarantine mover's safety
  invariants (never leaves zero copies of a duplicate set, refuses unmatched /
  newest-version / out-of-root / changed-since-scan files), durable cross-FS move,
  reversible restore, and root-qualified trash paths — via in-memory store + fake
  reader with injectable hash/move seams. A candidate scanned under an extra
  `scan --path` root stays actionable via its persisted `scan_root` (or an explicit
  `quarantine --path`), while a file inside no scanned root is still refused —
  containment is verified against real paths, so a mismatched `scan_root` grants no
  escape.
  `restore` returns a quarantined batch's files to disk AND re-indexes each
  restored model file into `local_files` (path/sha/size, nearest known scan
  root), so it reappears in `library candidates` / the Library page immediately;
  it prints a hint to run `scan` to re-match and re-evaluate candidacy.
- **CLI search** — the `search` command maps flags to query params and renders
  the first page (bounded by `--limit`); `--json` emits the raw body.

**Manually exercised (real HTTP, local fake API):** `serve` starts and renders
the dashboard + embedded assets; `subscribe` seeds without downloading; `list`,
`check`, and `unsubscribe` work; a bad model id surfaces the SDK's classified
`404` error.

**Live validation against api.civitai.com** is now available as an opt-in
integration-test harness — see [Live integration tests](#live-integration-tests).
Run it with your own `CIVITAI_TOKEN` to exercise these real code paths:

- Real API response shapes (field presence/casing) beyond the SDK's typed structs.
- An actual authenticated **file download** end-to-end — CivitAI's signed-redirect
  + auth flow, with the downloaded bytes' SHA256 checked against the API hash.
- by-hash version resolution (`GetModelVersionByHash`) round-trip.
- `ErrNotFound` classification on a bad id.
- The real poller seed/diff cycle against live model data.
- `.civitai.info` / `.preview.png` sidecars written from live data.

**Still NOT covered (even by the live harness):**

- Rate-limit backoff behaviour against the live throttle.
- Creator polling against a real creator's `/api/v1/models?username=` payload.

### Live integration tests

These tests hit the **real** `api.civitai.com`. They are gated so ordinary
`go test ./...` and CI stay green offline: they compile only under the
`integration` build tag AND skip unless `CIVITAI_INTEGRATION=1` is set (auth
tests also need `CIVITAI_TOKEN`).

```sh
# Read/metadata + by-hash + error-classification + poller seed (no file bytes):
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

### Deferred / stubbed (post-MVP)

- **Byte-range resume** — the SDK `Downloader` takes only a URL (no `Range`
  header), so an interrupted download is **re-fetched whole** rather than
  resumed. Interrupted rows are requeued on restart.
- **Search pagination beyond the first page** — the CLI/UI `search` returns the
  first page (bounded by `--limit`); walking the cursor/`Metadata` to fetch
  subsequent pages is not wired yet. First-page search itself is implemented and
  tested.

## Notes on the SDK surface

Coding against `pkg/civitai` matched the documented surface, with these details
confirmed from source:

- `ModelSearchResult.Items` are `ModelListItem` (id/name/type/nsfw/creator/stats)
  and **do not carry versions or a thumbnail**. Creator polling therefore diffs
  on the search response's **raw JSON** (`items[].modelVersions[].id`), and
  search result cards show no thumbnail (the typed item has no image field).
- `ModelVersionSummary` carries `ID`/`Name`/`BaseModel`/`Files` but **no
  `publishedAt`**, so `seen_versions.published_at` is left null.
- `ModelVersionDetail` has no `Images` field; the first preview image is parsed
  best-effort from the raw version JSON (`images[].url`).
