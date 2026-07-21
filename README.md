# civitai-manager

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
  activity feed, and live queue progress.

First-poll behaviour is deliberately conservative: subscribing **seeds** the
ledger with the current back-catalog *without downloading it*, so a new
subscription never retro-downloads everything. Pass `--backfill-latest` to also
grab the current newest version on subscribe.

## Build & run

Requires **Go 1.25+**.

```sh
go build -o civitai-manager .

# Run the web UI + poller + download worker:
./civitai-manager serve --addr :8787
# → open http://localhost:8787
```

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
#   --backfill-latest    download the current latest version now
#   --base-model SDXL    only download versions matching this base model
#   --file-type Model    prefer this file type when a version has several

civitai-manager list
civitai-manager unsubscribe <id>

# One-shot poll of every subscription (for cron); add --download to also
# fetch queued files immediately instead of leaving them for `serve`:
civitai-manager check
civitai-manager check --download
```

## Configuration & authentication

The CivitAI **API token** and other settings resolve by precedence:

1. command-line flag (`--token`, `--base-url`, `--model-root`, `--db`)
2. environment variable **`CIVITAI_TOKEN`** (token only)
3. config file (below)
4. built-in defaults

The token is **never logged** — diagnostic output redacts it to `****abcd`.
The public read endpoints work anonymously; a token is required to download most
files.

Config file location honours `XDG_CONFIG_HOME`, defaulting to
`~/.config/civitai-manager/config.yaml`:

```yaml
token: "your-civitai-api-key"      # or set CIVITAI_TOKEN
base_url: "https://civitai.com"
model_root: "~/civitai-models"
default_poll_interval: "1h"        # floored at 15m (API edge-caches ~5m)
addr: ":8787"
# db_path: "~/.config/civitai-manager/civitai-manager.db"
```

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
  cli/                  cobra commands: serve, subscribe, list, unsubscribe, check
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
  indexing, and HTTP-error retry/fail — via `httptest` + a fake downloader.
- **Store** — migration applies to version 1; subscription CRUD + unique-target
  constraints; seen-versions ledger; queue state transitions + dedup guard.
- **Config** — flag > env > file precedence; token redaction; XDG; duration parse.
- **Web** — every page/fragment renders without panic with expected elements;
  dashboard/asset/subscribe handlers return 200 with the right content.

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

- **Library management** — the `local_files` table exists and downloads register
  into it, but the full `scan`/reconcile/supersede feature is not implemented.
- **Byte-range resume** — the SDK `Downloader` takes only a URL (no `Range`
  header), so an interrupted download is **re-fetched whole** rather than
  resumed. Interrupted rows are requeued on restart.
- **Search pagination** — search shows the first page; cursor/`Metadata` paging
  through the UI is not wired yet.

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
