# Run presets & batch queue — design

Extend the workflow **run** surface so a user keeps several saved run-parameter
lines per workflow (**tabs**), duplicates one (**Fork**), and queues N runs of any
line in one click (**Queue ×N**), each with a fresh seed.

Related: `OUTPUT-GALLERY-DESIGN.md` (the `generations` capture this builds on),
`COMFYUI-INTEGRATION-DESIGN.md`, `CUSTOM-NODE-RESOLUTION-DESIGN.md` (house style).

**Status: design. Not yet implemented. No production code was written for this doc.**

Every claim about current behaviour below is cited as `file:line` against
`main` @ `d9ac7c6`. Where a claim could not be verified it says so.

---

## Decisions already made — these are constraints, not options

1. **Concurrency: the existing singleton stays.** ONE batch job owns N prompts and
   runs them **sequentially** (submit 1 → wait → submit 2 → …). Progress reads
   "3 of 8". Stop cancels the whole remaining batch. The singleton is **not**
   lifted and N prompts are **never** fired into ComfyUI's queue at once.
2. **Each queued item gets a NEW RANDOM SEED**; everything else identical.
3. **Persistence: a new table**, migration **0014**. Unrun drafts survive; tabs
   restore when the page is reopened.
4. **A tab holds the FULL run-parameter set**, not just prompt text — every
   editable field the run panel exposes. These are *run presets*.

---

## Ground truth from the code (verified 2026-07-29 against `d9ac7c6`)

### 🔴 The brief's seed-detection hook is wrong — this would have shipped a no-op

The task brief states "`isSeedControlSlot` already detects seed slots — reuse it
for decision 2." **It does not.** `isSeedControlSlot`
(`internal/comfy/run_inputs.go:527`) reports whether a `widgets_values` slot holds
a `control_after_generate` **string** — `"fixed"` / `"increment"` / `"decrement"` /
`"randomize"` (`run_inputs.go:136-138`). Its only two call sites use it for
**cursor alignment**, to skip that extra slot so later widget indices stay correct
(`run_inputs.go:279`, `run_inputs.go:446`). It never touches the seed *value*.

Randomizing "every slot `isSeedControlSlot` matches" would write a number into a
control string. `typedOverrideValue` preserves the slot's JSON type and refuses a
non-matching replacement (`internal/comfy/widget_overrides.go:45-47, 82-85`), so
it would **silently do nothing** — a green-tests, dead-in-production bug of
exactly the class this repo keeps getting bitten by.

> **The correct hook is `RunInput.Kind == comfy.RunInputSeed`**
> (`run_inputs.go:18`), which comes from the curated layout's `isSeed`/`kind`
> pair (`run_inputs.go:103`, `:112`). A test must pin that the randomizer selects
> by `Kind`, and must fail if it selects by `isSeedControlSlot`.

### What `DetectRunInputs` actually returns — live-measured

Measured by running `DetectRunInputs` over the committed real-graph fixture
`internal/comfy/testdata/wf587_converted_widgets.json` with and without the
live-captured `testdata/object_info_subset_wf587.json` (throwaway test, removed):

```
11 inputs, with and without object_info:
  node=40 widget=0 seed   KSampler.seed          resolved=true   "Seed"
  node=17 widget=0 int    KSampler.steps         resolved=true   "Steps"
  node=18 widget=0 float  KSampler.cfg           resolved=true   "CFG"
  node=41 widget=4 select KSampler.sampler_name  resolved=false  "Sampler"    choices=44 (0 w/o info)
  node=41 widget=5 select KSampler.scheduler     resolved=false  "Scheduler"  choices=9  (0 w/o info)
  node=35 widget=0 float  KSampler.denoise       resolved=true   "Denoise"
  node=4  widget=0 text   CLIPTextEncode.text    resolved=true   "Prompt (NEGATIVE)"
  node=3  widget=0 text   CLIPTextEncode.text    resolved=true   "Prompt (POSITIVE)"
  node=1  widget=0 int    EmptyLatentImage.width  resolved=true  "Width"
  node=11 widget=0 int    EmptyLatentImage.height resolved=true  "Height"
  node=23 widget=0 int    EmptyLatentImage.batch_size resolved=true "Batch size"
  seed inputs: 1
```

Three things this fixes about the naive design:

- **9 of 11 are `Resolved=true`** — the value lives on an *upstream* node, not on
  the node the label names. The preset key must therefore be the **holder's**
  `(NodeID, WidgetIndex)` (`comfy.UIWidgetKey`), exactly what the Parameters panel
  already emits (`internal/web/run_params.go:192-193`). Nothing new.
- **Exactly ONE seed input**, and it is `Resolved` (node 40 is an upstream
  rgthree Seed node, not the KSampler). Seed randomization must target the
  *holder*, which `Kind == RunInputSeed` already gives correctly.
- The `Sampler`/`Scheduler` slot indices are **4 and 5**, not 3 and 4 — the
  `control_after_generate` slot after `seed` consumed index 1. Widget indices are
  layout-dependent, which is the whole reason drift is dangerous.

### The existing run singleton

- `Server.runJob` / `Server.runMu` / `Server.runSeq`; `startRun` refuses when
  `s.runJob != nil && s.runJob.running` (`internal/web/run_handlers.go:197-199`)
  and **returns nothing** — a re-click while a run is in flight is a *silent*
  no-op that re-renders the current status fragment (`run_handlers.go:593-594`).
- `runSeq` increments once per `startRun` (`run_handlers.go:206`) and is stamped
  as `data-run-seq` on the status fragment root (`internal/web/run_pages.go:167-177`)
  — documented as "the single DOM hook the ux-audit harness keys on".
- `applyRunOutcomeLocked` sets `job.running = false` (`run_handlers.go:297`) and is
  called from `settleAndCapture` under `runMu` (`run_handlers.go:255-258`).
- Capture runs **outside** `runMu` and captures are explicitly **not** serialized
  with each other (`internal/web/outputs_capture.go:344-346`).
- `stopRun` sets `stopped` + cancels + best-effort `Interrupt`
  (`run_handlers.go:503-521`). `Interrupt(ctx)` takes **no prompt id**
  (`run_handlers.go:37`) — it interrupts whatever ComfyUI is currently executing.
- `runJobBudget = 30 * time.Minute` is a runaway backstop, not the normal path
  (`run_handlers.go:17-22`).

### The existing param / mode plumbing

- `runOptions` already carries all four override families:
  `Substitute`, `OptionFixes`, `WidgetOverrides` (legacy, api-keyed),
  `UIWidgetOverrides` (UI-keyed, positional), `ModeSelection`
  (`run_handlers.go:141-162`).
- Submit-time safety is a **lenient parse backed by a structural guarantee
  downstream**: `parseWidgetOverridesForModes` keeps only keys `DetectRunInputs`
  surfaces for *the mode-applied graph* (`internal/web/run_params.go:74-128`), and
  `parseModeChoices` keeps only mode keys `DetectModeSelectors` surfaces
  (`internal/web/run_modes.go:45-75`). `ApplyModeSelection` re-derives the
  accepted set again independently (`run_modes.go:42-44`).
- `#run-params` is a **stable container**; the mode `<select>` swaps its innerHTML
  on change (`run_modes.go:28-32`, `:159-166`). `#run-status` is a **separate**
  stable container the run poller swaps (`run_pages.go:19`, `:61-63`). They are
  siblings, never nested.
- `runModesInclude = "#run-modes select"` — every run control hx-includes it
  (`run_modes.go:26`).

### The existing capture / re-run

- `generations.params` stores `runParamsSnapshot`
  (`outputs_capture.go:26-37`) — `substitute`, `option_fixes`,
  `widget_overrides`, `ui_widget_overrides`, `resources`, `base_model`, `format`.
  **`mode_selection` is NOT in it.** That is the documented deferred gap
  (`claudedocs/SESSION-HANDOFF.md:71`).
- `runOptionsFromParams` refuses to replay positional overrides unless the
  generation's `graph_hash` still equals the workflow's, and treats a blank hash on
  either side as a mismatch (`outputs_capture.go:186-226`). `handleGenerationRerun`
  turns that into an HTTP **409** (`internal/web/outputs_handlers.go:233-237`).
- `generations` is `ON DELETE SET NULL` on `workflow_id`, with `workflow_name` /
  `base_model` / `graph_hash` **snapshotted** so an orphan stays labeled
  (`internal/store/migrations/0012_output_generations.sql`).
- `enforceOutputsCap` evicts the **oldest** generations to stay under
  `OutputsMaxBytes`, protecting `id >= keepID` (`outputs_capture.go:357-441`).
- `DeleteWorkflow` is a plain row delete (`internal/store/workflows.go:359-369`);
  generations survive as orphans by design.
