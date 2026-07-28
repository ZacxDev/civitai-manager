# Inert-picker option drift — design proposal

_Status (2026-07-27): **proposal, no production code changed.** Investigation grounded
in (a) ImpactPack Python + JS source on disk, (b) the live local ComfyUI v0.27 at
`http://127.0.0.1:8188` (`/object_info` + a real `/prompt` validation probe), and (c)
workflow 587 (`Basic_V37`) from the dogfood DB. Every claim below marked "verified" was
exercised against real ComfyUI; claims marked "source" come from reading ImpactPack's
code._

---

## 0. TL;DR

The recurring re-prompt on workflow 587 is **not** a policy question about remembering
the user's fix. It is a **converter-faithfulness bug**. ComfyUI's own frontend
**normalizes this drifted picker away at serialization time** — its `serializeValue`
override always emits the current valid value, so 587 "just runs" when you load it in
ComfyUI and hit Queue. civitai-manager's Go converter is a reimplementation of
ComfyUI's `graphToPrompt` that does **not** run the custom-node JS extensions, so it
faithfully serializes the *raw drifted* widget value where the real frontend would have
substituted a valid one. Preflight then correctly flags that raw value as a `BadOption`
— but ComfyUI would never have seen it.

**Recommendation:** mirror ComfyUI's normalization inside the converter
(`convert.go`), for **single-choice combos** (zero curation, zero new state, provably
loss-free) plus a **small curated inert-picker map** for the ImpactPack
`Select to add LoRA/Wildcard` family (multi-choice but inert). No persistence, no graph
mutation, no new invariant broken. Options 2 (persist fixes) and 3 (mutate stored graph)
are kept in reserve for a *different* class — genuine drift of an output-affecting combo
— which 587 is not.

---

## 1. The KEY question, answered with evidence

> When you LOAD 587 in ComfyUI's own frontend and hit Queue, what happens to the
> drifted "Select to add Wildcard" picker?

**Answer: ComfyUI's frontend silently normalizes it to the current valid value; 587
runs with no prompt and no error.** Three independent pieces of evidence, two of them
verified against reality:

### 1a. The value is inert at execution time (source)

`comfyui-impact-pack/modules/impact/pipe.py` — `EditDetailerPipe.doit(self, *args,
**kwargs)` reads `wildcard`, `model`, `clip`, `vae`, `positive`, `negative`,
`bbox_detector`, `sam_model`, … but **never** references `kwargs['Select to add
Wildcard']` or `kwargs['Select to add LoRA']`. Same for `ImpactWildcardProcessor`.
These two inputs are pure frontend UI helpers — the Python node ignores their value
entirely. Changing them cannot change any output. (Verified by reading the node bodies:
`pipe.py:348-413`, and the INPUT_TYPES at `pipe.py:326-327`, `impact_pack.py:2388`.)

### 1b. ComfyUI's frontend force-normalizes them on serialize (source — the mechanism)

`comfyui-impact-pack/js/impact-pack.js:712-801` registers a `beforeRegisterNodeDef`
extension for `ImpactWildcardEncode`, `ImpactWildcardProcessor`, `ToDetailerPipe[SDXL]`,
`EditDetailerPipe[SDXL]`, `BasicPipeToDetailerPipe[SDXL]`. For the "Select to add
Wildcard" combo (and the LoRA one) it:

```js
Object.defineProperty(node.widgets[combo_id+1], "value", {
    set: (value) => { if (value !== "Select the Wildcard to add to the text")
                          node._wildcard_value = value; },
    get: () => { return "Select the Wildcard to add to the text"; }   // getter is a CONSTANT
});
...
// Preventing validation errors from occurring in any situation.
node.widgets[combo_id+1].serializeValue = () => { return "Select the Wildcard to add to the text"; }
```

