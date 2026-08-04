# Workflow Discovery — feasibility + design proposal (v0 draft)

_Status: **D1 + D2 SHIPPED; D3 + D4 still open.** **D1** (dedicated Discover-
workflows browse page) shipped **v0.1.44**, with the `type=`→`types=` plural fix in
**v0.1.47**; **D2** (import: download → unzip → store N workflows, deduped by graph
content-hash, migration `0011`) shipped **v0.1.48**. Still open: **D3** (per-model
"related workflows" section) and **D4** (dedup-on-import UI / large-zip-via-queue /
convert-to-runnable nudge). Originally grounded 2026-07-26 by (a) reading the thin SDK
wrapper + vendored `pkg/civitai` SDK, (b) hitting the REAL CivitAI API with the
prebuilt `civitai` CLI (`/home/zach/go/bin/civitai`), and (c) downloading and
unzipping two real "Workflows"-type models. Cross-references the shipped ComfyUI work
in `claudedocs/COMFYUI-INTEGRATION-DESIGN.md` (Slices A/A2/B/C1, v0.1.28–v0.1.41)._

Scope, as decided: **CivitAI "Workflows"-type models only** (they are zips of `.json`).
Two target surfaces: **(1)** a dedicated "Discover workflows" search/browse page, and
**(2)** a per-model-detail-page "workflows related to this model" section. NOT in scope:
image-embedded-metadata discovery, general keyword corpus, article scraping.

---

## 1. Feasibility verdict

| Surface | Verdict | Basis |
|---|---|---|
| **Dedicated Discover-Workflows search/browse page** | **YES — high confidence, low risk** | `/api/v1/models?type=Workflows` returns real, paginated Workflows-type models with the same item shape the existing model search already renders. |
| **Import a discovered Workflows model into the local library** | **YES, with a runnability caveat** | The zip downloads fine and contains `.json` workflows — but they are **UI-format**, not the runnable API-format (see §2). |
| **Per-model "workflows that USE this model/version"** | **PARTIAL — no first-class query exists; only a heuristic approximation** | The models API has **no** "resources reference model X" filter. Best achievable: a Workflows-type search scoped by the model's name (`query`), its `baseModel`, and/or shared tags — an approximation with real false-positive/false-negative rates (see §2.4). |

Bottom line: ship the **dedicated page + import** first (it is essentially a re-skin of
the existing model search wired to a new store-write). Treat the per-model section as an
explicitly-labeled "related workflows (approximate)" feature, not a precise "uses this
model" list.

---

## 2. What reality forces on the design (load-bearing findings)

### 2.1 `type=Workflows` search works, and the result shape is the one we already parse

The thin wrapper's `Reader.SearchModels(ctx, url.Values)` passes arbitrary query params
straight through (`internal/civitai/civitai.go:82` → SDK `pkg/civitai/models.go:64`,
`GET /api/v1/models`). The typed `ModelListItem` is deliberately minimal — `id, name,
type, nsfw, creator, stats` only (`pkg/civitai/models.go:23`) — but the **full item
(versions, files, images, tags, baseModel) is preserved in `ModelSearchResult.Raw`**
(`models.go:60`), which is exactly what the web layer already reaches into for search
cards (`internal/web/model_pages.go:164 parseSearchImages`,
`:421 newestVersionInfoByModel`).

Empirically, `civitai models search --type Workflows --limit 8 --json` returns items like:

```
id=1818841  "WAN 2.2 Workflow T2V-I2V-T2I (Kijai Wrapper)"  type=Workflows
  tags=["tool"]  baseModel=["Wan Video 2.2 T2V-A14B"]  creator="pgc"  images=17
  files=[{name:"wan22WorkflowT2VI2VT2I_v185.zip", type:"Archive",
          sizeKB:66024, format:"Other"}]
id=618578   "FLUX.1-DEV & Kontext Workflows Megapack"       type=Workflows
  tags=["comfyui","flux.1","kontext",...]  baseModel=["Flux.1 D","Flux.1 Kontext"]
  files=[{name:"flux1DEVKontext_F1Img2img13.zip", type:"Archive", sizeKB:6.5, ...}]
```

Every Workflows result carries the same fields a Checkpoint/LoRA result does: numeric
`id`, `name`, `nsfw`, `creator.username`, `tags[]`, and `modelVersions[]` each with
`baseModel`, `files[]`, and a rich `images[]` showcase (images AND videos). **So the
existing search card grid, image carousel, NSFW handling, and "Updated X ago" popover
all work unchanged** on Workflows results.

