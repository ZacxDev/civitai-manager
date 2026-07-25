# ComfyUI Workflow Integration — design proposal (v0 draft)

_Status (updated 2026-07-25): **Slice A (Workflow Library) shipped v0.1.28; A2 (scan
+ Library tab + auto-link) v0.1.29; B (local run + UI→API converter) v0.1.30 — all
live-verified against the real local ComfyUI.** Slice **C** (remote CivitAI Comfy
Cloud / Orchestration) is the remaining, **not-started** slice. Original design below
is unchanged; it was grounded in two research passes (codebase recon + external API
research), 2026-07-24._

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
