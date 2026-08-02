# CLI reference

Every flag below is taken from `civitai-manager <command> --help` on v0.1.73.
Run `civitai-manager --help` for the authoritative list on your build.

- [Global flags](#global-flags)
- [`serve`](#serve)
- [`subscribe` / `list` / `unsubscribe`](#subscribe--list--unsubscribe)
- [`check`](#check)
- [`search`](#search)
- [`scan`](#scan)
- [`library`](#library)
- [`verify`](#verify)

## Global flags

These apply to every subcommand.

| Flag | Meaning |
| --- | --- |
| `--config <path>` | Config file path (default: XDG config dir) |
| `--token <token>` | CivitAI API token (overrides env/config; never logged) |
| `--base-url <url>` | CivitAI API base URL (default `https://civitai.com`) |
| `--model-root <dir>` | Root directory for downloaded models |
| `--db <path>` | SQLite database path |
| `--trash-dir <dir>` | Quarantine trash directory (default `<model-root>/.trash`) |
| `--max-file-size <size>` | Skip auto-downloads whose primary file exceeds this size (e.g. `500MB`, `2GB`; `0`/empty = unlimited) |
| `--download-jitter <dur>` | Anti-stampede window: schedule each auto-download at a random point in `[0, dur)` (e.g. `15m`; `0` = start immediately) |
| `--no-preview` | Do not write the `.preview.png` sidecar at all (the default still writes it) |
| `--max-preview-size <size>` | Skip the `.preview.png` when the fetched image exceeds this size (e.g. `2MB`; `0`/empty = no cap). The model file and `.civitai.info` are still written |
| `--outputs-dir <dir>` | Directory the output gallery copies successful workflow-run images into (default `<db-dir>/outputs`) |
| `--outputs-max-bytes <size>` | Total disk cap for the output gallery; the oldest generations are evicted above it (e.g. `20GB`; `0` = unlimited; default `20GB`) |
| `--hf-token <token>` | Optional HuggingFace token for the HuggingFace fallback resolver (overrides `HF_TOKEN` env/config; sent only to HuggingFace; never logged) |
| `-v`, `--verbose` | Verbose logging |
| `--version` | Print version, commit, and build date |

## `serve`

Runs the web UI, the subscription poller, and the download worker in one
process.

```sh
civitai-manager serve
# → open http://localhost:8787
```

| Flag | Meaning |
| --- | --- |
| `--addr <host:port>` | Listen address (default from config, `127.0.0.1:8787`); use a non-loopback host to expose the UI on your LAN |
| `--comfy-model-path <dir>` | Local ComfyUI `models/` directory root; enables the "Download & run" action to write a missing model into the correct subfolder (must be an existing writable dir) |
| `--comfy-root <dir>` | Local ComfyUI **install** root (the folder holding `custom_nodes/`); enables the "install the ComfyUI helper extension" action. Defaults to the parent of `--comfy-model-path` when that looks like a ComfyUI install |
| `--web-scan-timeout <dur>` | Deadline for a web "Scan now" (e.g. `30m`; default from config, `6h`). Bounds the web-triggered directory walk/hash. It is a runaway backstop — a large library legitimately hashes for hours and the **Stop** button ends a scan at any time — so lower it only to cap a scan that hangs |

The ComfyUI server URL itself is **config-only** (`comfy_url`, default
`http://127.0.0.1:8188`) and is deliberately never a per-request parameter, so a
run endpoint cannot be pointed at an arbitrary target. See
[configuration.md](configuration.md).

> **Read the [security notes](configuration.md#security-notes) before binding a
> non-loopback address.** The UI has no login.

## `subscribe` / `list` / `unsubscribe`

```sh
# Subscribe to a model (by id or full URL):
civitai-manager subscribe 4201
civitai-manager subscribe https://civitai.com/models/4201/realistic-vision

# Subscribe to a creator:
civitai-manager subscribe --creator someartist

civitai-manager list
civitai-manager unsubscribe <id>
```

| Flag | Meaning |
| --- | --- |
| `--creator <username>` | Subscribe to a creator username instead of a model |
| `--notify-only` | Record new versions but do not download |
| `--no-auto` | Disable auto-download for this subscription |
| `--backfill-latest` | Download the current latest version now, before returning |
| `--base-model <name>` | Only download versions matching this base model (e.g. `SDXL`) |
| `--file-type <type>` | Preferred file type to download (e.g. `Model`, `VAE`) |

First-poll behaviour is deliberately conservative: subscribing **seeds** the
ledger with the current back-catalog *without downloading it*, so a new
subscription never retro-downloads an entire history. `--backfill-latest` also
grabs the current newest version.

`unsubscribe <id>` fully removes the subscription's state — its seen-version
ledger **and** its download-queue rows — so re-subscribing to the same target
later is a clean slate that re-enqueues and re-downloads, rather than being
deduped against a stale completed row.

## `check`

One-shot poll of every subscription, suitable for `cron`.

```sh
civitai-manager check              # poll; leave new versions queued for `serve`
civitai-manager check --download   # poll, then drain the download queue now
```

| Flag | Meaning |
| --- | --- |
| `--download` | Also download queued files now (default: leave queued for `serve`) |

## `search`

Queries CivitAI's model catalog and prints a table (id, name, type, creator,
downloads, thumbs-up). Results are the first page (or `--limit`).

```sh
civitai-manager search "realistic vision"
civitai-manager search --username someartist --type Checkpoint --limit 20
civitai-manager search anime --tag style --nsfw --json
```

| Flag | Meaning |
| --- | --- |
| `--tag <tag>` | Filter by tag |
| `--username <name>` | Filter by creator username |
| `--type <type>` | Filter by model type (`Checkpoint`, `LORA`, `TextualInversion`, …) |
| `--nsfw` | Include NSFW results |
| `--limit <n>` | Max results to request (server default when 0) |
| `--json` | Print the raw API JSON instead of a table |

Pagination beyond the first page is not wired up yet.

## `scan`

Walks each `--path` (default: `model_root`), hashes model files (reusing an
mtime/size cache to skip unchanged multi-GB files), matches them to CivitAI, and
flags deletion candidates (superseded, duplicate, broken).

**`scan` never moves or renames your files** — use `library quarantine` to act on
what it flags.

```sh
civitai-manager scan
civitai-manager scan --path ~/ComfyUI/models --path ~/A1111/models/Lora
civitai-manager scan --no-remote      # local analysis only, no CivitAI API calls
```

| Flag | Meaning |
| --- | --- |
| `--path <dir>` | Directory to scan (repeatable; default: `model_root`) |
| `--no-remote` | Offline: skip all CivitAI API calls (local analysis only) |
| `--json` | Emit the report as JSON |

> **Data egress:** by default `scan` matches your files against CivitAI, which
> means **sending their SHA256 hashes to civitai.com**. `--no-remote` keeps the
> scan entirely local. (In the web UI the equivalent is the "Match against
> CivitAI" checkbox.)

`scan` **records, per file, the root it was found under**, so a candidate flagged
under an extra `--path` stays actionable by a later `quarantine` *without*
re-specifying that directory. Note the split: the extra scan *paths* are not
saved as config (you re-supply them each run), but each scanned file's
`scan_root` **is** persisted on its index row.

Safety bound: only files that **matched** a CivitAI version can ever be
quarantined. An unmatched host file scanned via an extra path is inventoried but
can never be moved.

## `library`

```sh
civitai-manager library candidates                      # list current candidates
civitai-manager library quarantine --all                # dry-run over all candidates
civitai-manager library quarantine --reason duplicate --apply
civitai-manager library quarantine --id 12 --apply
civitai-manager library quarantine --path ~/loose-loras --all --apply
civitai-manager library restore <batchID>               # undo a quarantine batch
civitai-manager library trash list                      # list quarantine batches
```

`quarantine` soft-deletes candidates by **moving** them (and their sidecars) into
the trash dir with an undo manifest. **It never hard-deletes.**

| Flag | Meaning |
| --- | --- |
| `--apply` | Actually move files (default: dry-run); **requires** a selector |
| `--all` | Quarantine every current candidate |
| `--id <id>` | Candidate id(s) to quarantine (repeatable) |
| `--reason <reason>` | Quarantine all candidates with this reason (`superseded` / `duplicate` / `broken`) |
| `--path <dir>` | Additional allowed scan root (repeatable; unioned with `model_root` and the roots recorded at scan time) |

Without `--apply` it is a **dry-run** that prints exactly what would move; a bare
`quarantine` (no selector) dry-runs over all current candidates. `--apply`
**requires** an explicit selector (`--id`, `--reason`, or `--all`) so the
destructive path always names its targets.

`restore` returns a quarantined batch's files to their original locations **and**
re-indexes each restored model file, so it reappears in `library candidates` and
on the Library page immediately.

## `verify`

Reconciles the files civitai-manager downloaded (the completed download-queue
rows) against what is actually on disk.

```sh
civitai-manager verify                          # report: OK / MISSING / CORRUPT counts
civitai-manager verify --check-hash             # also re-hash present files (slower)
civitai-manager verify --repair                 # re-download files reported MISSING
civitai-manager verify --repair --check-hash    # also re-download CORRUPT files
```

| Flag | Meaning |
| --- | --- |
| `--check-hash` | Also re-hash present files and flag content that no longer matches (slower) |
| `--repair` | Re-download files reported MISSING (and CORRUPT, with `--check-hash`) |

Plain `verify` only reports and exits 0. `--repair` re-enqueues each offending
file (its done row → queued) and re-downloads **only** those rows through the
normal verify pipeline; an unrelated queued backlog is left untouched.

This is the path that recovers a model you deleted or moved on disk — a normal
re-subscribe/`check` cannot, because the version is already "seen". If a repair
re-download itself fails (e.g. the source 404s), the row is re-detected as
repairable on the next `verify`, so you can simply re-run it.

## Download output

By default the one-shot download commands (`subscribe --backfill-latest`,
`check --download`) print friendly progress/summary lines; add `-v` for the
detailed structured worker/poller logs.

Each completed download prints a per-file verification line. The name shown is
the **on-disk** file name (files are written version-name-cased), not the API's
file name — so the printed name is exactly what you will find on disk:

```
✓ EasyNegative.safetensors (sha256 c74b4e810b03 verified)
⚠ some-model.safetensors (unverified — no hash from API)
```

A `⚠ unverified` line means the API supplied no hash for that file, so it was
downloaded but could not be checksum-verified. It is never reported as
"verified".

Downloaded files are laid out as
`<model_root>/<type>/<creator>/<model>/<versionName>.<ext>` with sanitized path
components, plus `.civitai.info` (raw version JSON) and `.preview.png` sidecars.
