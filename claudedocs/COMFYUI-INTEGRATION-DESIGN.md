# ComfyUI Workflow Integration — design proposal (v0 draft)

_Status (updated 2026-07-26): **Slice A (Workflow Library) shipped v0.1.28; A2 (scan
+ Library tab + auto-link) v0.1.29; B (local run + UI→API converter) v0.1.30 — all
live-verified against the real local ComfyUI. Slice C1 (remote CivitAI Comfy Cloud —
thin end-to-end) shipped v0.1.41.** All three integration slices are shipped, plus
cloud fast-follows: **affordability gate v0.1.42; UI→API converter for cloud v0.1.43;
converter structural fixes v0.1.46; converter array-type input fix v0.1.51.**
**Remaining custom-node gap:** CivitAI cloud rejects a bare `comfy:nodepack` URN at
submit — it needs a `comfyNodepackSnapshot` step → `nodepacklayer` AIR (post-paid), so
custom-node cloud runs are NOT yet supported. Original design below is unchanged; it
was grounded in two research passes (codebase recon + external API research),
2026-07-24._

## Slice C1 — SHIPPED v0.1.41 (verified contract + caveats)

**Verified against the real `@civitai/client` SDK (v0.2.0-beta.81, prod) AND a live
`whatif` call to `https://orchestration.civitai.com` on 2026-07-25** — no guessed
field names remain:

- **Auth:** `Authorization: Bearer <CivitAI token>` (the manager's existing config
  `Token`, reused). Base URL `https://orchestration.civitai.com`.
- **Submit:** `POST /v2/consumer/workflows?whatif=<bool>` (opt `&wait=<sec>`). Body =
  `WorkflowTemplate`: `{"steps":[{"$type":"customComfy","name":"comfy","input":{"resources":[<AIR URN strings>],"workflow":<raw API-format graph obj>,"trace":"none"}}]}`.
  `$type` is literally `"customComfy"` (camelCase); `trace` ∈ `none|logs|binary`.
- **Poll:** `GET /v2/consumer/workflows/{id}` → `Workflow{ id, status(unassigned→
  preparing→scheduled→processing→succeeded|failed|expired|canceled), cost{base,factors},
  transactions{insufficientBuzz}, steps[].output.blobs[]{id,url,available,type} }`.
- **Cancel:** `PUT /v2/consumer/workflows/{id}` status=`canceled` (best-effort Stop).
- **resources[]** must declare EVERY asset as an AIR URN — models AND custom nodes
  (`urn:air:comfy:nodepack:comfyregistry:<author>/<pack>@<ver>`); the orchestrator
  does NOT inspect the graph. Model URN grammar (from `civitai/src/shared/utils/air.ts`):
  `urn:air:<ecosystem-lower>:<type>:civitai:<modelId>@<versionId>[+<fileId>]`;
  ecosystem from baseModel, type from ModelType (checkpoint/lora/lycoris/vae/embedding/
  controlnet/upscaler/unet/…).

**⚠️ Load-bearing live-verify finding: CustomComfy is PER-SECOND metered.** `whatif`
returns `cost.base=0` — the real charge is `Max(1, runtimeSeconds × buzzPerSecond)`
computed AFTER the run, so there is NO meaningful upfront total. The UI therefore does
NOT show "Estimated cost: N Buzz" (would read as free); it confirms acceptance + states
per-second billing plainly. `transactions.insufficientBuzz` is consequently ~never set
on a customComfy whatif → the affordability gate is effectively inert.

**Implementation:** `internal/comfy/{air,cloud,resolve}.go`; `internal/web/{cloud_handlers,
cloud_job,cloud_pages}.go`; config `comfy_cloud` (opt-in, **default off**); store
`LocalFileByBasename`. Hybrid AIR-URN resolution: auto-derive filename→local_files→
model_cache→URN, flag `guessed`/`unresolved`/`custom-node`, user edits the URN list
before whatif→submit→poll→gallery. Egress+Buzz warning mirrors `match_remote`.
Endpoints loopback-gated + CSRF-before-egress. Reuses the race-safe streaming-job
pattern (local|cloud). Result blobs render as civitai-CDN `<img>` (same trust class
as showcase images). `/audit-pr` no 🔴; a DATA RACE (mutable poll-interval global) +
a stop-test flake were caught by the `-race` gate and fixed.

**Caveats / fast-follows (NOT yet done):**
1. **Real cloud run (spends Buzz) NOT yet live-verified** — only the free `whatif` +
   resolution path is. Held for explicit user OK / a cheap designated workflow.