`graphToPrompt()` (ComfyUI's UI→API serializer) calls each widget's `serializeValue()`.
This override makes the picker **always** serialize as the current placeholder
`"Select the Wildcard to add to the text"` — *regardless of what value was loaded from
the saved graph*. The literal code comment is **"Preventing validation errors from
occurring in any situation."** This is exactly the drift-immunity that civitai-manager's
converter lacks: it does not execute this JS, so it emits the stored
`"Select Wildcard 🟢 Full Cache"` verbatim.

### 1c. The backend rejects the raw value and accepts the normalized one (VERIFIED, live)

Throwaway `/prompt` probe against the real ComfyUI (`ImpactWildcardProcessor` →
`Textbox`, no models/images):

| Submitted "Select to add Wildcard" value | ComfyUI `/prompt` response |
|---|---|
| `Select Wildcard 🟢 Full Cache` (the drifted stored value) | **rejected** — `value_not_in_list`: `'Select Wildcard 🟢 Full Cache' not in ['Select the Wildcard to add to the text']` |
| `Select the Wildcard to add to the text` (what the frontend serializes) | **accepted** — `prompt_id` returned, queued |

So the only reason 587 runs in ComfyUI is that its frontend never sends the drifted
value. civitai-manager sends it, hits the exact `value_not_in_list`, and surfaces it as
`BadOption` — every run, because option-fixes are ephemeral by design.

**Verdict: converter-faithfulness fix, NOT a persistence/remember design.** Persisting
the user's pick would paper over a converter that is simply not mirroring
`graphToPrompt`.

---

## 2. The problem class — is this a one-off or a pattern?

It is a **class**, not a one-off. Two overlapping sub-classes:

### Class A — single-choice combos (structural, no curation)

The installed `EditDetailerPipe` / `ImpactWildcardProcessor` "Select to add Wildcard"
input is a combo with **exactly one** valid choice (verified via `/object_info`:
`['Select the Wildcard to add to the text']`). For a single-choice combo there is, by
definition, **no decision for the user to make** — the only valid value is the one
choice. Any drifted value can only be normalized to it. This is detectable
generically: `spec.IsCombo && len(spec.Choices) == 1`.

### Class B — inert "action pickers" that drift even when multi-choice

ImpactPack ships a family of **frontend-only action pickers** — a combo you pick from to
*append text to another field*, whose own value is never read by Python and is
force-normalized by `serializeValue`:

- `Select to add Wildcard` — on `ImpactWildcardEncode`, `ImpactWildcardProcessor`,
  `ToDetailerPipe`, `ToDetailerPipeSDXL`, `EditDetailerPipe`, `EditDetailerPipeSDXL`,
  `BasicPipeToDetailerPipe`, `BasicPipeToDetailerPipeSDXL`. (Single-choice today, so also
  caught by Class A.)
- `Select to add LoRA` — same node set. **Multi-choice** (312 choices on this box — the
  installed LoRA list). Its placeholder `"Select the LoRA to add to the text"` is
  `Choices[0]`. In 587 it currently holds the valid placeholder, so it is not drifting
  *today* — but if ImpactPack renamed that placeholder (exactly the kind of change that
  drifted the Wildcard one), Class A would NOT catch it, because it has 312 choices.

The broader ecosystem has more `serializeValue`-normalized UI-only widgets (rgthree
seed/label helpers, various "refresh"/"select to add" combos). We did not enumerate them
exhaustively; the point is that "a combo whose frontend overrides `serializeValue` to a
constant" is a recognized pattern, and civitai-manager will keep hitting it one custom
node at a time until it mirrors the behavior structurally.

**In 587 specifically:** 6 nodes carry the drift — `EditDetailerPipe` ×4 (ids 12,13,14,15)
and `ImpactWildcardProcessor` ×2 (ids 3,4), all with `Select to add Wildcard =
"Select Wildcard 🟢 Full Cache"`. Preflight groups them into ONE `BadOption`, but it is
still one forced pick per run.

---

## 3. Solution options with tradeoffs

### Option 1 — Mirror-ComfyUI normalization in the converter (RECOMMENDED, see §4)

When the converter emits a combo widget value that is **not** a valid choice, normalize
it to the valid value that ComfyUI's frontend would have serialized, when we can do so
without a real decision:

- **1a (structural): single-choice combo** — `len(Choices)==1` → emit `Choices[0]`.
  Provably loss-free; there is no other valid value.
- **1b (curated): known inert action-picker** — `(classType, inputName)` in a small map
  of ImpactPack's `Select to add {LoRA,Wildcard}` family → emit `Choices[0]` (the
  placeholder is always the first entry). Covers the multi-choice inert case.

- **+** Cleanest possible: mirrors ComfyUI 1:1, so the converted graph is byte-faithful
  to what the real frontend produces. No new state, no stored-graph mutation, no new
  invariant. Fixes the whole class, not just 587. Preflight needs no change — it simply
  never sees a bad value.
- **−** 1b needs a curated map (small maintenance surface). 1a's "single choice" is a
  heuristic *in general* — but it is exactly ComfyUI's own reality: a one-element combo
  has one legal value.
- **Tension with "surface, nothing silent":** that principle was stated before the
  repeated-prompt friction was felt, and it targets **output-affecting** choices. An
  inert picker (§1a) and a single-choice combo (no alternative) have **no output effect
  and no decision** — normalizing them is *more* faithful to ComfyUI, not a silent
  behavior change. We still surface genuine, output-affecting drift (see §3.5).