The distinguishing marks of a Workflows model's files: `type:"Archive"`,
`format:"Other"`, name ends `.zip`. `download_url` requires auth (Bearer token) — same
as any gated file.

### 2.2 Import = download a zip, unzip to MANY `.json`, each is UI-format

Downloaded two real models (`civitai download --model <id> --yes`) and unzipped:

```
618578  flux1DEVKontext_F1Img2img13.zip →
  flux_img2img_1_2.json           (UI-format, 17 nodes)
  flux_img2img_1_3_HighresFix.json (UI-format, 27 nodes)
1309369 WAN21IMGToVIDEO_v40.zip →
  WAN2.1_Img2video_manual.json    (UI-format)
  WAN2.1_Img2Video_auto.json      (UI-format)
```

Two structural facts the import flow MUST handle:

1. **One Workflows model → one (or more) zip files → each zip contains N workflow
   `.json`s.** A single "import" produces **multiple** `store.Workflow` rows, not one.
   (The Flux Megapack version above yields 2; a version can also ship several zips —
   `modelVersions[].files[]` had 3 distinct zips on model 339604.)

2. **The contained `.json`s are UI-format** (editor "Save" graph: top-level `nodes[]`
   array), which `comfy.DetectFormat` classifies as `FormatUI` (`internal/comfy/format.go:39`).
   **UI-format is NOT runnable** — `store.Workflow.Runnable()` is api-only
   (`internal/store/types.go:204`), ComfyUI `/prompt` rejects UI graphs, and Comfy Cloud
   can't run them either (COMFYUI design doc caveat #4, line 57). Both CivitAI downloads
   in my sample were UI-format; this appears to be the norm for CivitAI Workflows models
   (creators export the editor graph, not the API prompt). This is the single most
   load-bearing finding for the "discover → run" story.

The app already owns a UI→API converter — `comfy.ConvertUIToAPI(uiGraph, info ObjectInfo)`
(`internal/comfy/convert.go:91`) — but it **requires a live local ComfyUI's `/object_info`
schema** to resolve node classes/widgets. So a discovered UI-format workflow becomes
runnable **only** after the user runs it through the existing local-ComfyUI convert path;
cloud-run stays blocked (no local `/object_info` in the cloud path).

### 2.3 The download queue writes bytes; it does NOT extract

`store.Enqueue` enforces one active download per `(version_id, file_id)` via
`ux_dlq_active` (`internal/store/queue.go:62`), and the worker's `stream()` simply writes
the response body to `DestPath` while hashing (`internal/queue/queue.go:287`) — **there
is no post-download extraction/transform hook.** Reusing the queue for a Workflows zip
would therefore deposit a `.zip` in the model tree and stop; extraction + `InsertWorkflow`
would still have to happen elsewhere. This shapes the import-flow decision in §3.3.

### 2.4 Per-model discovery: no first-class reverse lookup; approximation only

There is **no** models-API parameter for "workflows whose graph references model/version
X." The API's only reverse-by-resource surface is the **images** endpoint
(`/api/v1/images?modelVersionId=…`, already used by the community feed at
`internal/web/handlers.go:437`) — that returns images, not workflows, and does not carry
the workflow graph.

What DOES work as an approximation (all verified against the live API):

```
# by the model's name as a free-text query, scoped to Workflows:
civitai models search --type Workflows --query pony --limit 5
  → 876576 "Pony ControlNet (multi) Union"
    286551 "PonyDiffusionXL's ComfyUI Beginner Workflow"
    615860 "PonyXL | ComfyUI Hires Fix | Workflow"  ...

# by shared base model:
civitai models search --type Workflows --base-model Pony --limit 5
  → 339604, 434391, 735795, 1367872, 965939  (all baseModel "Pony")

# by tag: --tag <name> also filters.
```

Accuracy caveats to state plainly in the UI:
- **`query=<model name>` matches the workflow's OWN name/description text**, not its
  actual resource references — it finds workflows that *mention* the model, missing ones
  that use it without naming it, and catching unrelated ones that happen to share words.
- **`baseModel` is broad** — "all Pony workflows" is a huge, loosely-related set, not
  "workflows using THIS Pony checkpoint."
- Neither inspects the zip's graph, so **precision is inherently limited.** Label the
  section "Related workflows" (not "Workflows that use this model") and show the match
  basis.

