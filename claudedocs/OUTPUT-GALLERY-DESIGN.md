# Output Gallery — Design Proposal

**Status:** proposal (no code written). Author: backend-architect recon pass.
**Scope:** durable capture + browse of ComfyUI run outputs in civitai-manager.
**Target release line:** v0.1.x (next feature slice after v0.1.51).

---

## 0. Locked decisions (from the user — do not relitigate)

1. **Copy into app storage.** On run completion, copy the output PNG(s) out of
   ComfyUI's output dir into a civitai-manager-owned outputs directory, so a
   generation survives ComfyUI clearing/overwriting its output folder.
2. **Auto-capture every successful run** (not opt-in).
3. **Global "Outputs" gallery** (new nav item) **PLUS** a per-workflow section on
   the workflow detail page.

Everything below designs *around* these three. Where a decision has a real
trade-off (NSFW handling, Trash integration, retention caps), it is called out as
an open question rather than silently chosen.

---

## 1. What I verified in the code (evidence) vs. what I'm proposing

**Verified (read the source):**

- **Run → images path.** `internal/web/run_handlers.go`: `realRun` loads the
  workflow → (UI→API convert) → preflight → `client.Submit` → polls
  `client.History` until settled, then returns
  `&runResult{Images: hist.AllImages(), PromptID: promptID}`. `runResult.Images`
  is `[]comfy.ImageRef{Filename, Subfolder, Type}` (`internal/comfy/client.go`
  L324-329). `HistoryEntry.AllImages()` (L378) flattens every node output's
  `Images` — **only the `images` key**, not `gifs`/`videos`/`audio`.
- **Settle path.** The run goroutine in `startRun` calls
  `run(ctx, wf, up, opts)` then, **under `runMu`**, `applyRunOutcomeLocked` —
  which on success sets `job.phase = runPhaseDone`, `job.images = res.Images`,
  `job.promptID = res.PromptID`. The job is **in-memory only** (`runJob`), single
  active run globally (`s.runMu` + the `s.runJob != nil && running` guard). Zero
  persistence — outputs are lost when the next run replaces `s.runJob`.
- **Serving today.** `handleWorkflowRunView` (L543) is a **live proxy**: it
  validates `prompt == active run promptID` AND the exact `ImageRef` is one of the
  run's images (`imageAllowed`), then `client.View(ctx, ref)` fetches bytes from
  ComfyUI's `/view` and streams them with `Content-Type` forced to `image/*` or
  `application/octet-stream`, `X-Content-Type-Options: nosniff`,
  `Cache-Control: no-store`. Loopback-gated (`s.gate(w)`). Because it keys off the
  **active** run's promptID, it stops working the moment the next run starts —
  this is exactly the persistence gap.
- **`client.View`** (L446) fetches `/view?filename&subfolder&type`, bounded read
  at `maxImageBytes = 64 MiB`, returns `([]byte, contentType, error)`. **This is
  an HTTP fetch, not a filesystem read** — so it works even when ComfyUI runs on a
  different host (important for the "different host" edge case, §7).
- **Store / migrations.** `internal/store/store.go`: embedded `migrations/*.sql`,
  applied in **filename order**, each in its own tx, tracked in
  `schema_migrations`. Latest is `0011_workflow_graph_hash.sql` → **next is
  `0012_*.sql`**. Timestamps are **RFC3339 UTC strings** (`nowRFC3339`,
  `formatTime`). Nullable FKs use the `local_files`/`workflows` convention
  (nullable INTEGER columns, `nullInt`). `PRAGMA foreign_keys = ON` is set.
- **Config / paths.** `internal/config/config.go`: `ModelRoot` default
  `~/civitai-models`; `DBPath` default `<XDG config>/civitai-manager/civitai-manager.db`
  (i.e. `~/.config/civitai-manager/`). `ComfyModelPath` is ComfyUI's *models*
  dir (separate concept). `ParseSize` parses `"500MB"`/`"2GB"`;
  `ValidateWritableDir` probe-checks a writable dir. `web.Config` (server.go L33)
  mirrors a subset of config into the server.
- **Atomic write primitive.** `internal/comfy/download_target.go`:
  `WriteModelStream`/`WriteModelStreamVerified` = temp-file-in-same-dir → fsync →
  atomic rename, size-capped, **refuses to overwrite** (`ErrDestExists`),
  best-effort free-disk pre-check. `SafeModelDest(root, subdir, refName)` collapses
  any traversal to a basename and asserts containment inside `root`.
- **Nav / layout.** `internal/web/layout.go`: `navbar` renders `navLink` entries
  (Dashboard / Models / Workflows / Apps / Library / Trash). `page(...)` is the
  shell; NSFW mode + theme are threaded through every page.