- `PRAGMA foreign_keys = ON` is set (`internal/store/store.go:57`), so
  `ON DELETE CASCADE` / `SET NULL` genuinely fire.

### Migration numbering

`internal/store/migrations/` ends at **`0013_nodepack_cache.sql`**. **0014 is the
next free number — verified.** Migrations are `go:embed`'d and applied in filename
order (repo `CLAUDE.md`), so 0014 is append-only.

---

## Data model — migration `0014_run_presets.sql`

One migration file, two concerns: the preset table, and batch identity on
`generations`.

```sql
-- 0014_run_presets.sql
--
-- Saved per-workflow RUN PRESETS (the run panel's tabs) + batch identity on
-- generations.
--
-- A preset is a DRAFT the user edits, not a historical record. It stores the full
-- run-parameter set for ONE workflow — the same JSON shape generations.params
-- already uses (runParamsSnapshot), extended with mode_selection and with
-- per-entry drift metadata (see the reconciliation rule in
-- claudedocs/RUN-PRESETS-AND-BATCH-DESIGN.md).
--
--   workflow_id — owning workflow. ON DELETE CASCADE, DELIBERATELY UNLIKE
--                 generations' ON DELETE SET NULL. A generation is a durable
--                 artifact (images on disk) that stays viewable forever behind
--                 snapshot labels; a preset is a POINTER INTO A SPECIFIC GRAPH
--                 (positional widget keys + mode keys) that means nothing without
--                 that graph. An orphaned preset could never be reconciled and
--                 never be run — it could only render as an error.
--   name        — user-authored tab label. UNTRUSTED: escaped at render time.
--                 Blank renders as "Preset N".
--   position    — tab order (0-based, dense but not enforced; ties break by id).
--   graph_hash  — SNAPSHOT of workflows.graph_hash AT SAVE TIME. This is THE
--                 reconciliation key: equal ⇒ the stored positional keys still
--                 mean what they meant. Blank is treated as "cannot prove equal",
--                 exactly like runOptionsFromParams (outputs_capture.go:207).
--                 It is re-stamped ONLY on an explicit save, never silently on a
--                 successful read — silent re-stamping would erase the evidence
--                 of drift for the next open.
--   params      — JSON runPresetSnapshot (superset of runParamsSnapshot). NOT
--                 NULL, defaults to '{}' so a corrupt/absent blob degrades to
--                 "no stored values" rather than an error, mirroring
--                 parseRunParams (outputs_capture.go:158-165).
CREATE TABLE run_presets (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  workflow_id INTEGER NOT NULL,
  name        TEXT    NOT NULL DEFAULT '',
  position    INTEGER NOT NULL DEFAULT 0,
  graph_hash  TEXT    NOT NULL DEFAULT '',
  params      TEXT    NOT NULL DEFAULT '{}',
  created_at  TEXT    NOT NULL,             -- RFC3339 UTC
  updated_at  TEXT    NOT NULL,
  FOREIGN KEY (workflow_id) REFERENCES workflows(id) ON DELETE CASCADE
);

CREATE INDEX ix_run_presets_workflow ON run_presets(workflow_id, position, id);

-- Batch identity on generations. N sequential runs produce N rows; without a
-- marker the outputs rail becomes undifferentiated noise (12 near-identical
-- thumbnails from one click). These columns are ALL NULL for an ordinary single
-- run, so every existing row and every existing query is unaffected.
--
--   batch_id    — opaque per-batch id (comfy.NewID()), NULL for a single run.
--                 A TEXT id and NOT a run_batches table: the batch job is an
--                 in-memory singleton that dies with the process, so a durable
--                 batch row could be left permanently "running" after a crash —
--                 a row that lies. The gallery needs identity + ordering only.
--   batch_index — 1-based position within the batch.
--   batch_total — N as requested at batch start (so "3 of 8" survives a halt).
--   preset_id   — the preset this run came from. ON DELETE SET NULL, mirroring
--                 workflow_id: deleting a preset must never delete images.
--   preset_name — SNAPSHOT of the preset label at run time, exactly the
--                 workflow_name idiom, so a deleted preset's batch stays labeled.
--
-- SQLite gotcha, VERIFY BEFORE RELYING ON IT: ALTER TABLE ADD COLUMN with a
-- REFERENCES clause is permitted only when the column's default is NULL. It is
-- here (no DEFAULT given). The FK is not applied retroactively to existing rows,
-- which is fine — they are all NULL.
ALTER TABLE generations ADD COLUMN batch_id    TEXT;
ALTER TABLE generations ADD COLUMN batch_index INTEGER;
ALTER TABLE generations ADD COLUMN batch_total INTEGER;
ALTER TABLE generations ADD COLUMN preset_id   INTEGER REFERENCES run_presets(id) ON DELETE SET NULL;
ALTER TABLE generations ADD COLUMN preset_name TEXT;

CREATE INDEX ix_generations_batch ON generations(batch_id, batch_index);
```

### The stored JSON — `runPresetSnapshot`

**Decision: one JSON shape, two tables.** `run_presets.params` and
`generations.params` use the *same* struct, so `buildRunParamsSnapshot` /
`parseRunParams` / `runOptionsFromParams` (`outputs_capture.go:89-226`) are
extended once and both surfaces move together. Two shapes would drift.

Extensions to the existing `runParamsSnapshot`:

```go
type runParamsSnapshot struct {
    Substitute        map[string]string       `json:"substitute,omitempty"`
    OptionFixes       []optionFixEntry        `json:"option_fixes,omitempty"`
    WidgetOverrides   []widgetOverrideEntry   `json:"widget_overrides,omitempty"`   // legacy, api-keyed
    UIWidgetOverrides []uiWidgetOverrideEntry `json:"ui_widget_overrides,omitempty"`
    Resources         []string                `json:"resources,omitempty"`
    BaseModel         string                  `json:"base_model,omitempty"`
    Format            string                  `json:"format,omitempty"`

    // NEW — closes the documented deferred gap (SESSION-HANDOFF.md:71): a
    // captured generation could not restore its mode selection because
    // generations.params never held one. Keyed ModeSelector.Key → ModeGroup.Key,
    // exactly runOptions.ModeSelection (run_handlers.go:161).
    ModeSelection map[string]string `json:"mode_selection,omitempty"`
}

type uiWidgetOverrideEntry struct {
    NodeID string          `json:"node_id"`
    Widget json.RawMessage `json:"widget"` // RawMessage — see outputs_capture.go:51-64
    Value  string          `json:"value"`

    // NEW — drift metadata. Snapshotted from the RunInput at save time so a
    // drifted graph can be reconciled STRUCTURALLY, not only by hash equality.
    // All omitempty: an entry written before this field existed simply has no
    // tuple, which the reconciler treats as "unverifiable" (see below).
    Kind      string `json:"kind,omitempty"`       // comfy.RunInputKind
    ClassType string `json:"class_type,omitempty"` // RunInput.SourceClassType (the HOLDER's class)
    InputName string `json:"input_name,omitempty"` // RunInput.InputName (the CONSUMER's semantics)
    Label     string `json:"label,omitempty"`      // display only; never part of the match key
}
```

`Label` is **display-only and explicitly not part of the match key** — it is
untrusted author text (`run_inputs.go:307-309` derives it from the source node's
title) and can change harmlessly.

---

## 🔴 The reconciliation rule (problem 1)

A preset stores positional keys. The graph can change under it: a re-scan replaces
the row's graph in place (`store.UpsertWorkflowByPath`, `workflows.go:150-176`), a
re-import lands a different `graph_hash`, a different mode selection exposes a
different input set entirely. **Silently applying a stale set to a changed graph is
the failure mode to prevent.**

Reconciliation runs at **render time**, for the **active tab only**, against the
**mode-applied current graph** — the same graph the run would actually convert.

### Step 0 — establish the graph the tab is about

```
modes    := parseModeChoices(preset.ModeSelection, wf)      // drops unknown selector/mode keys
dropped  := preset.ModeSelection keys NOT surfaced by DetectModeSelectors(wf.Graph)
graph    := comfy.ApplyModeSelection(wf.Graph, modes)       // no-op for an ordinary workflow
live     := comfy.DetectRunInputs(graph, nil)               // the allow-list
```

A dropped mode key is **named to the user**. It cannot be silently ignored: with
the mode not applied, a multi-mode template converts to an all-bypassed graph and
the run aborts with `ConversionEmptyError` ("nothing to run",
`run_handlers.go:306-310`) — a confusing error for what is really "your saved mode
no longer exists".

### Step 1 — classify by hash