### Option 2 — Persist applied option-fixes per workflow

A `(workflowID, nodeClass, inputName, oldValue) → newValue` override table; auto-apply on
future runs without re-prompting. Does not mutate the stored graph.

- **+** Honors "surface once, then remember" for *any* drift, including genuine
  multi-choice output-affecting combos.
- **−** New persistent state + a migration. Overrides go stale when choices change again
  (needs invalidation: re-validate the stored `newValue` against live `/object_info`
  each run, drop if now invalid). For 587 it is **overkill** — it remembers a "choice"
  that had exactly one option and no output effect. Wrong tool for this class; right tool
  for §3.5's residual class.

### Option 3 — Opt-in "fix permanently" (rewrite stored graph)

Rewrite the drifted value to the valid one in the stored workflow graph.

- **+** Simple mental model; the workflow is the user's own.
- **−** Reverses the deliberate **never-mutate-the-stored-graph** invariant. Destroys the
  original bytes (no undo). If the user later installs a different ImpactPack version, a
  hard-baked value could itself drift. Even opt-in, it is the highest-blast-radius option
  for the least reason here. Not recommended for this class.

### Option 4 — Curated inert-picker recognition (this is Option 1b, standalone)

A `(classType, inputName)` map of frontend-only pickers that are always safe to
normalize/drop. On its own it does not catch single-choice drift on *non*-curated nodes;
combined with 1a it does. Recommended **as part of** Option 1, not instead of it.

### 3.5 The residual class Options 1 does NOT cover (be honest)

Option 1 deliberately normalizes **only** where there is no decision (single choice) or
no output effect (curated inert). It must **NOT** auto-normalize a *genuine*
output-affecting combo that drifted to one of several real values — e.g. a `sampler_name`
that changed from `dpmpp_2m` to something no longer offered, or a multi-choice
`scheduler`. Those keep surfacing as `BadOption` (correct — the user must choose). If the
repeated-prompt friction is later felt *there* too, that is the moment for Option 2
(persist the pick). 587 is not that case.

---

## 4. Recommendation + how it hooks in

**Adopt Option 1 = 1a (single-choice, structural) + 1b (curated inert-picker map).**
It eliminates the 587 friction with the least new state and risk, stays faithful to
ComfyUI (it literally mirrors `serializeValue`), generalizes to the class, and honors
"don't silently change output" (neither sub-case can change output).

### Detection / normalization point

The normalization belongs in the **converter**, not preflight — because ComfyUI's
equivalent (`serializeValue`) runs at UI→API serialization. Doing it here means the
API graph submitted to *both* the local ComfyUI **and** CivitAI cloud carries the
correct value, and preflight/`detectBadOptions` then naturally finds nothing to flag.

Hook: `internal/comfy/convert.go`, `buildInputs`, the widget-emit line
(`convert.go:435-436`):

```go
if !linked[name] {
    inputs[name] = wv[cursor]        // <-- current: raw stored value
}
```

Wrap the emitted value in a normalizer that has `spec` (it does — `spec` is already in
scope from `lookupSpec` at `convert.go:418`) and `n.Type`:

```go
if !linked[name] {
    inputs[name] = c.normalizeComboWidget(n.Type, name, spec, wv[cursor])
}
```

`normalizeComboWidget(classType, inputName, spec, raw)`:

1. If `!spec.IsCombo` or `len(spec.Choices)==0` → return `raw` unchanged.
2. Decode `raw` as a scalar string (reuse `scalarComboValue` from `preflight.go:214`);
   if it is a link/array/non-string → return `raw`.
3. If the value is already a valid choice (`choicesContainValue`,
   `preflight.go:201`) → return `raw`.