2. **`minimumDurationSeconds` submit-time affordability gate** — the real runaway-spend
   protection (reject unless the user can afford N sec; + the server-side live-balance
   mid-run cancel). Recommended fast-follow given whatif's cost preview is inert.
3. **Stop mid-submit can still charge Buzz** (best-effort cancel before cloudID is
   recorded) — surfaced in the canceled fragment, not fully preventable client-side.
4. **UI-format workflows** can't cloud-run (no local `/object_info` to convert) — panel
   says "API-format required"; no faked conversion.
5. Custom-node detection is a curated core-node heuristic; the run gate is UI-only
   (orchestrator re-enforces affordability on the real submit).

## 1. The load-bearing findings (what reality forces on the design)

1. **There is NO first-class "golden workflow" slot on CivitAI per model version.**
   CivitAI does not host a per-version workflow `.json`. A recommended workflow is
   recovered from **a sample image's embedded metadata**, or distributed as an
   article/"Workflow"-type attachment, or hand-authored. **Implication: "golden
   workflow per version" is a thing WE curate & store locally** — CivitAI won't hand
   us one.

2. **ComfyUI `/prompt` only accepts API-format JSON** (flat `{node_id:{class_type,
   inputs}}`), NOT the UI "Save" graph (nodes/links/positions). There is **no clean
   pure-Go UI→API conversion** — the real conversion needs a live ComfyUI + its
   `/object_info` schema (the frontend's `graphToPrompt`). **Implication: our contract
   is "we consume/produce API-format."**

3. **The good news for extraction:** a ComfyUI-generated PNG embeds BOTH graphs as
   `tEXt` chunks — key **`prompt`** = API-format (directly submittable!), key
   **`workflow`** = UI-format. Both are plain `json.dumps` in `tEXt` chunks, readable
   in **pure Go** with a small PNG-chunk walker. (A1111 images instead use key
   `parameters` = a flat string — branch on the keyword.) Metadata may be absent
   (`--disable-metadata`), so handle "no metadata."

4. **Local ComfyUI is trivial to talk to:** `http://127.0.0.1:8188`, **no auth**,
   tiny surface (`POST /prompt`, WS `/ws?clientId=`, `GET /history/{id}`, `GET
   /view`). Roll-our-own ~150-line client; no heavy dep. Tolerate an optional bearer
   token (some run `ComfyUI-Login`).

5. **Where the library manager adds unique value:** ComfyUI does NOT auto-download
   models and **fails validation if a referenced checkpoint/LoRA filename isn't on
   disk** under its `models/`. We can **pre-flight a workflow against `/object_info`**
   (whose choices-lists enumerate installed files) and against OUR local library —
   telling the user "this workflow needs X.safetensors, which you have / don't have /
   have under a different name" BEFORE submitting. This is the differentiator.

6. **Remote (CivitAI Comfy Cloud) is real and documented** — the Orchestration API
   (`POST /v2/consumer/workflows`, `whatif` cost estimate, poll-by-token) includes a
   documented **CustomComfy** step that submits a raw comfy graph. Caveats: you must
   **declare every resource** (checkpoints/LoRAs/nodepacks as URNs — no auto-scan),
   it's **Buzz-metered**, and the **exact request-body field names are
   playground-only / unverified**. Higher uncertainty → later slice.

## 2. Data model (store)

Follow the existing convention: **civitai integer ids (`model_id`+`version_id`) as
the linkage, nullable** (exactly how `local_files` links). New migration `0008`:

```
workflows
  id            INTEGER PK autoinc
  name          TEXT              -- user/derived label
  format        TEXT              -- 'api' | 'ui'  (only 'api' is runnable)
  graph         TEXT/BLOB         -- the workflow JSON (as stored)
  source        TEXT              -- 'imported' | 'extracted-png' | 'authored'
  model_id      INTEGER NULL      -- civitai linkage (optional)
  version_id    INTEGER NULL
  base_model    TEXT NULL         -- e.g. 'SDXL 1.0' (for filtering/matching)
  is_golden     INTEGER DEFAULT 0 -- the designated recommended wf for that version
  resources     TEXT NULL         -- extracted list of referenced model filenames
                                   -- (from CheckpointLoader/LoraLoader inputs) for pre-flight
  created_at    TEXT
  updated_at    TEXT
-- partial unique index: at most ONE is_golden=1 per version_id
```

Optionally a `workflow_runs` ledger later (for run history / result-image paths),
but NOT in slice 1 — keep the first slice thin.

## 3. Web UI

New **"Workflows"** nav tab (add `navLink` in `navbar`, register routes in
`Handler()`). Sections:

- **Workflow Library** — list stored workflows as cards: name, attached model/version
  (link), base model, resource summary, format badge (api/ui), **Run** button
  (disabled/greyed for ui-format). Import affordances: **paste API-format JSON** and
  **upload a PNG** (we extract the `prompt` chunk server-side).
- **Model-page hook** — on a version's card, show "Golden workflow: ✓ / Set…" and a
  Run button when one is attached. (Ties into the existing model detail page.)
- **Run view** — reuses the existing **race-safe streaming-job pattern** (the
  scan/discover machinery): submit → a job goroutine drives the ComfyUI WS, snapshots
  progress under the job mutex, client polls a stable container; **Stop** = `POST
  /interrupt`. On completion, render the result images (fetched via `/view`, proxied
  through us or written to a temp/output dir).

## 4. Local-ComfyUI client (`internal/comfy` — new package)

Thin, dependency-light (`net/http` + one WS lib, e.g. `nhooyr.io/websocket`):

- `Submit(ctx, graph, clientID, promptID)` → `POST /prompt`, surface 400
  `node_errors`.
- `Watch(ctx, ws, promptID)` → read text frames; **done = `executing` w/ `node==null`
  && matching `prompt_id`**, or `execution_success`; **fail** on
  `execution_error`/`execution_interrupted` (match `prompt_id`); ignore binary
  preview frames.
- `Results(ctx, promptID)` → `GET /history/{id}` → walk `outputs[].images[]` → `GET
  /view` bytes.
- `ObjectInfo(ctx)` → `GET /object_info` for **pre-flight validation** (installed
  checkpoints/LoRAs/custom-node class list).
- A **separate `*http.Client`** — NOT the civitai SDK's download client (its SSRF
  guard blocks loopback, exactly where ComfyUI lives).