A precise version would require us to download+unzip+parse each candidate's graph and
match filenames — far too expensive at browse time; not proposed.

---

## 3. Proposed integration

Design principle: **maximize reuse of the existing model-search + workflow-library
machinery; add the minimum new surface.** Almost everything the dedicated page needs
already exists.

### 3.1 Surface (1): dedicated "Discover workflows" page

A near-clone of the existing search flow (`internal/web/handlers.go:145 handleSearch`),
differing only by pinning `type=Workflows` and routing card actions to import instead of
subscribe/download.

- **Route:** `GET /workflows/discover` (+ an HX-partial results endpoint mirroring the
  HX branch of `handleSearch`). Add a nav entry next to the existing Workflows/Library
  tabs.
- **Query build:** reuse `handleSearch`'s param assembly verbatim — `query`, `limit`
  (`searchLimit`, `internal/web/handlers.go:18`), `sort`/`period`
  (`searchSortOptions`/`searchPeriodOptions`, `:104`), and `setNSFWParam` (`:31`) — then
  `q.Set("type","Workflows")`. This inherits the NSFW egress posture, the sort/period
  dropdowns, and pagination (`--cursor`/`--page`) for free.
- **Cards:** reuse the existing search-card renderer + `parseSearchImages`
  (`internal/web/model_pages.go:164`) + `galleryTileW`/carousel (`:988`) + the
  "Updated X ago" popover (`newestVersionInfoByModel`, `:421`). Workflows results carry
  images/videos, so cards look identical to model cards. Swap the card's primary action
  from Subscribe/Download to **"Import workflow(s)"** (§3.3).
- **Empty-query feed:** an optional cached "popular Workflows this month" mirroring
  `popularModels` (`:267`) — same TTL cache pattern, keyed additionally by type.
- **Reuse, not fork:** prefer parameterizing the existing search handler/renderer with a
  `type` + an "action mode" (subscribe vs import) over copy-pasting, to avoid a second
  drifting search implementation.

### 3.2 Surface (2): per-model "Related workflows" section on the model detail page

Slot a lazy, cache-first fragment into the existing version region
(`internal/web/model_pages.go:584 versionRegionInner`), mirroring the community-feed
container (`:625 communityFeedContainer` → `handleModelCommunity`, `handlers.go:437`):

- **Route:** `GET /models/{id}/related-workflows?versionId=…`, lazy-loaded on `revealed`,
  fail-open, cache-first (reuse the community-cache TTL pattern, `communityCacheTTL`).
- **Query:** `type=Workflows` + `query=<model name>` (from the already-loaded
  `ModelDetail.Name`) and/or `baseModel=<selected version's baseModel>`. One bounded
  `SearchModels` call, same egress posture as the community feed.
- **Presentation:** the same card grid as §3.1, under a header that names the match basis
  ("Related workflows — matched by name/base model, approximate"). Each card links to the
  dedicated discover flow / import.
- **Honesty:** because it is an approximation (§2.4), keep it clearly secondary to the
  showcase/community sections and never phrase it as authoritative.

### 3.3 Import flow: download → unzip → store N workflows

The natural model is the **existing paste/PNG import** (`internal/web/workflow_handlers.go:65
handleWorkflowImport`, `:117 handleWorkflowImportPNG`): detect format, extract resources,
`InsertWorkflow`, redirect with a flash. Discovery import is the same terminal step, with
a fetch+unzip prefix.

Proposed `POST /workflows/discover/{modelId}/import` (CSRF-protected; **loopback-gated** —
it triggers egress + a download, matching the scan/discover posture and the workflow-import
gate at `workflow_handlers.go:76`):

1. Resolve the model/version (cache-first `cachedModelDetail`) and pick the Archive file
   (`civitai.SelectFile(files, "Archive")`, `internal/civitai/civitai.go:111` — already
   supports a file-type preference).
2. Fetch the zip. **Recommendation: a direct bounded fetch, NOT the download queue.**
   Rationale: (a) the queue has no extraction hook and would leave a raw `.zip` in the
   model tree (§2.3); (b) these zips are small (KB to tens of MB) so streaming-to-temp
   ceremony is overkill; (c) import wants the bytes in-process to unzip immediately.
   Use a bounded reader (mirror `maxWorkflowUpload`, `workflow_handlers.go:22`) and the
   SDK `Downloader` (which resolves civitai auth) for the fetch.
   - *Tradeoff:* the queue gives retry/resume/dedup/progress for free. For a large
     Workflows zip (e.g. the 66 MB WAN model) those matter — so a **v2** could route the
     zip through the queue to `DestPath` and add a post-`done` extract step. Start with
     the direct fetch; revisit if large zips prove common.