- **NSFW + gallery components.** `NSFWHide|NSFWBlur|NSFWShow`
  (`model_pages.go` L26-28), `s.nsfwMode()`. Reusable gallery bits:
  `lightboxOverlay()` + `cmOpenLightbox`/`cmReveal` JS (`model_pages.go` L1229),
  the `.cm-masonry`/`.cm-masonry-item`/`.cm-blur`/`.cm-reveal` pattern
  (`model_community_pages.go` L58-140). **NSFW `hide` OMITS server-side** (URL
  never reaches the DOM) — an invariant.
- **Workflow detail page.** `workflowDetailPage` (`workflow_pages.go` L702)
  assembles cards; the run panel is inserted as `runSection`. A per-workflow
  Outputs card slots in right after the run panel.
- **Routes.** `internal/web/server.go` registers `mux.HandleFunc("METHOD /path", …)`
  with `{id}` path params; CSRF via `s.verifyCSRF`, loopback via `s.gate`.

**Proposed (design choices, not yet in code):** the `generations` +
`generation_images` schema, the `<data dir>/outputs/` storage root + filename
scheme, the capture hook placement, the four new routes, the gallery/detail UX,
and the retention model. All detailed below.

---

## 2. Schema + migration (`0012_output_generations.sql`)

**A "generation" = one run = one row**, grouping **N images**. Images live in a
child table (not a JSON column) so the gallery grid, per-image serving by id, and
per-image delete are all indexed SQL rather than JSON surgery.

```sql
-- 0012_output_generations.sql
-- Durable capture of ComfyUI workflow-run outputs. One run = one `generations`
-- row grouping N `generation_images`. The workflow may be deleted later, so
-- workflow_id is nullable (ON DELETE SET NULL) and workflow_name is a snapshot
-- taken at capture time so the gallery still labels an orphaned generation.
CREATE TABLE generations (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  workflow_id    INTEGER,                    -- civitai-manager workflow id, nullable
  workflow_name  TEXT NOT NULL DEFAULT '',   -- snapshot of the workflow name at run time
  prompt_id      TEXT NOT NULL,              -- ComfyUI prompt id (also the on-disk dir name)
  base_model     TEXT,                       -- snapshot of wf.BaseModel (nullable)
  graph_hash     TEXT,                       -- snapshot of the workflow graph_hash (nullable)
  params         TEXT,                       -- JSON snapshot: applied overrides + resources (see §3)
  status         TEXT NOT NULL DEFAULT 'ready', -- 'ready' | 'partial'  (see capture semantics §3)
  image_count    INTEGER NOT NULL DEFAULT 0, -- denormalized N for cheap grid rendering
  created_at     TEXT NOT NULL,              -- RFC3339 UTC
  FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE SET NULL
);

CREATE INDEX ix_generations_created  ON generations(created_at DESC, id DESC);
CREATE INDEX ix_generations_workflow ON generations(workflow_id);
CREATE INDEX ix_generations_prompt   ON generations(prompt_id);

CREATE TABLE generation_images (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  generation_id  INTEGER NOT NULL,
  idx            INTEGER NOT NULL,           -- 0-based position within the generation
  rel_path       TEXT NOT NULL,              -- path RELATIVE to the outputs root (e.g. "<prompt_id>/0-foo.png")
  filename       TEXT NOT NULL,              -- original ComfyUI basename (display/alt)
  content_type   TEXT NOT NULL DEFAULT 'image/png',
  size_bytes     INTEGER NOT NULL DEFAULT 0,
  sha256         TEXT,                        -- nullable; reserved for future dedup (§2 note)
  created_at     TEXT NOT NULL,
  FOREIGN KEY (generation_id) REFERENCES generations(id) ON DELETE CASCADE
);

CREATE INDEX ix_generation_images_gen ON generation_images(generation_id, idx);
```

**Design notes / rationale:**

- **Child table over JSON column.** Serving one image by `generation_images.id`
  (`GET /outputs/img/{imageID}`) and per-image delete both want an indexed row.
  A JSON blob would force a full-row read + re-serialize for every image byte
  fetch. The denormalized `image_count` on the parent keeps the grid query from
  needing a join/aggregate.