## 5. Config

Add to `internal/config`:
- `comfy_url` (string, default `http://127.0.0.1:8188`)
- `comfy_token` (secret — mirror `Token`'s redaction; only if a login node is used)
- later, remote: `comfy_cloud` toggle + reuse existing `token` (CivitAI Bearer) for
  Orchestration.

## 6. Loopback / egress posture

- Talking to a **local** ComfyUI is an **outbound loopback call from the server** —
  fine, but the endpoint that TRIGGERS a run takes effectively a
  user-controlled/arbitrary target if `comfy_url` were ever user-supplied per-request
  → keep `comfy_url` **config-only**, and **loopback-gate the run/import endpoints**
  (same posture as scan/discover). This dovetails with the open **F2** thread.
- **Remote** submission sends the workflow (and referenced resource ids) to
  civitai.com — make that egress obvious to the user (same principle as the
  `match_remote` opt-out).

## 7. Proposed slicing (each independently shippable as a v0.1.x)

- **Slice A — Workflow Library (no execution).** _[SHIPPED v0.1.28; A2 scan +
  auto-link v0.1.29]_ `0008` migration + `internal/comfy`
  PNG-extraction + Workflows tab: import (paste JSON / upload PNG → extract `prompt`
  chunk), list, view, attach-to-version, set-golden. All pure-Go, fully
  testable/verifiable here. **Lowest risk, immediate value, zero external dep.**
- **Slice B — Local ComfyUI run.** _[SHIPPED v0.1.30; incl. UI→API converter]_ `comfy` client + run streaming-job + result
  gallery + `/object_info` pre-flight. **Requires a reachable local ComfyUI to
  live-verify** (open question below).
- **Slice C — Remote Comfy Cloud.** _[NOT STARTED — the remaining slice]_ Orchestration CustomComfy submit + `whatif` cost
  preview + poll + results. **Highest uncertainty** (body field names, Buzz), do last.

Recommended order: **A → B → C.** A stands alone and de-risks the data model + UI
before we touch execution.

## 8. Open questions → see the session (AskUserQuestion). Key ones:
- First-slice scope (library-only A, vs thin end-to-end local run).
- Do we have a **local ComfyUI to dogfood/live-verify** against, and where?
- Workflow sources to support initially (paste + PNG-extract; skip unreliable CivitAI
  `meta` graph).
- Local-vs-remote priority (recommend local first).