3. Unzip in-memory (`archive/zip`), iterate every `.json` entry (bound entry count +
   per-entry size to defend against a zip bomb). For each: `comfy.DetectFormat`
   (`internal/comfy/format.go:39`), `comfy.ExtractResourcesAny` for the resource list
   (`:118`), then `store.InsertWorkflow` (`internal/store/workflows.go:86`) with:
   - `Source`: a new `WorkflowSourceCivitai` constant (add to `internal/store/types.go:197`)
     to distinguish discovered workflows from imported/PNG/scanned.
   - `ModelID`/`VersionID`: **pre-linked** to the source Workflows model/version (this is
     one linkage the discovery path CAN populate deterministically, unlike scan's
     filename-guess `FindVersionByFileName`).
   - `Name`: derived from the entry filename (`defaultWorkflowName`, `workflow_handlers.go:376`).
   - `Format`: almost always `ui` (§2.2).
4. Redirect to the Workflows library tab with a flash "Imported N workflow(s)" (reuse
   `redirectWorkflows`, `workflow_handlers.go:336`).

### 3.4 How discovered workflows connect to run/cloud (with caveats)

Once stored, a discovered workflow is an ordinary `store.Workflow` row, so it inherits the
shipped library/detail/run/cloud UI (`internal/web/workflow_pages.go`, `run_*`, `cloud_*`).
But state the caveats prominently:

- **UI-format (the common case) is NOT runnable as-is.** The detail page already greys the
  Run button for ui-format (COMFYUI doc §3). To run, the user must convert via the local
  ComfyUI path (`comfy.ConvertUIToAPI`, needs `/object_info`); **cloud-run stays blocked**
  for ui-format (COMFYUI caveat #4).
- **Cloud needs AIR URNs for every resource + custom node** (COMFYUI Slice C1, lines
  25–30) — a known gap; a discovered workflow's resource list is filenames, and its
  custom-node packs are unknown until inspected. So "discover → cloud-run" is the least
  reliable path and should not be advertised as turnkey.
- **The clean, honest win is "discover → import → local convert → run/preflight."** The
  preflight differentiator (COMFYUI finding #5) applies: after import we can tell the user
  which referenced models they already have locally.

---

## 4. Slicing (thin-first, independently shippable v0.1.x)

- **Slice D1 — Discover page, browse-only (MVP, recommended first).** _[SHIPPED
  v0.1.44; `type=`→`types=` plural fix v0.1.47]_ Dedicated
  `/workflows/discover` page = existing search wired to `type=Workflows`; cards render via
  the existing renderer + `parseSearchImages`; sort/period/NSFW/pagination reused; each
  card links out to the CivitAI model page. **No import, no store writes.** Pure reuse,
  fully verifiable here at the HTTP level (curl the results fragment). Lowest risk.
- **Slice D2 — Import (download → unzip → store N workflows).** _[SHIPPED v0.1.48;
  graph-hash dedup + migration `0011`]_ Add
  `WorkflowSourceCivitai`, the loopback-gated `POST /workflows/discover/{id}/import`,
  direct bounded fetch + in-memory unzip + per-`.json` `InsertWorkflow` pre-linked to the
  source model/version, zip-bomb guards. Reuses `comfy.DetectFormat`/`ExtractResourcesAny`
  and `InsertWorkflow`. Verifiable with a temp DB + a small real zip (dedup guard in §5).
- **Slice D3 — Per-model "Related workflows" section.** _[OPEN]_ Lazy fragment on the model detail
  page (mirrors the community feed), `type=Workflows` scoped by name/baseModel,
  cache-first + fail-open, clearly labeled approximate. Independent of D2 (can link to
  D1/D2).
- **Slice D4 (optional) — quality-of-life.** _[OPEN]_ Dedup-on-import UI (skip/replace already-
  imported), "Related workflows" tag-based refinement, large-zip-via-queue path, and a
  post-import "convert to runnable" nudge that hands off to the local ComfyUI convert flow.

Recommended order: **D1 → D2 → D3 → D4.** D1 stands alone and de-risks the UI; D2 adds the
real value (getting workflows into the library); D3 is the honest-approximation nicety.

---

## 5. Open questions / risks / caveats

1. **Runnability (biggest).** Discovered workflows are UI-format (§2.2), which needs local-
   ComfyUI conversion to run and cannot cloud-run. The feature's promise must be "discover
   + import into your library," with running as a downstream, caveated step — not "discover
   and run in the cloud."
2. **Per-model precision (§2.4).** No reverse "uses model X" query exists; name/baseModel/tag
   is an approximation with real false positives/negatives. Label it as such; don't
   overpromise.
3. **Zip handling.** One model → multiple zips → each with multiple `.json`. Must fan out to
   N rows, and defend against zip bombs (bound entry count, per-entry and total size). Some
   entries may be non-workflow JSON or non-JSON files — skip gracefully (mirror the scan's
   "skipped" counting).
4. **Dedup vs already-imported.** Re-importing the same model would duplicate rows.
   `InsertWorkflow` (`workflows.go:86`) is a plain insert with no dedup key; only the SCANNED
   path has `UpsertWorkflowByPath` keyed on `source_path` (`:117`). Discovered workflows have
   no `source_path`. Decide a dedup key (e.g. content hash of the graph, or
   `(model_id, version_id, name)`) before D2 ships, or accept duplicates in the MVP and dedup
   in D4.
5. **Egress obviousness.** Both surfaces send data to civitai.com: the search sends the query
   (§3.1), import downloads a gated file with the user's token. Mirror the `match_remote` /
   existing-search egress transparency (repo `CLAUDE.md` invariant); make the download action
   explicit.
6. **Rate limits / pagination.** The models API caps `page*limit` at 1000 and 429s beyond it;
   prefer `--cursor`-style deep paging (per CLI help). The discover feed should paginate by
   cursor, and the per-model fragment should stay cache-first (reuse the community-cache
   pattern) to avoid hammering the API on every model view.
7. **Auth for download.** Workflows zip `download_url` requires a Bearer token; import fails
   without a configured CivitAI token — surface a clear "configure your token" error rather
   than a silent 401.
8. **Trust / safety.** The CLI itself warns these zips are "pickle/archive-format … can
   execute code when loaded." We only unzip + parse JSON (no code execution), and workflow
   graph JSON is already stored untrusted and pretty-printed with escaping
   (`prettyJSON`/`sanitize`), so the risk is low — but bound the unzip and never execute
   archive contents.

---

### Appendix — reuse anchors (file:line)

- **Search:** `internal/web/handlers.go:145 handleSearch`, `:234 handleSubscribeSearch`,
  `:267 popularModels`, `:18 searchLimit`, `:31 setNSFWParam`, `:104 searchSortOptions`.
- **SDK search:** `internal/civitai/civitai.go:82 SearchModels` (arbitrary `url.Values`),
  `:111 SelectFile` (file-type pref); SDK `pkg/civitai/models.go:23 ModelListItem`,
  `:60 Raw`, `:64 SearchModels`.
- **Card/image parse + render:** `internal/web/model_pages.go:164 parseSearchImages`,
  `:421 newestVersionInfoByModel`, `:988 galleryTileW`, `:625 communityFeedContainer`;
  community feed handler `internal/web/handlers.go:437 handleModelCommunity`.
- **Workflow store:** `internal/store/workflows.go:86 InsertWorkflow`,
  `:117 UpsertWorkflowByPath`, `:317 AttachWorkflow`, `:340 SetGolden`;
  `internal/store/types.go:154 Workflow`, `:197 WorkflowSource*`, `:204 Runnable`.
- **Import + comfy:** `internal/web/workflow_handlers.go:65 handleWorkflowImport`,
  `:117 handleWorkflowImportPNG`, `:76 loopback gate`, `:22 maxWorkflowUpload`,
  `:336 redirectWorkflows`, `:376 defaultWorkflowName`;
  `internal/comfy/format.go:39 DetectFormat`, `:69 ExtractResources`, `:118 ExtractResourcesAny`;
  `internal/comfy/convert.go:91 ConvertUIToAPI`.
- **Download queue:** `internal/store/queue.go:62 Enqueue` (`ux_dlq_active` single-active);
  worker `internal/queue/queue.go:287 stream` (writes bytes to `DestPath`, no extract hook).
- **Routes / streaming-job pattern:** `internal/web/server.go:326 /search`, `:350–388`
  library + workflow routes; `internal/web/workflow_scan_handlers.go` (race-safe streaming
  job: snapshot-under-mutex, stable poll container).