4. **1a:** if `len(spec.Choices)==1` → emit `Choices[0]`; `c.warnf(...)` a normalization
   note (the converter already collects `warnings []string`, so this is *surfaced*, not
   silent — it just isn't *blocking*).
5. **1b:** else if `(classType,inputName)` ∈ `inertActionPickers` → emit `Choices[0]`
   (the placeholder is always index 0 for this family) + `c.warnf(...)`.
6. Otherwise → return `raw` unchanged (genuine multi-choice drift stays a `BadOption`,
   §3.5).

`inertActionPickers` is a tiny package-level set:

```go
var inertActionPickers = map[[2]string]bool{
    {"EditDetailerPipe", "Select to add Wildcard"}: true,
    {"EditDetailerPipe", "Select to add LoRA"}:     true,
    {"EditDetailerPipeSDXL", "Select to add Wildcard"}: true,
    // … ImpactWildcardProcessor/Encode, ToDetailerPipe[SDXL], BasicPipeToDetailerPipe[SDXL]
}
```

Reuse existing helpers so there is no new comparison logic: `scalarComboValue`,
`choicesContainValue` (both already in `preflight.go`).

### Blast radius / risk

- **Small and contained.** One new helper on the converter, called at one existing
  emit site. Everything else (preflight, cloud submit, options apply) is downstream and
  unchanged — they just stop seeing the bad value.
- **Reversibility:** fully reversible — it only changes an *emitted* value, never the
  stored graph (invariant preserved). No migration, no schema, no persisted state.
- **Faithfulness risk:** essentially nil for 1a (a one-element combo has one legal
  value) and for 1b (the value is provably inert per §1a/§1b). The only behavior change
  is that graphs that used to error now run — which is exactly what ComfyUI already does.
- **What it must not do:** must not touch multi-choice, output-affecting combos (§3.5) —
  step 6 guards this. Add a converter test that a drifted 2-choice non-inert combo is
  left untouched and still surfaces as `BadOption`.
- **Verification bar:** rebuild, then run the *actual* 587 through the converter and
  submit to the live ComfyUI `/prompt` — assert it is accepted (no `value_not_in_list`)
  with the normalized value, and that a synthetic genuine-drift combo still surfaces.
  (The §1c probe is the template.)

### Test coverage to add

- `convert_test.go`: single-choice combo with a drifted value → normalized to
  `Choices[0]`; a warning is emitted.
- `convert_test.go`: `EditDetailerPipe` "Select to add Wildcard" drift (the 587 case) →
  normalized; `ImpactWildcardProcessor` too.
- `convert_test.go`: multi-choice **non-inert** combo drift → left unchanged (still a
  `BadOption` downstream).
- `convert_test.go`: `Select to add LoRA` (multi-choice, curated inert) with a drifted
  placeholder → normalized to `Choices[0]`.
- A converter→preflight integration assertion: after conversion, 587 preflight has
  **zero** `BadOptions`.

---

## 5. Open questions for the user

1. **Surfacing the normalization.** Recommendation emits a converter **warning**
   ("normalized inert picker `Select to add Wildcard` → `Select the Wildcard to add to
   the text`") rather than a blocking `BadOption`. Is a non-blocking warning the right
   level of "not silent," or do you want it invisible (no warning at all, since ComfyUI
   itself shows nothing)?
2. **Curated map maintenance.** OK to hard-code the ImpactPack `Select to add
   {LoRA,Wildcard}` family in-repo (Option 1b)? It is stable (unchanged across many
   ImpactPack versions) but is a small maintenance surface. Alternative: rely on Class A
   (single-choice) only and accept that a *multi-choice* inert picker drifting is still a
   one-time `BadOption` until curated.
3. **Scope of §3.5 (genuine drift).** Do you want Option 2 (persist per-workflow fixes)
   built *now* for genuine output-affecting multi-choice drift, or deferred until that
   friction is actually felt? (Recommendation: defer — 587 does not need it, and it is
   the higher-state, higher-risk path.)
4. **Should normalization also apply to the local-run path only, or local + cloud?**
   Recommendation: both (it lives in the shared converter), since cloud submit hits the
   same `value_not_in_list`. Confirm you want cloud graphs normalized too.

---

## Appendix — reproduction facts

- Workflow: dogfood DB `~/.config/civitai-manager/civitai-manager.db`, `workflows.id=587`
  (`Basic_V37`), `graph` column (UI format: `nodes`/`links`/`groups`). 6 drifting nodes:
  `EditDetailerPipe` ids 12–15, `ImpactWildcardProcessor` ids 3–4; all
  `Select to add Wildcard = "Select Wildcard 🟢 Full Cache"`.
- Live schema: `GET http://127.0.0.1:8188/object_info/EditDetailerPipe` →
  `Select to add Wildcard` is a 1-choice combo `["Select the Wildcard to add to the
  text"]`; `Select to add LoRA` is a 312-choice combo whose `[0]` is the placeholder.
- ImpactPack source on disk:
  `…/custom_nodes/comfyui-impact-pack/modules/impact/pipe.py` (inert `doit`),
  `…/js/impact-pack.js:712-801` (`serializeValue` override — the normalization mechanism).
- Converter: `internal/comfy/convert.go` `buildInputs` (emit site `:435-436`);
  preflight `internal/comfy/preflight.go` `detectBadOptions` (`:123`), helpers
  `scalarComboValue` (`:214`), `choicesContainValue` (`:201`).