| | condition | rule |
|---|---|---|
| **EXACT** | `preset.graph_hash == wf.graph_hash`, both non-blank | The hash **is** the proof that every position still means what it meant. Apply all stored values. No per-entry check, no banner. |
| **UNVERIFIABLE** | either hash blank | Treated as DRIFTED. Blank cannot be proven equal — the same call `runOptionsFromParams` already makes (`outputs_capture.go:207`). `graph_hash` is nullable for pre-0011 rows (`0011_workflow_graph_hash.sql`), so this is a real, reachable case. |
| **DRIFTED** | hashes differ | Per-entry tuple match, below. |

### Step 2 — per-entry rule (DRIFTED only)

For each stored `ui_widget_overrides` entry, look up
`UIWidgetKey{NodeID, Widget}` in `live`:

| outcome | condition | what happens | what the user is told |
|---|---|---|---|
| **KEPT** | key present **and** `kind`, `class_type`, `input_name` all equal the live `RunInput`'s | value pre-filled into the field | nothing (it matched) |
| **DROPPED — retargeted** | key present but any of the three differs | value discarded; field pre-filled from the graph's current value (`RunInput.Current`) | named, with both sides: *"Seed → now Prompt (POSITIVE); your saved value was not applied."* |
| **DROPPED — gone** | key absent from `live` | value discarded | named by its stored `label` (falling back to `class_type.input_name`) |
| **DROPPED — unverifiable** | key present but the entry carries **no** `kind`/`class_type`/`input_name` (written before 0014) | value discarded | named as *"saved before this workflow changed; could not be checked"* |
| **DEFAULTED — new** | a live input with no stored entry | field pre-filled from `RunInput.Current` | named: *"1 new parameter appeared: Denoise."* |

`substitute` and `option_fixes` are **name-keyed, not positional**, and degrade
safely downstream (an unknown filename / input name is a no-op —
`ApplySubstitutions`, and `ValidateOptionFixes` re-validates option fixes on-list
against live `object_info` inside `realRun`, `run_handlers.go:417-423`). They are
therefore **KEPT unchanged in every case** and are not diffed. Say that in the doc
UI, not in a banner.

### Step 3 — what the user sees

A non-dismissable amber `alert("warning", …)` at the top of the tab body,
rendered only when something was dropped or defaulted:

> **This workflow's graph changed since this preset was saved.**
> 7 of 9 saved values were re-applied. 2 could not be matched and were reset to
> the workflow's current values: **Prompt (POSITIVE)**, **Seed**. 1 new parameter
> appeared: **Denoise**. The workflow mode **"IMAGE2VIDEO"** no longer exists and
> was not applied — pick a mode above.
> Saving this preset will adopt the current values.

Every name in that banner is untrusted (author titles) → `g.Text`.

### Step 4 — two independent guards, neither trusted alone

1. **Render-time reconciliation** (above) — *informative*. It decides what the
   form shows and what it tells the user.
2. **Submit-time allow-list** — *structural*. The run never submits the stored
   blob. It submits the **form's current field values**, which go through the
   unchanged `parseWidgetOverridesForModes` (`run_params.go:74-128`), which
   re-derives the allowed key set from the mode-applied graph, drops anything
   outside it, and drops conflicting duplicates.

This is the repo's existing "lenient parse backed by a structural guarantee
downstream" idiom (`run_params.go:61-63`, `run_modes.go:42-44`) applied to
presets. A reconciliation bug can produce a *wrong-looking form*; it cannot
produce a *submission outside the curated editable set*.

### What is deliberately NOT done

- **The preset is not auto-repaired and its `graph_hash` is not silently
  re-stamped.** Re-stamping on a successful read would hide the drift from the
  next open. Only an explicit Save re-stamps.
- **A drifted preset is not refused.** `handleGenerationRerun` returns 409 on drift
  (`outputs_handlers.go:233-237`) — correct there, because a generation is a
  *historical record* and replaying it with different parameters would be a lie.
  A preset is a *draft the user is editing*; refusing to open it would strand the
  prompt text, which is both what the user cares most about and the least
  position-sensitive thing in the set.
- **Residual risk, stated honestly.** If node 3 stays `CLIPTextEncode.text` but
  the graph is rewired so node 3 now feeds the *negative* conditioning, no
  widget-key check can detect it — the tuple still matches. The mitigation is that
  the reconciled `Label` is rendered beside every field, so the user sees
  "Prompt (NEGATIVE)" pre-filled with their positive text. That is a visibility
  mitigation, not a guarantee. Detecting it properly needs link-graph analysis
  that `DetectRunInputs` does not do.

---

## Multi-mode interaction (problem 2)

**Yes — a preset captures the selected mode, and yes, that closes the existing
gap.**

`runOptions.ModeSelection` is already a first-class per-run override
(`run_handlers.go:161`, applied at `run_handlers.go:370-372`); the only reason
"re-run this generation" cannot restore it is that `generations.params` has no
field for it (`outputs_capture.go:26-37`) — the exact deferred item at
`SESSION-HANDOFF.md:71`, which says it "needs a migration". **0014 is that
migration**, because the fix is a JSON field in the shared snapshot shape, not a
column.

Consequences to implement together:

- `buildRunParamsSnapshot` writes `opts.ModeSelection`.
- `runOptionsFromParams` reads it back — **gated by the same `graph_hash` check as
  the positional overrides**. A `ModeGroup.Key` is `"<selector node id>:<group
  index>"` (`internal/comfy/modes.go:80-83`), i.e. positional in the group array,
  so it is exactly as position-sensitive as a widget index and must not cross a
  graph change unchecked.
- On drift, a mode key that no longer resolves is **dropped and named**, never
  silently ignored (see Step 0 — silent drop degrades to a confusing
  "nothing to run").

**Open question 1 below** is the one thing this does not settle: whether the
page-level `#run-modes` picker or the active preset is the source of truth when
they disagree.

---

## Batch job — state machine

### Shape: extend `runJob`, do not add a second job type

`runSeq` / `data-run-seq` / `#run-status` / `runStatusFragment` are all keyed to
*one* job (`run_pages.go:146-165`). A parallel batch-job type would need every one
of them duplicated. Instead `runJob` gains batch fields, and **a single run is
simply a batch of 1** (`batchTotal == 1`, `batchID == ""`), so every existing path
keeps working with no behavioural change.

```go
// all under Server.runMu, like every other runJob field
batchID     string // "" for a single run
batchTotal  int    // N as requested; 1 for a single run
batchIndex  int    // 1-based index of the item currently in flight
batchDone   int    // items that reached phase==done
batchStop   bool   // Stop requested — cancel the remainder
presetID    int64
presetName  string // snapshot, for the status line and the generation row
itemCancel  context.CancelFunc // the CURRENT item's cancel (batch-level cancel is `cancel`)
```

### States

```
        ┌──────────┐  startBatch (singleton free)
        │  IDLE    │──────────────────────────────┐
        └──────────┘                              ▼
             ▲                            ┌───────────────┐
             │                            │ ITEM i RUNNING│◀────┐
             │                            │ (realRun,     │     │ i < N
             │                            │  unchanged)   │     │ and !batchStop
             │                            └───────┬───────┘     │
             │                                    │             │
             │                    ┌───────────────┼─────────────┴──────┐
             │                    ▼               ▼                    ▼
             │            phase==done      phase==failed          batchStop
             │            capture (async)  (any reason)           observed
             │            batchDone++      ──────────────         ──────────
             │                    │             HALTED               STOPPED
             │                    │        remainder not started  remainder not started
             │                    │        Interrupt NOT sent     Interrupt sent once
             │                    │             │                    │
             └────────────────────┴─────────────┴────────────────────┘
                                        running=false
```

Only **four** terminal shapes, all rendered by the existing
`runStatusFragment` dispatch (`run_pages.go:146-165`) plus one batch summary line:

| terminal | condition | fragment |
|---|---|---|
| **COMPLETE** | all N items `done` | `runTerminal` success + "8 of 8 complete." |
| **HALTED** | item i non-`done` | `runTerminal` failure — the **failing item's** existing `runFailure` panel, unchanged — plus "Batch halted at item 3 of 8 — 2 completed, 5 not started." |
| **STOPPED** | user pressed Stop | `runStopped` + "Batch stopped — 3 of 8 completed, 5 cancelled." |
| **IDLE** | never started / other workflow | unchanged (`run_pages.go:147-153`) |

### The load-bearing edit — 🔴 highest-risk part of the feature

`settleAndCapture` → `applyRunOutcomeLocked` sets `job.running = false`
(`run_handlers.go:297`). **Per-item settle must NOT clear `running`**, or a
concurrent `startRun` slips in between items and the singleton is broken.

Required split:

- `applyItemOutcomeLocked(job, res, err) (terminal bool)` — classifies the item
  exactly as `applyRunOutcomeLocked` does today (same `switch`, reused verbatim)
  but leaves `job.running = true` and returns whether the item was non-`done`.
- `applyBatchOutcomeLocked(job)` — sets `running = false`, `finishedAt`, and the
  batch summary message.
- `settleAndCapture` keeps its exact current ordering contract
  (`run_handlers.go:241-253`): settle under `runMu`, snapshot the phase under that
  **same** lock, run capture **outside** the mutex. Only the settle callback
  changes.

Sequencing under `runMu` for each item:

```
lock:   batchIndex = i; if batchStop { unlock; break }
        itemCtx, itemCancel = WithTimeout(batchCtx, runJobBudget); job.itemCancel = itemCancel
        reset per-item fields (phase, message, promptID, images, preflight, …)
unlock
        res, err := realRun(itemCtx, wf, up, optsForItem(i))   // UNCHANGED function
lock:   terminal := applyItemOutcomeLocked(job, res, err); phase := job.phase
unlock
        if phase == done && len(res.Images) > 0 { capture(...) }   // OUTSIDE the mutex
        if terminal || batchStop { break }
```

`batchCtx` is `context.WithCancel(baseCtx)` with **no total deadline** — a
legitimate 8×10-minute batch is 80 minutes. Termination is still provable:
`N ≤ N_max` and each item is bounded by `runJobBudget`, so worst case is
`N_max × 30 min`. That bound is the primary argument for the cap value below.

### Interaction with `enforceOutputsCap`

Item i's capture runs off the mutex and can overlap item i+1's run
(`outputs_capture.go:344-346` already documents overlapping captures).
`enforceOutputsCap(keepID)` never evicts `id >= keepID`
(`outputs_capture.go:403-407`), and generation ids are monotonic, so item i's
eviction pass **cannot** evict item i+1's row. This property is what makes a batch
safe against self-eviction, and it already holds — but it must be pinned by a test,
because it is currently only an incidental consequence of the `keepID` guard.

It does **not** protect the user's *older* generations: a large batch can evict
them. That is the disk argument for the cap.

---

## Cancel semantics (problem 3)

`stopRun` is extended, not replaced. Its current ordering is load-bearing and is
preserved (`run_handlers.go:503-521`, and the terminal-render dependency at
`run_pages.go:154-160`):

1. **Under `runMu`**: set `stopped = true` **and** `batchStop = true`, read
   `job.itemCancel` and `job.cancel`.
2. **Unlock**, then cancel the **item** context (unblocks `realRun`'s poll loop at
   `run_handlers.go:494-498`) and the **batch** context.
3. Best-effort `client.Interrupt(ctx)` with its own 5s timeout — **once**.

Precisely what happens to each part:

- **The currently-executing ComfyUI prompt** — `Interrupt` is sent. It takes no
  prompt id (`run_handlers.go:37`), so it interrupts whatever ComfyUI is executing
  right now. Because we submit strictly one at a time and never enqueue ahead, that
  is our item. ⚠ It can also kill a prompt the user submitted from their own
  ComfyUI tab — **that hazard exists today for a single run and the batch does not
  widen it**; do not silently expand it (e.g. do not add a queue-clear).
- **The not-yet-submitted remainder** — never submitted at all. `batchStop` is
  read at the top of each loop iteration under `runMu`, before any work. Nothing to
  cancel because nothing was queued: this is the direct payoff of decision 1's
  sequential submission.
- **Already-captured generations** — kept. Stop is not undo. The images are on
  disk and the rows exist; deleting them would be destroying the user's output.

`stopped` is set **synchronously**, before the goroutine settles, which is exactly
why `runStatusFragment` keys the terminal off it (`run_pages.go:154-160`) — the
Stop response *and* any in-flight poll immediately render the poller-free view and
the poll loop halts instead of re-arming. That invariant is unchanged.

**What the user sees:** "Batch stopped — 3 of 8 completed, 5 cancelled." plus the
existing "Run again" control. No poller.

---

## Failure mid-batch (problem 6) — HALT, uniformly

**Rule: the first item that reaches any non-`done` terminal state halts the
batch.** One rule, no classification.

Why halt and not skip/continue:

- **Preflight failures are deterministic across items.** Only the seed differs
  between items, and preflight checks installed nodes/models/option validity
  (`run_handlers.go:405-454`) — none of which the seed affects. Item 4 would fail
  identically to item 3, seven more times, while the user watches.
- **The failure panel is interactive and needs the user.** It offers Fix/resolve
  (`missingModelsPanel`), gated node-pack install (`missingNodesPanel`), and
  option pickers (`incompatibleOptionsSection`) — and each of those actions
  *starts a new run* (`/run-substitute`, `/install-option-and-run`,
  `/run-with-options`). They cannot coherently target "item 4 of 8" of a batch
  that has already moved on. Halting keeps them meaningful: they start a fresh
  single run, and the user re-queues the batch when it works.
- **ComfyUI rejections (`PromptValidationError`) are equally deterministic.**
- **Transport/timeout errors could be transient**, but distinguishing transient
  from permanent is not something we can do reliably, and retrying against a real
  GPU is exactly the runaway the cap exists to prevent. A uniform rule beats a
  clever one.

**Rejected alternatives, explicitly:** *skip-and-continue* (burns the whole batch
producing nothing on a deterministic failure, and orphans the interactive fix
actions); *pause-and-ask* (needs a new "resume" job state, a durable batch row,
and a new UI state — real scope for a case where halting loses only the not-yet-
started remainder, which the user can re-queue with one click).

**What is reported:** the failing item's existing `runFailure` panel, byte-for-byte
unchanged — every missing-model / node-attribution / option-fix affordance keeps
working. Above it, one batch line:

> **Batch halted at item 3 of 8** — 2 completed, 5 not started.

The 2 completed generations are captured and visible in the rail. The Queue ×N
control is re-rendered with N pre-filled to the remainder (5), so re-queueing after
a fix is one click.

---

## Cap on N (problem 5)

### `maxBatchCount = 25`

Three independent arguments, all grounded:

1. **Provable worst-case wall clock.** Each item is bounded by
   `runJobBudget = 30 min` (`run_handlers.go:22`). N=25 ⇒ a 12.5-hour theoretical
   ceiling for a fully wedged batch. N=100 ⇒ 50 hours. 25 keeps the provable bound
   in "bad day" rather than "abandoned overnight" territory.