- **`ON DELETE SET NULL` for workflow_id** (matches locked decision "keep the
  generation with the name snapshot" when the source workflow is deleted).
  `workflow_name`/`base_model`/`graph_hash` are **snapshots** so an orphaned
  generation is still fully labeled. Requires `PRAGMA foreign_keys = ON` — already
  set in `store.Open`.
- **`ON DELETE CASCADE` for images** so deleting a generation row drops its image
  rows; the file cleanup is done by the store method *before* the DB delete (§6).
- **`rel_path` is root-relative, never absolute.** The outputs root can move
  (config change / different machine); storing a relative path keeps rows portable
  and forces every read through the containment helper (§4). **The DB is the only
  source of the path** — no user input ever forms a path.
- **`status`.** `'ready'` = all images copied; `'partial'` = the run succeeded but
  one or more `View` fetches/writes failed (best-effort capture, §3). No
  `'capturing'` transient is persisted — capture happens in one shot after settle
  and inserts the row only once outcomes are known (avoids a stuck-row class).
- **`sha256` nullable, unused in MVP.** Reserved so a later phase can add
  content-dedup (skip storing a byte-identical output, or hard-link) without a
  second migration. **Not populated in MVP** (KISS/YAGNI).
- **Idempotent-ish.** Follows repo convention: plain `CREATE TABLE` (no `IF NOT
  EXISTS` — migrations run exactly once, guarded by `schema_migrations`), indexes
  created inline, no backfill (there is nothing to backfill — capture is
  forward-only).

**New `Store` methods** (mirroring the `workflows.go` style — `scanX` helper,
`nullStr`/`nullInt`, RFC3339 times):

- `InsertGeneration(ctx, *Generation) (int64, error)` — inserts the parent + all
  image rows in **one tx**, sets `image_count`.
- `ListGenerations(ctx, ListGenerationsOpts) ([]Generation, error)` — newest
  first, optional `WorkflowID` filter, `Limit`/`Offset` (keyset by
  `(created_at, id)` for stable pagination).
- `CountGenerations(ctx, workflowID *int64) (int, error)` — for pagination + the
  per-workflow section header.
- `GetGeneration(ctx, id) (*Generation, []GenerationImage, error)` — detail page.
- `GetGenerationImage(ctx, imageID) (*GenerationImage, error)` — the byte-serving
  route.
- `DeleteGeneration(ctx, id) ([]string, error)` — returns the rel_paths to unlink
  (caller removes files, then the CASCADE drops image rows), then deletes parent.
- `DeleteGenerationsByWorkflow(ctx, workflowID) ([]string, error)` — bulk delete.
- `SumGenerationBytes(ctx) (int64, error)` + `OldestGenerations(ctx, n)` — for the
  optional retention cap (§6).

---

## 3. Capture hook

**Where.** In `startRun`'s run goroutine (`run_handlers.go` ~L167-185), **after**
`applyRunOutcomeLocked` settles the job, **outside `runMu`**, on the success path
only. Sketch (not final code):

```go
go func() {
    defer cancel()
    ... res, err = run(ctx, wf, up, opts) ...
    s.runMu.Lock()
    s.applyRunOutcomeLocked(job, res, err)
    phase := job.phase
    s.runMu.Unlock()
    // Best-effort capture: never let a capture failure change the run outcome.
    if phase == runPhaseDone && res != nil && len(res.Images) > 0 {
        s.captureGeneration(wf, opts, res) // own bounded context; logs+swallows errors
    }
}()
```

**Why here, not inside `realRun`:**

- `realRun` is the **test seam** (`s.runFn`); putting capture in the goroutine
  keeps it working for both `realRun` and any injected `runFn`, and lets tests
  disable capture via a nil/seam `captureFn`.
- Capture does **network `View` fetches + disk writes** — it must **NOT hold
  `runMu`** (that mutex guards the tiny in-memory job snapshot; a poll must never
  block on a 64 MiB image fetch). Doing it after releasing the lock is mandatory
  for the race-safety invariant.
- The run `ctx` gets `cancel()`'d by the `defer`; capture therefore uses its
  **own** `context.WithTimeout(s.baseCtx, captureBudget)` (e.g. 60s) so a slow
  `View` can't wedge but a cancelled run ctx doesn't abort a legitimate copy.

**Best-effort semantics (never fail the run):**

- `captureGeneration` wraps everything; **any error is logged and swallowed** —
  the run already reported "Run complete" to the user. A failed capture must not
  turn a good generation into a visible failure.
- Per-image: fetch via `client.View(ctx, ref)` → atomic write to disk. If some
  images copy and others fail, insert the generation with the images that landed
  and `status='partial'` (the detail page shows a subtle "some outputs could not
  be saved" note). If **zero** land, insert nothing (log only).
- Re-entrancy: single active run globally (existing invariant) ⇒ captures are
  serialized; no concurrent-capture race.

**What the snapshot stores (`generations.params` JSON):**

```json
{
  "substitute":   { "missing.safetensors": "installed.safetensors" },
  "option_fixes": [ { "input": "...", "old": "...", "new": "..." } ],
  "resources":    [ "sdxl.safetensors", "lora_x.safetensors" ],
  "base_model":   "SDXL 1.0",
  "format":       "ui"
}
```

- `substitute` + `option_fixes` come straight from the `runOptions` that were
  applied (this is exactly what "re-run with the captured params" needs — see §5).
  `option_fixes` is a list-of-objects form of `opts.OptionFixes` (the map key
  `comfy.OptionFixKey` is a struct, so serialize as an explicit list).
- `resources` = `wf.Resources` (the referenced model filenames), `base_model` =
  `wf.BaseModel`, `graph_hash` = `wf.GraphHash` — all **snapshots at run time** so
  the record is meaningful even if the workflow later changes or is deleted.
- **Deliberately NOT stored:** the full API graph. It can be large, it's already
  on the workflow row (when the workflow still exists), and re-run reconstructs
  from `workflow_id` + the saved `runOptions`. (Open question §8: should we snapshot
  the graph so re-run survives workflow deletion? Costs storage; default = no.)

---

## 4. Storage layout + serving

### 4.1 On-disk location

**Default: `<dir(DBPath)>/outputs/`** — i.e. `~/.config/civitai-manager/outputs/`,
right next to the SQLite DB. Rationale:

- It's **app-owned application data**, the same class as the DB — not model weights
  (so **not** under `ModelRoot`, which would mix generated images into the model
  library the scanner walks) and not ComfyUI's own dir (the whole point is to
  escape ComfyUI clearing it).
- Co-locating with the DB keeps "the app's state" in one backup-able tree.

**Config knob (yes, add one):** `outputs_dir` (YAML) + `--outputs-dir` (flag),
resolved flag > file > default, `expandHome`'d, `ValidateWritableDir`'d at load
(same treatment as `ComfyModelPath`). Thread it into `web.Config` as
`OutputsDir string`. Reasons a knob is warranted: users with a small home volume
will want outputs (potentially many GB) on a data disk; and it mirrors the
existing `TrashDir`/`ModelRoot` precedent of "app writes here, let me relocate it."

Add companion caps (§6): `max_outputs_bytes` / `max_outputs_count` (parsed via
`ParseSize`), default unset = unlimited.

### 4.2 Filename scheme (collision-free across runs)

```
<outputs_dir>/<prompt_id>/<idx>-<sanitized-basename>
   e.g.  ~/.config/civitai-manager/outputs/abc123.../0-ComfyUI_00012_.png
```

- **`prompt_id` as the per-generation subdirectory** — it's the ComfyUI prompt id
  minted per run (`comfy.NewID`), unique per run, and known at capture time
  **without needing the DB autoincrement id first**. This avoids the
  insert-row-then-write ordering hazard.
- **`<idx>-` prefix** disambiguates multiple images from one run; `idx` is the
  0-based position in `res.Images`.
- **`<sanitized-basename>`** = `path.Base` of the ComfyUI filename with any
  traversal/separator collapsed (reuse the `SafeModelDest` basename discipline).
  ComfyUI already returns bare filenames, but we never trust the untrusted comfy
  server — the basename is sanitized and the final path is asserted contained.

### 4.3 Writing to disk (disk-safety)

- **Bytes are already in memory** (`client.View` returns `[]byte`, bounded at
  `maxImageBytes = 64 MiB`). So capture writes from a `bytes.Reader`.
- **Reuse the atomic-write discipline** from `WriteModelStream` (temp-in-same-dir
  → fsync → atomic rename). Two options:
  1. **Call `comfy.WriteModelStream(dest, bytes.NewReader(b), int64(len(b)), cap)`
     directly** — it already does temp+rename+fsync+cap+refuse-overwrite. Cleanest
     reuse; the `refuse-overwrite` (`ErrDestExists`) is harmless because
     `<prompt_id>/<idx>-…` is unique per run.
  2. Or factor a tiny `internal/outputs` (or `store`-adjacent) `writeAtomic`
     helper if we don't want the web/capture layer importing `comfy`'s writer.
  **Recommendation: option 1 for MVP** (DRY, already audited), revisit if the
  import coupling bothers us.
- **Per-file guard:** the 64 MiB `View` bound already caps a single image. Add a
  **per-generation cap** (refuse absurd N) and the **global retention cap** (§6).
- **`MkdirAll(<outputs_dir>/<prompt_id>, 0o755)`** before writing (the atomic
  writer already `MkdirAll`s the dest dir).
- **Dedup:** out of scope for MVP (`sha256` column reserved). Note: for
  batch-of-identical-seeds runs, content-hash dedup (hard-link or skip) is a
  future win; deferred.

### 4.4 Serving the stored files

**Serve by image id, from disk, path-contained — NOT a proxy** (the whole point is
we own the bytes now):

- `GET /outputs/img/{imageID}` → `GetGenerationImage(imageID)` → resolve
  `filepath.Join(outputsRoot, rel_path)` through a **`SafeOutputPath`-style
  containment check** (assert the resolved path is inside `outputsRoot`, reject
  any `..`), `os.Open`, stream with `Content-Type` from the stored
  `content_type` **restricted to `image/*`** (else `application/octet-stream`),
  `X-Content-Type-Options: nosniff`. Unlike the live `/view` proxy this **can**
  set a real `Cache-Control` (the file is immutable once written) —
  `Cache-Control: private, max-age=…, immutable`.
- **Path containment is mandatory** even though paths come from the DB: defense in
  depth (a corrupted row must never read outside the outputs root). Reuse the
  `filepath.Rel` + prefix check from `SafeModelDest`.
- **Gating:** this is a **GET, read-only, app-owned data** route → **no CSRF**.
  On loopback-gating: the live `/view` proxy is loopback-gated because it *reaches
  ComfyUI*; serving local app files does not. **Proposal:** do **not**
  loopback-gate the image GET (so the gallery works if the user deliberately binds
  the UI to a LAN address, same as every other page), **but** keep it strictly
  id-indexed + path-contained + content-type-restricted. (Open question §8: if we
  want outputs treated as more sensitive than model previews, loopback-gate it too
  — cheap to add, one `s.gate(w)` line.)

---

## 5. Gallery UX

### 5.1 Global `/outputs` page (`GET /outputs`)

- **New nav entry** in `navbar` (`layout.go`): `navLink("/outputs", "Outputs")`,
  placed after "Workflows" (outputs come from workflow runs).
- **Grid/masonry, newest first.** Reuse `.cm-masonry` / `.cm-masonry-item` (already
  in `app.css`, survives Tailwind purge). Each tile = the **first image** of a
  generation as a lazy-loaded thumbnail (`GET /outputs/img/{firstImageID}`),
  captioned with `workflow_name` (or "workflow #id" / "(deleted workflow)") + a
  relative time + `×N` when `image_count > 1`. Tile links to the per-generation
  detail page (`/outputs/{id}`).
- **Filter by workflow.** `?workflow=<id>` (a labeled `<select>` of workflows that
  have generations, reusing `labeledSelect`). Empty = all.
- **Pagination.** `?page=N` server-side `LIMIT/OFFSET` (or keyset on
  `(created_at, id)` for stability). Page size ~48. Prev/Next controls; total from
  `CountGenerations`. **Never load all rows/bytes** — the grid query selects only
  `(id, first_image_id, workflow_name, image_count, created_at)`.
- **NSFW.** See §7 — MVP renders outputs plain (they are the user's own local
  generations with no rating signal); the open question is whether to respect
  `blur` mode globally.
- **Empty state:** a friendly "No generations yet — run a workflow and its outputs
  will appear here."

### 5.2 Per-generation detail (`GET /outputs/{id}`)

- **Full image(s)** in a grid, each opening the existing **`lightboxOverlay()` +
  `cmOpenLightbox`** (reuse verbatim — offline, theme-aware, no new JS).
- **Params panel:** rendered from `generations.params` — applied substitutions,
  option fixes, resources, base model, graph hash, prompt id, timestamp. All
  **escaped** (`g.Text`) — comfy filenames + user graph strings are untrusted.
- **"Re-run this"** (reuses the override mechanism): a CSRF+loopback-gated
  `POST /outputs/{id}/rerun` that: loads the generation, reconstructs
  `runOptions{Substitute, OptionFixes}` from the saved `params`, verifies
  `workflow_id` is non-null and the workflow still exists, and calls
  `s.startRun(wf, opts)` — then redirects/HX-swaps to the workflow's run panel.
  **Disabled (with a tooltip) when `workflow_id` is null** ("source workflow was
  deleted"). This is the deterministic reuse of the existing run path — no new run
  logic.
- **Delete** (`POST /outputs/{id}/delete`, CSRF + loopback): removes files + rows
  (§6), redirects to `/outputs`.

### 5.3 Per-workflow section (workflow detail page)

- In `workflowDetailPage` (`workflow_pages.go`), **after the run panel**, add an
  "Outputs" `card` when `CountGenerations(workflowID) > 0`: the same masonry grid
  fragment (reused renderer) limited to the most recent ~12, with a "View all →"
  link to `/outputs?workflow=<id>`. A "Delete all outputs for this workflow"
  control (CSRF + loopback) maps to `DeleteGenerationsByWorkflow`.
- The grid tile renderer is a **single shared function** used by both the global
  page and this section (DRY).

---

## 6. Retention / lifecycle

- **Delete one generation** — `POST /outputs/{id}/delete`: `DeleteGeneration`
  returns rel_paths → unlink each file → remove the (now-empty) `<prompt_id>` dir →
  DB delete (CASCADE drops image rows). Order: **files first, then rows** (an
  orphaned file is a benign leak; an orphaned row that 404s on serve is worse).
- **Delete all for a workflow** — `POST /workflows/{id}/outputs/delete-all` (or
  `/outputs/delete-all?workflow=`): `DeleteGenerationsByWorkflow`, same file-first
  discipline.
- **Workflow deleted** — handled by `ON DELETE SET NULL` (§2): the generation and
  its files **survive**, labeled by the `workflow_name` snapshot, "re-run"
  disabled. (This is the locked decision.)
- **Optional size/count cap** (`max_outputs_bytes` / `max_outputs_count`,
  default unset = unlimited): enforced **after each successful capture** —
  `SumGenerationBytes`/`CountGenerations`, then evict **oldest-first**
  (`OldestGenerations`) until under cap. Eviction reuses `DeleteGeneration`
  (files + rows). Simple, predictable, no background sweeper needed.
- **Trash integration — recommend NO for MVP.** The existing Trash
  (`quarantine.go`) is a **reversible move + manifest for model files** flagged by
  the library analyzer; its undo semantics and manifest schema are model-file
  specific. Routing generated images through it adds real complexity for little
  value (a regenerated image is cheap to recreate vs. a multi-GB model you don't
  want to redownload). **Proposal:** hard-delete outputs with a confirm. (Open
  question §8: a lightweight "recently deleted" soft-delete flag on `generations`
  is a cheaper middle ground than full Trash if undo is wanted.)

---

## 7. Edge cases / risks

- **ComfyUI on a different host.** *Handled naturally* — capture copies via
  `client.View` (an HTTP `/view` fetch), not a filesystem read of ComfyUI's output
  dir. So a remote ComfyUI captures fine as long as the run itself worked and
  `/view` is reachable. (Contrast: the `comfy_model_path` download flow *does* need
  a local FS and degrades off-host — capture does not share that limitation.) If
  `/view` fails per-image, best-effort → `status='partial'`.
- **Non-image outputs (video/audio/gifs).** `AllImages()` only flattens the
  `images` key, so today's run + this capture handle **images only** — consistent
  with current behavior. Video/animated outputs (`gifs`/`videos` node-output keys)
  are **out of scope for MVP**; the schema (`content_type`) and the lightbox
  (already has a `<video>` counterpart) can accommodate them later. Called out as
  a follow-up, not silently dropped.
- **Huge galleries.** Server-side pagination + lazy `loading="lazy"` thumbnails;
  grid query selects only metadata columns; bytes streamed per-request from disk
  (never all in memory). `image_count` denormalized to avoid per-row aggregates.
- **Concurrent runs.** Single active run globally (existing `runMu` invariant) ⇒
  captures are serialized ⇒ no concurrent-capture contention. If the single-run
  invariant is ever relaxed, capture keys on unique `prompt_id` dirs so it stays
  collision-free.
- **Backfill.** **None** — capture is forward-only. In-memory `runJob` images from
  before the feature (and any run that completed before this ships) are lost;
  documented as expected.
- **Disk exhaustion.** Atomic writer's free-disk pre-check + per-image 64 MiB
  bound + optional global cap. A full disk fails the write → best-effort
  `status='partial'`/skip, never a crash.
- **Untrusted comfy filenames.** Sanitized to a basename + path-contained on write
  and on serve; escaped on render. A hostile comfy cannot write outside the outputs
  root or inject markup.
- **Invariants preserved:** offline/no-CDN (images are same-origin `/outputs/img/…`
  routes, no external refs); theme-aware (reuses existing themed components); CSRF
  on every new POST (rerun/delete/delete-all); loopback-gating on the
  state-changing + comfy-reaching endpoints (rerun reaches ComfyUI → gated). NSFW:
  see below.
- **NSFW invariant — the one genuine open decision.** Outputs are the **user's own
  local generations** with **no rating metadata** (unlike civitai showcase/community
  images, which carry `nsfwLevel`). So `hide`-mode's "omit server-side" has nothing
  to key on. Options: **(a)** render outputs plain regardless of mode (they're the
  user's own content) — simplest, MVP default; **(b)** respect `blur` mode by
  blurring all output thumbnails behind click-to-reveal (safe-for-shoulder-surfing);
  **(c)** add a per-generation `nsfw` flag the user can toggle. **Proposal: (a) for
  MVP**, flagged as open (§8) because it's a user-facing sensitivity call.

---

## 8. Phased implementation plan + test plan + open questions

### Phase 1 — MVP (capture + view + global gallery)

1. Migration `0012_output_generations.sql` + `store` methods (Insert/List/Count/
   Get/GetImage/Delete) with a `generations.go` + `generations_test.go`.
2. Config: `outputs_dir` knob (+ `--outputs-dir`), default `<dir(DBPath)>/outputs`,
   validated writable; thread `OutputsDir` into `web.Config`.
3. Capture hook in `startRun`'s goroutine + `captureGeneration` (View → atomic
   write via `comfy.WriteModelStream` → `InsertGeneration`), best-effort, own
   bounded context, logs+swallows.
4. Routes: `GET /outputs` (grid, pagination, `?workflow=`),
   `GET /outputs/img/{imageID}` (path-contained serve).
5. Nav entry.

### Phase 2 — detail + re-run + delete

6. `GET /outputs/{id}` detail (full images + lightbox + params panel).
7. `POST /outputs/{id}/rerun` (reconstruct `runOptions`, `startRun`), CSRF+gate,
   disabled when workflow deleted.
8. `POST /outputs/{id}/delete` (files-then-rows).

### Phase 3 — per-workflow section + retention polish

9. Per-workflow Outputs card on the detail page (shared grid renderer) +
   "Delete all for this workflow".
10. Optional `max_outputs_bytes`/`max_outputs_count` caps + oldest-first eviction.

### Phase 4 — later (not committed)

11. Video/animated output capture (`gifs`/`videos` keys). Content-hash dedup
    (`sha256` column). Optional per-generation NSFW flag / soft-delete undo.

### Test plan

- **Store (unit, `:memory:`):** insert generation + N images in one tx →
  round-trip Get; List ordering (newest first) + workflow filter + pagination
  bounds; `image_count` correctness; **`ON DELETE SET NULL`** (delete workflow →
  generation survives, `workflow_id` NULL, name snapshot intact); **CASCADE**
  (delete generation → image rows gone); `DeleteGeneration` returns correct
  rel_paths; `SumGenerationBytes`/`OldestGenerations`.
- **Storage/write (unit):** `SafeOutputPath` containment (reject `..`, absolute,
  cross-root); atomic write leaves no temp on failure; unique `<prompt_id>/<idx>`
  naming; sanitized basename for a hostile comfy filename.
- **Capture (unit, fake comfy client — the existing `comfyClient` seam):** a run
  returning 2 images writes 2 files + a `ready` row; a `View` error on one image →
  `partial` with 1 image; **a capture panic/error never changes the run outcome**
  (run still reports done); capture uses a fresh context (not the cancelled run
  ctx).
- **Web (httptest, the repo's `web_test.go` pattern):** `/outputs` renders tiles
  newest-first + paginates; `?workflow=` filters; `/outputs/img/{id}` serves bytes
  with `image/*` + `nosniff`; a bad/foreign image id 404s; `/outputs/{id}/delete`
  requires CSRF (403 without) and removes files+rows; `/outputs/{id}/rerun`
  requires CSRF + is loopback-gated + disabled for a deleted workflow; NSFW-mode
  rendering matches the chosen policy; nav shows "Outputs".
- **Live-verify (per repo convention — HTTP-level, no browser):** against a
  **throwaway temp DB** (not the real `~/.config/...db`), seed a workflow, run it
  against the local ComfyUI (`127.0.0.1:8188`), confirm files land under
  `outputs/<prompt_id>/`, `/outputs` shows the generation, `/outputs/img/{id}`
  returns the PNG bytes, and "re-run" re-enqueues. Do **not** exercise
  rerun/delete against the real DB (creates real runs / deletes real files).

### Open questions for the user

1. **NSFW policy for outputs** (§7): (a) always plain [MVP default], (b) respect
   `blur` mode, or (c) per-generation flag? Your call — it's a sensitivity choice.
2. **Loopback-gate the image GET** (`/outputs/img/{id}`)? Default proposal: no
   (app data, works on a LAN bind like every page). Gate it if outputs are
   "more private" than model previews.
3. **Snapshot the API graph** in `generations.params` so "re-run" survives
   workflow deletion? Default: no (storage cost; re-run needs the live workflow).
4. **Trash vs. hard-delete** (§6): hard-delete [MVP default], or a lightweight
   soft-delete "recently deleted" undo?
5. **Retention cap default:** unlimited [proposed], or a sane default (e.g. 20 GB
   / 2000 generations) out of the box?
6. **`outputs_dir` default location:** `~/.config/civitai-manager/outputs`
   [proposed, next to the DB] vs. under `ModelRoot`?

### Blast-radius / risk note

This introduces: **a new migration** (`0012`), **filesystem writes** (a new
app-owned data tree + atomic copies on every run), **capture wired into the hot
run-settle path** (must be best-effort + off the run mutex), and **new web routes**
including two **state-changing** ones (rerun reaches ComfyUI + spends compute;
delete removes files). That combination — migration + egress-adjacent capture +
untrusted-comfy filenames + filesystem writes + new POST endpoints — is
**high-blast-radius** and **must go through full `/audit-pr`** (per the repo
convention: migrations, filesystem writes, untrusted input, and state-changing
web endpoints all trip the "full adversarial audit" bar), plus the deterministic
`verify-agent` gate (`GOPRIVATE` set) and HTTP-level live-verify against the
dogfood binary before merge. Reversibility: the migration is additive
(reversible by dropping two tables); the on-disk outputs tree is new (nothing
existing is moved/overwritten); capture is best-effort (can be feature-flagged
off). Classify as **costly-but-reversible**.

---

## 9. Shipped follow-ups (post-v0.1.68)

Three items the v0.1.68 audit deliberately deferred, now implemented.

### 9.1 Capture on the download-and-run path

v0.1.68 wired capture into `startRun` only. `startDownloadAndRun` (plain
"Download & run" **and** "install option and run") settled its outcome under
`s.runMu` with a `defer Unlock()` and had **no capture**, so those successes never
reached the gallery.

Both paths now end in **one shared tail**, `(*Server).settleAndCapture` in
`run_handlers.go`. Its ordering is load-bearing and must not be reshuffled:

1. `s.runMu.Lock()` → `applyRunOutcomeLocked` → snapshot `job.phase` → `Unlock()`
   (the phase MUST be read under the same lock that settled it).
2. Capture strictly **outside** `runMu` — it does network `/view` fetches + disk
   writes and must never block a status poll or hold the run mutex. This is why
   `startDownloadAndRun`'s `defer s.runMu.Unlock()` is gone.
3. Capture on the **success path only** (`phase == runPhaseDone && res != nil &&
   len(res.Images) > 0`), fully `recover()`-guarded, honouring the `captureFn`
   seam (nil → `captureGeneration`).

A `downloadFn` seam (mirroring `runFn`) was added so the download-and-run
goroutine can be driven in tests without network or disk.

### 9.2 Size-cap error semantics in the comfy client

`comfy.readBounded` was `io.ReadAll(io.LimitReader(r, max))` — an oversized body
was **silently truncated**. For `View` (`maxImageBytes` = 64 MiB) that meant a
corrupt/partial image could be captured and stored as if fine.

It now reads `max+1` and, on overflow, returns the **truncated bytes together with
an `ErrResponseTooLarge`-wrapped error**. Returning the data is deliberate: the
`data, _ := readBounded(...)` **error-snippet** call sites (non-2xx bodies for
`statusError`) keep working unchanged, while every call site that **parses or
stores** the payload hard-fails with an explicit "response too large":
`object_info`, `history`, `queue`, `system_stats`, `view`, and the `submit`
HTTP-200 parse. The `View` error names the offending filename so
`captureGeneration`'s warn-and-skip log is actionable.

Boundary: a body of **exactly** `max` bytes still succeeds; `max+1` errors.

### 9.3 Total outputs disk cap + eviction

Answers open question §8.5 ("retention cap default") and implements §6's optional
cap — under the final name **`outputs_max_bytes`**.

- **Config knob `outputs_max_bytes`** (`internal/config`), plumbed exactly like
  `outputs_dir`: YAML key, `Flags.OutputsMaxBytes`, CLI flag
  **`--outputs-max-bytes`** (a human size string like `20GB`, or a byte count),
  and `web.Config.OutputsMaxBytes`.
- **Default 20 GiB** (`DefaultOutputsMaxBytes`). **`0` (or a negative value) means
  UNLIMITED** — no eviction ever. The field is a `*int64` so an *unset* key is
  distinguishable from an explicit `0`; always read it through
  `Config.OutputsCapBytes()`.
- **Enforcement** runs in `(*Server).enforceOutputsCap`, called **after a
  successful capture insert** in `captureGeneration`. While
  `SumGenerationImageBytes` exceeds the cap it deletes the **oldest** generations
  (`ListOldestGenerations`, `created_at ASC, id ASC`) — rows via
  `store.DeleteGeneration`, files via the shared `removeOutputFiles` helper, whose
  every unlink routes through `safeOutputPath` (path containment is a hard
  invariant; do not open-code `os.Remove` on a `rel_path`).
- **Guarantees:** the just-inserted generation is **never** evicted; the loop is
  bounded by `maxEvictionBatch` (500) so it is provably finite even if recorded
  sizes disagree with disk; it stops the moment the total is back under the cap;
  it runs on its **own** 30 s context (`evictionBudget`) so a slow `/view` that ate
  the capture budget cannot leave the tree permanently over-cap; every error is
  logged and swallowed — eviction never alters a run outcome.
- **Observability:** each eviction logs at **INFO** with the generation id and
  bytes reclaimed (silent deletion of the user's own images must be observable);
  a pass that ends still over the cap logs a WARN rather than looping.

No migration was needed — `0012` already carries `generation_images.size_bytes`
and `generations.created_at`. Two store queries were added:
`SumGenerationImageBytes` and `ListOldestGenerations`.