2. **Disk, which is the real hazard.** Every successful item captures its images
   and then runs `enforceOutputsCap`, which **evicts the oldest generations** to
   stay under `OutputsMaxBytes` (`outputs_capture.go:357-441`, logged at INFO
   because "silently deleting the user's own generated images must be
   observable"). A batch therefore has an N-fold amplified ability to churn the
   user's earlier generations out of existence. 25 keeps one click from being able
   to turn over a typical cap.
3. **Unwinding is manual.** Stop kills the remainder, but every already-captured
   generation stays, and removing them is one delete per generation.

**25 is a cap, not a default.** The control offers quick picks **2 / 4 / 8 / 16**
plus a number input. The server clamps: `n = min(max(n, 1), maxBatchCount)`. The
**server-side clamp is the authority** — a hand-built request asking for 500 is
clamped and *told*, not rejected (a 400 would be fine too, but the UI's own input
must never be able to produce one).

### Starting a batch while one is running

Today this is a **silent no-op**: `startRun` returns early
(`run_handlers.go:197-199`) and the handler re-renders the current status
(`run_handlers.go:593-594`). For a single re-click that is tolerable; for
"I clicked Queue ×8 and nothing happened" it is not.

**Change:** `startRun` / `startBatch` return `bool` (started). The handlers render
a one-line refusal above the (unchanged) status fragment:

> A batch is already running (**3 of 8**). Stop it before starting another.

This is a small, contained improvement that also fixes the existing single-run
silent no-op. It touches `handleWorkflowRun`, `handleWorkflowRunWithParams`,
`handleWorkflowRunSubstitute`, `handleWorkflowRunWithOptions`, and
`handleGenerationRerun` — all of which currently discard the refusal.

### `maxPresetsPerWorkflow = 12`

- Tabs are a horizontal strip. Past ~12 the strip stops being scannable and needs
  its own overflow UI — scope we are not building.
- It bounds render cost **only because inactive tabs are label-only**: exactly
  **one** `DetectRunInputs` runs per page render (the active tab), which is the
  same cost as today (`run_params.go:142`). Reconciling all 12 on every render
  would be a 12× regression on the run page for no benefit.
- At the cap, **Fork** renders disabled with the reason. No silent eviction of the
  oldest preset — presets are user data.

---

## Batch identity in the output gallery (problem 4)

`generations` gains `batch_id` / `batch_index` / `batch_total` / `preset_id` /
`preset_name` (schema above). `captureGeneration` reads them off the job snapshot
and writes them alongside the existing snapshot fields (`outputs_capture.go:296-304`).

Grouping, in ascending order of scope — pick the smallest that solves the noise:

- **Recent-outputs rail** (`outputs_rail.go`, `outputsRailLimit = 12`): an 8-item
  batch would otherwise consume 8 of 12 slots. **Collapse a batch to ONE tile**
  showing the first image with a `×8` corner badge, linking to the batch view.
  The rail's store query stays a fixed bounded read — collapsing in the renderer
  over the rows already fetched would let a batch-heavy history under-fill the
  rail, so **fetch `outputsRailLimit * 2` and clamp to 12 groups** instead.
- **`/outputs` grid** (`outputs_pages.go:87`): each tile gains a small
  "Batch 3/8 · «Hi-res 8-step»" caption. No structural change.
- **New: `GET /outputs/batch/{batch_id}`** — the N generations of one batch in
  order, with the preset's params rendered once at the top instead of N times.
  This is the surface that makes a batch worth having: "here are my 8 seeds of the
  same prompt, side by side."

`preset_name` being snapshotted means a deleted preset still labels its batch —
the exact `workflow_name` idiom from 0012.

⚠ `batch_id` is opaque and generated server-side (`comfy.NewID()`). It must be
treated as untrusted when it arrives in a URL path: validate it as a bare
`[A-Za-z0-9_-]{1,64}` before it reaches a query, and let the query return zero
rows for anything unknown.

---

## HTTP surface

Every new POST: `r.ParseForm` → `s.verifyCSRF(w, r)` → `s.gate(w)`, in that exact
order, matching `handleWorkflowRun` (`run_handlers.go:568-578`).

**Why the preset CRUD endpoints are loopback-gated too**, even though they touch
no filesystem path: they are the *input* to a run, and the run panel itself is
already loopback-only — `runPanel` renders "Local run is disabled when the server
is bound to a non-loopback address" and nothing else when `!extraAllowed`
(`run_pages.go:32-38`). An ungated preset editor would be an editor for a surface
the caller cannot use. Uniformity beats a per-endpoint judgement call here.

| Method | Path | Gating | Returns |
|---|---|---|---|
| `GET` | `/workflows/{id}/run/params` | loopback | **existing**, unchanged target; now renders the tab strip + active tab body (see below) |
| `POST` | `/workflows/{id}/run/presets` | CSRF + loopback | innerHTML of `#run-params`: strip + **new** tab active. `from=<pid>` ⇒ Fork (deep-copies that preset's params); absent ⇒ a fresh preset seeded from the graph's current values |
| `POST` | `/workflows/{id}/run/presets/{pid}/activate` | CSRF + loopback | innerHTML of `#run-params`: **saves the posted (outgoing) field values into the currently-active preset**, then renders with `{pid}` active |
| `POST` | `/workflows/{id}/run/presets/{pid}/save` | CSRF + loopback | innerHTML of `#run-params`: persist values + name + mode, **re-stamp `graph_hash`**, clear the drift banner |
| `POST` | `/workflows/{id}/run/presets/{pid}/delete` | CSRF + loopback | innerHTML of `#run-params`: strip with the neighbouring tab active |
| `POST` | `/workflows/{id}/run/queue` | CSRF + loopback | innerHTML of `#run-status`: the running fragment with batch progress (or the refusal line) |
| `GET` | `/outputs/batch/{batch_id}` | none (read-only page, like `/outputs`) | full page: the batch's generations in order |
| `POST` | `/workflows/run/stop` | **existing**, CSRF + loopback | unchanged signature; now cancels the whole batch |
| `GET` | `/workflows/{id}/run/status` | **existing**, loopback | unchanged shape; fragment gains the batch progress line |

### Why tab-switch is a POST that saves

Requirement 3 says **unrun drafts must survive**. A GET tab-switch would silently
discard whatever the user typed in the outgoing tab. Making tab-switch the
*activate* POST — which saves the outgoing values and returns the new tab — makes
the switch atomic, CSRF'd, and draft-preserving in one round trip, with no
dirty-state tracking and no `beforeunload` JS. `/save` stays as the explicit
"commit this and adopt the current graph" action (it is the only thing that
re-stamps `graph_hash`).

### `POST /workflows/{id}/run/queue` — parameters

| field | validation |
|---|---|
| `csrf_token` | `verifyCSRF` |
| `preset_id` | must belong to `{id}`; a mismatch is 404, never a cross-workflow read |
| `count` | parsed, then **clamped** `min(max(n,1), 25)` server-side |
| `wp_node` / `wp_widget` / `wp_value` | the **existing** parallel arrays, parsed by the **unchanged** `parseWidgetOverridesForModes` (`run_params.go:74`) |
| `mode_key` | the **existing** field, parsed by the **unchanged** `parseModeChoices` (`run_modes.go:45`) |
| `confirm_no_seed` | required **only** when the reconciled graph exposes no `RunInputSeed` (below) |

### The no-seed guard

If the mode-applied graph exposes **zero** inputs with `Kind == RunInputSeed`,
every item in the batch is byte-identical. wf587 has exactly one seed, but a graph
whose sampler is a custom class outside `runInputLayouts` (`run_inputs.go:87-131`),
or whose seed lives inside a subgraph — which `DetectRunInputs` explicitly does not
scan (`run_inputs.go:149-154`) — surfaces none.

> ⚠ **Could not verify:** whether ComfyUI returns cached outputs for an identical
> re-submitted prompt (making the batch not just identical but instant and free).
> Do not assert that in user-facing copy. What *is* certain from our own code is
> that the submitted graph would be byte-identical.

**Behaviour: offer, do not perform** — the repo's established idiom for exactly
this shape (the substitute confirmation, repo `CLAUDE.md`). The first click
returns an offer:

> This workflow exposes no editable seed, so all 8 runs would use identical
> parameters. **[Queue 8 identical runs anyway]** · [Run once instead]

Only a second click carrying `confirm_no_seed=1` proceeds. A hard block is wrong —
a workflow can carry randomness we cannot see — but a silent 8× identical batch is
worse.

### Seed randomization details

Per batch item, on top of the reconciled override map:

```
for _, ri := range live {                       // live = DetectRunInputs(modeAppliedGraph, nil)
    if ri.Kind == comfy.RunInputSeed {          // NOT isSeedControlSlot — see the ground-truth section
        overrides[comfy.UIWidgetKey{NodeID: ri.NodeID, Widget: ri.WidgetIndex}] = freshSeed()
    }
}
```

This overrides the preset's stored seed **whether or not it stored one** —
decision 2 says every queued item gets a new seed.

`freshSeed()` uses `crypto/rand` over `[0, 1e15)`, deliberately matching the range
the existing client-side dice button already uses
(`Math.floor(Math.random() * 1000000000000000)`, `run_params.go:291`). Rationale:
the seed the dice button produces and the seed a batch produces should come from
the same space, and 1e15 < 2^53 so the value round-trips exactly through the JSON
number the widget slot holds (`typedOverrideValue` preserves integer precision,
`widget_overrides.go:45-47`). ComfyUI accepts larger seeds, but a value above 2^53
is mangled by any JS that touches it.

---

## UI shape (gomponents + htmx)

### Container layout — `#run-params` is reused, not replaced

```
card "Run"
├── #run-comfy-status   ← STABLE. lazy pill + Run button.        (unchanged)
├── #run-modes          ← STABLE. mode <select>s, hx-included by everything. (unchanged)
├── #run-params         ← STABLE. mode select's existing hx-target.  ★ new content
│    ├── <div role="tablist" class="cm-run-tabs">
│    │      <button role="tab" aria-selected="true"  …>Base</button>
│    │      <button role="tab" aria-selected="false" …>Hi-res 8-step</button>
│    │      <button class="cm-run-tab-add">+ Fork</button>
│    └── <form id="run-preset-form">
│           [drift banner — alert("warning", …), only when something drifted]
│           [csrf_token] [preset_id]
│           [name input]
│           …the existing runParamField() controls, UNCHANGED…
│           [Run once] [Queue ×N: 2 4 8 16 + number] [Save] [Delete]
└── #run-status         ← STABLE. the ONLY polled container.       (unchanged)
```

**This is the key structural decision and it is deliberately the least invasive
one available.** The tabs live *inside* `#run-params`, which is already the mode
select's `hx-target` (`run_modes.go:164`). So:

- `run_modes.go` needs **no** target change; `runParametersPanelForModes`
  (`run_modes.go:214-228`) gains the strip around the fields it already builds.
- `#run-status` and `#run-params` are **siblings, never nested**
  (`run_pages.go:61-63`), so the run poller swapping `#run-status` every second
  **cannot clobber a half-typed prompt in a tab**. The streaming invariant and the
  editable form are structurally isolated, for free.
- The poller still never `outerHTML`-replaces itself: `runPoller`
  (`run_pages.go:192-200`) targets `#run-status` and swaps `innerHTML`. Unchanged.

### htmx wiring

- Every tab button, Fork, Save, Delete, Run once and Queue ×N carries
  `hx-include="#run-preset-form, #run-modes select"` — the form's fields **and**
  the existing `runModesInclude` (`run_modes.go:26`), the same pattern every
  current run control uses (`run_pages.go:111`, `:271`, `:366`, `:471`).
- Tab buttons / Fork / Save / Delete → `hx-target="#run-params"`,
  `hx-swap="innerHTML"`.
- Run once / Queue ×N → `hx-target="#run-status"`, `hx-swap="innerHTML"` —
  identical to the existing Run button (`run_pages.go:104-114`).
- `hx-disabled-elt="this"` on Queue ×N so a double-click cannot race the
  singleton check.

### Status fragment

`runRunning` (`run_pages.go:204-227`) gains one line when `batchTotal > 1`:

```
⟳  Item 3 of 8 · Generating…            [Stop batch]
```

and the terminal fragments gain the batch summary line. `dataRunSeq` is
**unchanged**: one `runSeq` per **batch**, not per item, so `data-run-seq` keeps
meaning "this run attempt" and `run_seq_attr_web_test.go` keeps passing. Add
`data-batch-index` / `data-batch-total` on the same root, mirroring the documented
single-DOM-hook rationale (`run_pages.go:167-171`), so the ux-audit harness can
pin progress without scraping prose.

### CSS — 🔴 the purged-Tailwind trap

New classes must be **`.cm-*` custom CSS in `internal/web/assets/app.css`**, not
new Tailwind utilities. A new utility in an `h.Class("…")` string is **unstyled**
until `output.css` is regenerated (repo `CLAUDE.md`).

There is already a tab idiom to reuse: `.cm-version-tabs` / `.cm-version-tab` /
`.cm-version-tab-active` (`app.css:352-395`), used by the model detail page's
version tabs. Reuse it or add `.cm-run-tab*` beside it in the same file. Either
way, all `.cm-*` rules there are written against `--civitai-*` theme tokens, so
**both `data-theme` paths are covered by construction** — that is the reason to
prefer them over hand-rolled colors.

If any Tailwind utility *is* added, regenerate before committing:

```
cd internal/web && nix-shell -p tailwindcss --run \
  "tailwindcss -c tailwind.config.js -i input.css -o assets/output.css --minify"
```

Accessibility: `role="tablist"` / `role="tab"` / `aria-selected`, following the
existing library-tabs markup (`library_pages.go:183-207`) which is already pinned
by `library_tabs_web_test.go`.

---

## Test plan

This is required, not optional. Every entry states the assertion **and** the real
bug it catches.

### Unit — `internal/comfy` (pure, no I/O)

| test | asserts | catches |
|---|---|---|
| `TestSeedInputsSelectedByKind` | over the real `wf587_converted_widgets.json` fixture, the seed set is exactly `{node 40, widget 0}`, selected via `Kind == RunInputSeed` | **the brief's `isSeedControlSlot` mistake** — an `isSeedControlSlot`-based implementation returns ∅ here and every batch item is identical |
| `TestSeedInputsIgnoreControlSlot` | no `RunInput` is produced for a `"randomize"`/`"fixed"` string slot; the KSampler's `sampler_name`/`scheduler` land at widget **4/5** | the control-slot off-by-one — an implementation that forgets it randomizes `steps` |
| `TestRandomSeedsDifferPerItem` | 25 generated seeds are pairwise distinct, all integers in `[0, 1e15)` | a batch that reuses one seed (identical outputs); a value ≥ 2^53 that loses precision through the JSON widget slot |
| `TestRandomSeedOverridesEvenWhenPresetPinsIt` | a preset storing `seed=12345` still yields a *fresh* seed per item | "everything else identical" being read as "including the seed" |
| `TestReconcileExactHashAppliesEverything` | equal non-blank hashes ⇒ all entries KEPT, no per-entry tuple check, empty banner | over-strict reconciliation that drops valid presets and looks broken |
| `TestReconcileBlankHashIsDrifted` | blank on either side ⇒ DRIFTED, not EXACT | the pre-0011 NULL `graph_hash` case silently applying stale positions |
| `TestReconcileDropsRetargetedSlot` | stored `{node 40, w0, kind=seed}` against a graph where `{node 40, w0}` is now `kind=text` ⇒ DROPPED, field defaulted to `RunInput.Current`, entry named in the report | **the headline bug**: a captured seed being type-coerced into a prompt |
| `TestReconcileDropsVanishedKey` | stored key absent from `live` ⇒ DROPPED and named by its stored label | a silently-ignored value the user believes is applied |
| `TestReconcileDropsUnverifiableLegacyEntry` | entry with no `kind`/`class_type`/`input_name` + drifted hash ⇒ DROPPED, distinct reason string | pre-0014 blobs being trusted across a graph change |
| `TestReconcileReportsNewInputs` | a live input with no stored entry ⇒ DEFAULTED + named | new parameters appearing silently at their graph defaults |
| `TestReconcileKeepsNameKeyedFamilies` | `substitute` / `option_fixes` survive DRIFT unchanged and are never in the drop list | over-eager reconciliation breaking the missing-model substitute flow |
| `TestReconcileDropsUnknownModeKey` | a `mode_selection` key `DetectModeSelectors` no longer surfaces ⇒ dropped **and named** | a template silently converting to an all-bypassed graph and failing as "nothing to run" |
| `TestReconcileRunsAgainstModeAppliedGraph` | for a multi-mode fixture, reconciliation uses `ApplyModeSelection(graph, modes)`, not the stored graph | reconciling against an all-bypassed graph, which surfaces **zero** inputs and would drop every entry |

### Unit — snapshot round-trip (`internal/web`)

| test | asserts | catches |
|---|---|---|
| `TestSnapshotRoundTripsModeSelection` | `buildRunParamsSnapshot` → JSON → `runOptionsFromParams` preserves `ModeSelection` | the 0014 gap-closure silently not working |
| `TestRunOptionsFromParamsGatesModeOnHash` | mode selection is withheld with a non-empty `staleReason` when hashes differ | positional mode keys crossing a graph change unchecked |
| `TestSnapshotJSONIsStable` | two builds of the same options marshal byte-identically (the existing sort discipline, `outputs_capture.go:115-137`, extended to the new fields) | unstable params blobs that break diffing/comparison |

### Unit — batch mechanics

| test | asserts | catches |
|---|---|---|
| `TestClampBatchCount` | `0→1`, `1→1`, `25→25`, `26→25`, `500→25`, `-3→1`, `"abc"→1` | a hand-built request queueing 500 GPU runs |
| `TestBatchStateTransitions` | table-driven over a fake `runFn`: 8 successes ⇒ COMPLETE(8/8); failure at 3 ⇒ HALTED(index 3, done 2, 5 not started); stop at 3 ⇒ STOPPED(3 done, 5 cancelled) | every off-by-one in the "3 of 8" accounting |
| `TestBatchHaltsOnFirstFailure` | the fake `runFn` is invoked exactly **3** times for a batch of 8 failing at 3 | skip-and-continue sneaking in and burning the GPU on a deterministic preflight failure |
| `TestBatchStopPreventsFurtherSubmits` | after Stop during item 3, `runFn` invocation count never increases | a remainder that keeps submitting after Stop — the exact thing decision 1 exists to prevent |
| `TestBatchSingletonHeldAcrossItems` | `startRun` returns `false` at every point between items | the `applyItemOutcomeLocked` split accidentally clearing `running` — **the riskiest edit** |
| `TestBatchSeqIsOnePerBatch` | `runSeq` increments once for a batch of 8; `data-run-seq` is constant across all items | breaking the documented `data-run-seq` contract (`run_pages.go:167-171`) |

### Store — `internal/store`

| test | asserts | catches |
|---|---|---|
| `TestMigration0014Applies` | a fresh DB reaches 0014; an **existing** DB seeded at 0013 with generations rows migrates with those rows intact and the new columns NULL | an `ALTER TABLE` that fails on a populated table (the `REFERENCES`-must-default-NULL rule) |
| `TestRunPresetCRUD` | insert / list-by-workflow ordered by `(position, id)` / update / delete; `params` defaults to `'{}'` | ordering that makes tabs jump between renders |
| `TestRunPresetCascadeOnWorkflowDelete` | `DeleteWorkflow` removes the workflow's presets **and leaves its generations** (`workflow_id` NULL) | the CASCADE/SET-NULL asymmetry silently inverting and deleting the user's images |
| `TestGenerationPresetIDSetNullOnPresetDelete` | deleting a preset NULLs `generations.preset_id` and **keeps `preset_name`** | a preset delete cascading into image deletion |
| `TestListGenerationsByBatch` | returns the batch in `batch_index` order; an unknown `batch_id` returns zero rows, not an error | a batch page that 500s on a stale/hostile id |
| `TestPresetCountCapEnforced` | inserting a 13th preset for one workflow is refused at the store layer | a client-only cap |
| `TestForeignKeysActuallyEnforced` | inserting a preset with a nonexistent `workflow_id` fails | `PRAGMA foreign_keys` regressing (all the SET NULL / CASCADE reasoning above depends on it) |

### Web — `internal/web` (via `newTestServer`, `web_test.go:136`)

**Render states** (each asserts the exact markup a browser would act on):

| test | asserts | catches |
|---|---|---|
| `TestRunPanelRendersTabStrip` | `role="tablist"`, one `role="tab"` per preset, exactly one `aria-selected="true"` | a tab strip with zero or two active tabs |
| `TestRunPanelNoPresetsRendersDefaultTab` | a workflow with no stored presets renders one tab seeded from the graph's current values | an empty run panel on first use |
| `TestOnlyActiveTabIsReconciled` | with 12 presets, `DetectRunInputs` runs **once** (via a counting seam) | the 12× render regression the cap's reasoning depends on |
| `TestDriftBannerNamesEveryDroppedField` | drifted preset ⇒ `alert("warning", …)` present and containing each dropped label and each new label | a banner that says "something changed" without saying what |
| `TestNoDriftBannerOnExactHash` | equal hashes ⇒ no banner | crying wolf until users ignore the banner |
| `TestUntrustedPresetNameEscaped` | a preset named `<img src=x onerror=alert(1)>` renders escaped in the tab, the banner, and the batch caption | stored XSS via a tab label |
| `TestQueueControlOffersNoSeedConfirm` | a seedless graph's Queue ×N returns the offer, **not** a started batch, and the offer names N | a silent 8× identical batch |
| `TestQueueSecondClickWithConfirmStarts` | `confirm_no_seed=1` starts the batch | an offer with no way through |
| `TestBatchProgressLineRendered` | running fragment contains "Item 3 of 8", `data-batch-index="3"`, `data-batch-total="8"`, and a poller targeting `#run-status` | a batch that looks like a stuck single run |
| `TestTerminalFragmentHasNoPoller` | COMPLETE / HALTED / STOPPED fragments contain **no** `#run-poll` | an infinite poll loop after the batch ends |
| `TestPollerTargetsStableContainer` | every fragment's poller has `hx-target="#run-status"` and `hx-swap="innerHTML"`, and no element `outerHTML`-replaces the polling node | the repo's streaming invariant regressing |
| `TestTabStripOutsideRunStatus` | the tablist markup is **not** inside `#run-status` | the poller clobbering a half-typed prompt every second |
| `TestHaltedFragmentKeepsFailurePanel` | a mid-batch preflight failure still renders `missingModelsPanel` / `missingNodesPanel` / `incompatibleOptionsSection` | the batch wrapper swallowing the interactive fix affordances |

**Security / gating** (one per new endpoint, table-driven):

| test | asserts | catches |
|---|---|---|
| `TestPresetEndpointsRejectMissingCSRF` | each new POST without a token ⇒ **403**, and **no** store mutation | a CSRF-able preset or batch trigger |
| `TestPresetEndpointsRejectBadCSRF` | a wrong token ⇒ 403 | a constant-time-compare regression |
| `TestPresetEndpointsRejectNonLoopback` | with `extraPathsAllowed()` false, each new endpoint (POST **and** the GET) returns the gate note and starts nothing (mirrors `nonloopback_gate_web_test.go`) | a remotely-triggerable GPU batch |
| `TestPresetCrossWorkflowRejected` | `POST /workflows/1/run/presets/{pid}/save` where `{pid}` belongs to workflow 2 ⇒ 404, no write | reading / overwriting another workflow's preset via an id guess |
| `TestQueueRejectsForeignPresetID` | same for `/run/queue` | running workflow 1's graph with workflow 2's params |
| `TestBatchIDPathValidated` | `/outputs/batch/{id}` with `../`, quotes, or a 500-char id ⇒ 400/404, never a query error | an injection-shaped id reaching the store |
| `TestSecondBatchRefusedWithMessage` | starting anything while a batch runs ⇒ the refusal line naming "3 of 8", and `runFn` is not re-invoked | the current **silent** no-op (`run_handlers.go:197-199`) leaving the user thinking nothing works |

**Behavioural / mid-batch:**

| test | asserts | catches |
|---|---|---|
| `TestStopCancelsRemainder` | Stop during item 3 of 8 ⇒ `runFn` never called a 4th time; `Interrupt` called exactly **once**; the 2 captured generations survive | the headline decision-1 requirement |
| `TestStopIsIdempotent` | two Stops ⇒ one `Interrupt`, same terminal fragment | double-click producing two interrupts (one of which could hit the user's own next prompt) |
| `TestActivateSavesOutgoingTab` | typing in tab A then clicking tab B persists A's values | requirement 3's "unrun drafts must survive" |
| `TestForkDeepCopies` | editing the fork does not mutate the source preset's stored params | a shared-map aliasing bug |
| `TestForkRefusedAtCap` | the 13th Fork returns the disabled control + reason, and inserts nothing | a client-only cap |
| `TestCaptureRecordsBatchColumns` | each captured generation carries `batch_id`, correct 1-based `batch_index`, `batch_total`, `preset_id`, snapshotted `preset_name` | an ungroupable gallery — problem 4's whole point |
| `TestEvictionNeverEvictsLaterBatchItem` | with a tiny `OutputsMaxBytes`, item 3's eviction pass never deletes item 4+ | the batch eating its own output via `enforceOutputsCap` |

### `-race` coverage

`go test -race ./internal/web/... ./internal/comfy/... ./internal/store/...` in the
gate, plus these specifically race-shaped tests:

| test | asserts | catches |
|---|---|---|
| `TestBatchConcurrentStartRunRace` | 50 goroutines calling `startRun` while a batch runs; exactly **zero** start, and `runJobState()` is polled concurrently throughout | **the riskiest edit**: `applyItemOutcomeLocked` clearing `running` between items and letting a second run in |
| `TestBatchProgressSnapshotRace` | `runJobState()` from N goroutines against a live batch never returns a torn view (`index ≤ total`, `done ≤ index`) | appending to progress and snapshotting under *different* locks — the repo's streaming invariant |
| `TestStopDuringSubmitRace` | Stop racing the submit boundary always lands in exactly one terminal state | a batch that both halts and continues |
| `TestOverlappingCapturesRace` | two batch-item captures overlapping `enforceOutputsCap` (already possible today, `outputs_capture.go:344-346`) | the `keepID` guard regressing under batch pressure |

### Live verification (not a unit test — required before merge)

Browsers are unavailable on this host (repo `CLAUDE.md`), so:

- **HTTP-level reproduction** against a dogfood `serve`: each button's
  `hx-vals`/form *is* its request — `curl` it and assert the returned fragment.
  Extract CSRF from the `hx-vals` JSON (`csrf_token&#34;:…`), not from an
  `<input>`.
- **State-mutating actions against a THROWAWAY temp DB only**, never
  `~/.config/civitai-manager/civitai-manager.db`. Queue ×N genuinely runs the GPU:
  verify with **N=2** against a trivial workflow, or with a fake `comfyClientFn`.
- **Real end-to-end** against the local ComfyUI at `127.0.0.1:8188` with wf587:
  Queue ×3, confirm three generations with three **different** seeds in
  `generations.params` and one shared `batch_id`; then Queue ×5 and Stop after
  item 2, confirming items 3–5 never reach ComfyUI's `/history`.
- Report **deployed ≠ verified** honestly: HTTP-level reproduction verifies the
  server response, not browser DOM/JS dispatch.

### Gate

`gofmt -l ./internal/ ./cmd/` (empty), `go build ./...`, `go vet ./...`,
`go test ./...`, `go test -race` on `web`/`comfy`/`store`. Full adversarial
`/audit-pr` — this touches web endpoints, a DB migration, concurrency, and drives
real GPU work, which is the highest blast-radius class in this repo.

---

## Phased implementation plan

Each phase compiles, passes the gate, and is independently shippable. Commit in
small increments — a bare `git commit` sweeping everything yields broken
intermediate trees (repo `CLAUDE.md`).

| # | scope | ships alone? | risk |
|---|---|---|---|
| **P0** | `0014_run_presets.sql` + `internal/store/run_presets.go` (CRUD, cap, cascade) + the `generations` batch columns in `store.Generation` / scan / insert. **No UI, no handlers.** | Yes — dead schema, zero behaviour change | 🟢 low. The one hazard is `ALTER TABLE … REFERENCES` on a populated table; `TestMigration0014Applies` covers it. |
| **P1** | `internal/comfy/run_presets.go` — the **pure** reconciler: `Reconcile(graph, modes, stored) Reconciliation{Kept, Dropped, Defaulted, DroppedModes}`. No store, no I/O. Plus the snapshot struct extensions + round-trip. | Yes — unreferenced pure code | 🟡 The correctness core. Heavily unit-tested; no runtime exposure until P3. |
| **P2** | Tab strip **read-only**: `runParametersPanelForModes` renders the strip + active tab from stored presets; a workflow with none gets one implicit tab. Run once works exactly as today. | Yes | 🟡 Touches the run page's most-tested renderer. Mitigated by reusing `#run-params` so `run_modes.go` is untouched. |
| **P3** | Fork / Save / Delete / activate endpoints + the drift banner (wires P1 into P2). | Yes | 🟡 New CSRF + gated endpoints; the cross-workflow id checks are the thing to get right. |
| **P4** | **Seed randomization + the batch job + Queue ×N + batch cancel.** `applyItemOutcomeLocked` / `applyBatchOutcomeLocked` split, `startBatch`, `stopRun` extension, `startRun` returning `bool` + the refusal line, the no-seed confirm. | Yes | 🔴 **Riskiest — see below.** |
| **P5** | Capture writes the batch columns; rail collapses a batch to one tile; `/outputs` caption; `GET /outputs/batch/{id}`. | Yes (batches just look ungrouped until it lands) | 🟢 low, read-mostly. |
| **P6** | Wire `mode_selection` into `buildRunParamsSnapshot` / `runOptionsFromParams` (hash-gated) → **closes the deferred re-run gap**; optional "Open as preset" on the generation page. | Yes | 🟡 Changes existing re-run behaviour; the 409 path must keep working. |

### The riskiest part, called out

**P4's `applyItemOutcomeLocked` / `applyBatchOutcomeLocked` split.**

`applyRunOutcomeLocked` today unconditionally sets `job.running = false`
(`run_handlers.go:297`), and it is the shared tail of *every* run path — the plain
run, the download-then-run job, and the resolve / substitute paths. Splitting it so
per-item settle leaves `running == true` is a **four-line change with a
server-wide invariant hanging off it**: get it wrong and the singleton opens
between items, two runs submit concurrently, and the failure is nondeterministic
and load-dependent — the hardest possible thing to catch after the fact.

Mitigations, all mandatory:
- `TestBatchSingletonHeldAcrossItems` + `TestBatchConcurrentStartRunRace` under
  `-race`, with 50 adversarial concurrent `startRun` callers.
- The split must **reuse the existing `switch` verbatim** for classification —
  only the `running` / `finishedAt` assignment moves. No re-derivation.
- Live-verify P4 with a **fake `comfyClientFn`** before ever pointing it at the
  real GPU.

Second-riskiest: the **`#run-params` content swap in P2** touches the most
test-covered renderer on the run page. It is deliberately structured so
`run_modes.go` and `#run-status` need **no** changes at all.

---

## Invariants checklist

- **CSRF on every POST** — all six new POSTs use the `ParseForm → verifyCSRF →
  gate` prologue verbatim from `run_handlers.go:568-578`. ✅
- **Loopback-gating** — every new endpoint that reaches ComfyUI *or* feeds a run
  is `s.gate(w)`-ed, including the preset CRUD (rationale above). ✅
- **Race-safe streaming jobs** — progress append **and** snapshot both under
  `runMu`; the poller targets the stable `#run-status` and swaps `innerHTML`;
  nothing `outerHTML`-replaces the polling node. Tabs live outside `#run-status`
  so the two never interact. ✅
- **Offline / no-CDN** — no new assets; the tab strip is markup + `.cm-*` CSS. ✅
- **Theme-aware** — `.cm-*` rules in `app.css` are written against `--civitai-*`
  tokens, covering both `data-theme` paths. ✅
- **Tailwind purge** — prefer `.cm-*`; if any utility class is added, regenerate
  `output.css` with the documented command **in the same commit**. ✅
- **Migrations append-only and ordered** — 0014 verified as the next free number;
  the file only `CREATE`s and `ADD COLUMN`s. ✅
- **The stored workflow is never mutated** — presets are a separate table; every
  override still applies to an ephemeral copy inside `realRun`
  (`run_handlers.go:365-403`). ✅
- **Never substitute silently** — the no-seed batch is an *offer* requiring a
  second confirmed click, mirroring `confirm_substitute`. ✅

---

## Open questions / needs the user's decision

1. **🔴 Mode ownership — the one thing this design cannot settle from the code.**
   Decision 4 says a tab holds the *full* run-parameter set, which includes the
   mode. But `#run-modes` is a page-level container that every run control
   `hx-include`s (`run_modes.go:21-26`) and that sits **outside** `#run-params`. If
   a preset stores mode X and the user then changes the picker to Y, which wins?
   - **(a)** Picker wins; a preset's stored mode only *pre-selects* it on open —
     needs `#run-modes` re-rendered on tab switch (a second `hx-target`, or
     `hx-swap-oob`).
   - **(b)** Move the mode `<select>` **inside** the preset form, making mode a
     genuinely per-preset field. Cleanest conceptually and truest to decision 4,
     but it changes `runModesInclude`, which **every** run control depends on —
     the largest refactor in the feature.
   - **(c)** Ship (a) now, keep (b) as a follow-up.

   I lean **(c)**, but this is a user-facing micro-decision with real
   implementation cost either way, so it should not be picked unilaterally.
2. **Are presets UI-format only?** `DetectRunInputs` returns nothing for an
   api-format graph (`run_inputs.go:170`), so an api workflow has no editable
   parameters, no seed, and no meaningful preset. Proposal: tabs / Fork / Queue ×N
   render only for `wf.Format == store.WorkflowFormatUI`; an api workflow keeps
   today's single Run button unchanged. Confirm.
3. **Batch from the download-then-run path?** `startDownloadAndRun`
   (`internal/web/run_download.go`) and the substitute / option-fix endpoints all
   start runs. Proposal: those stay **single-run only** — they are recovery
   actions, and batching a recovery is not a thing a user asks for. Confirm.
4. **Cap values.** `maxBatchCount = 25`, `maxPresetsPerWorkflow = 12`, quick picks
   `2/4/8/16`. The reasoning is above; the numbers themselves are judgement.
   Confirm or set your own.
5. **Server restart mid-batch.** The batch is in-memory, so a restart silently
   loses the remainder (completed items are already captured). Accept that, or add
   a `run_batches` row so the UI can say "a batch of 8 was interrupted at 3"? A
   durable row costs crash-recovery semantics and can be left permanently
   "running" — which is why this design deliberately avoided it. Your call.
6. **"Open as preset" on the generation page.** Today "Re-run this" 409s outright
   when the graph drifted (`outputs_handlers.go:233-237`), which is a dead end.
   Loading the generation's params into a **new reconciled tab** turns that dead
   end into an editable starting point — and once P6 lands, it also restores the
   mode. Worth doing (P6), or out of scope?
7. **Should Save adopt a drifted graph silently?** The design has Save re-stamp
   `graph_hash`, which clears the banner — i.e. Save means "yes, this is what I
   want against the current graph". Should it instead require an explicit "Adopt
   current graph" confirmation while the banner is showing?

---

## Done when

- A UI-format workflow's run panel shows saved tabs that survive a page reload and
  a server restart.
- **Fork** duplicates the active tab; **Save** persists it; **Delete** removes it;
  switching tabs never loses typed text.
- **Queue ×N** runs N sequential ComfyUI prompts from one tab, each with a
  different seed, reporting "3 of 8", with **Stop** cancelling the remainder and
  leaving completed outputs intact.
- Opening a preset against a **changed** graph shows exactly which saved values
  were re-applied, which were dropped, and which parameters are new — and never
  silently applies a stale value to a retargeted slot.
- A batch's generations are grouped and labeled in the gallery rather than
  flooding the rail.
- Re-running a captured generation restores its **mode selection** (the
  `SESSION-HANDOFF.md:71` gap is closed).
