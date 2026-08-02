# Branch evaluation — `feat/comfy-model-cache` (`46a10ed`) is FULLY SUPERSEDED

**Verdict: port nothing. Retire the branch.** All seven claims in its commit
message landed on `main` independently between `1464cca` and `19372da`
(v0.1.102), and in every case where the two differ, **`main`'s version is the one
that fixed a bug this branch still contains**. Porting any of it would be a
regression wearing a feature's clothes.

Topology: forked from `1464cca` (an ancestor of `main`), **1 ahead / 62 behind**.
`git merge-tree --write-tree origin/main 46a10ed` exits 1 — the conflicts are in
`app.css`, `layout.go`, `workflow_pages.go`, `workflow_handlers.go`,
`workflow_resources.go` and two test files, i.e. precisely the sites `main`
rewrote. The conflicts are the supersession, not an obstacle to it.

## Claim-by-claim

| # | Commit-message claim | Status on `main` | Superseded by |
|---|---|---|---|
| 1 | migration `0019` comfy_model_cache | **identical blob** (`2b0894b`) | — |
| 2 | three-state chips ✓ / ◎ / ✗ | shipped | `70e4e04` |
| 3 | rich hover popover per chip | shipped | `70e4e04` |
| 4 | export `ChoicesContain` | solved differently | `9c22767` |
| 5 | remove maturity explainer copy | **deliberately kept** | — |
| 6 | consolidate track spacing | not on `main`; rationale gone | — |
| 7 | "Safe mode" preset | shipped, correctly | `3dac17e` |

Store layer confirmed byte-identical by blob OID, not by reading:
`internal/store/migrations/0019_comfy_model_cache.sql` → `2b0894b` and
`internal/store/comfy_cache.go` → `32d3771` on **both** refs.

## Why each drop is a drop, not a loss

**(2)+(3) chips and popover — `main`'s is strictly better, on two axes.**

*Performance.* The branch's `comfyResource` closure re-reads the blob from SQLite
and `json.Unmarshal`s the **entire** `ObjectInfo` **once per chip**. `main`
replaced exactly that shape with the per-request `comfyModelIndex` memo
(`internal/web/comfy_model_cache.go`). Measured on the operator's live database
**today**: the cached blob is **4,661,986 bytes** and there are **305 resource
entries across 52 workflows** — so the branch's shape is ~305 full decodes of a
4.7 MB payload to draw one page, the same class as the documented
"`store.ListQueue` on a render path" prohibition.

*Correctness.* The branch's popover reuses `.cm-updated` / `.cm-updated-pop`.
Those are **descendant** selectors, and chips render inside
`workflowResourcesPopover`, itself a `.cm-updated` — so hovering the outer
trigger opens **every** chip's popover at once, stacking panels over chips 2..N.
`main` introduced the child-combinator pair `.cm-res-chip-wrap` >
`.cm-res-chip-pop` for that reason, guarded by
`TestResourceChipPopoverDoesNotReuseTheSharedHoverMechanism` and
`TestResourceChipPopoverCSSIsScopedByAChildCombinator`. The branch also keeps a
`title=` on the hover unit, which `297ccd2` removed elsewhere because the native
tooltip races the popover.

The branch's popover additionally carries "View model" and "View on HF" **link**
rows. `main` deliberately emits no links: the chip *is* the link, and a duplicate
`href` destroyed the "exactly one href" guard in the provenance suite, while an
off-site HF `<a>` would reinstate the off-site link the chip rule suppresses.
Porting them back would fail `TestResourceChipHFProvenanceLink`.

**(4) `ChoicesContain` — the consumer it was exported for does not exist.** `main`
gave the web layer `comfy.ModelFileChoices` (build a basename set once) +
`comfy.HasModelFile` (lookup), which is what the memo needs. `choicesContain`
stays unexported with its one in-package caller in `modelSatisfied`. Exporting it
now would widen the API for zero consumers. (It would *not* trip the `deadcode`
gate — it still has a caller — so the argument is redundancy, not reachability.)

**(5) explainer copy — `main` keeps `.cm-maturity-note` on purpose.** It states
the two facts a content-gating control must not leave implicit: out-of-band
content is never fetched, and the user's own generations are unrated and always
render. Deleting it is a straight accessibility/comprehension loss.

**(6) track spacing `0.6rem` → `0.25rem` — the layout it made room for no longer
exists.** On the branch, Safe mode was appended *inside* the form directly under
the two tracks, so the tracks were tightened to fit. `main` puts Safe mode
*outside* the form in its own `.cm-maturity-preset` block, and
`.cm-mat-track:last-of-type { margin-bottom: 0 }` already collapses the gap
before the actions row. All that remains is the gap *between* the two tracks —
an unmotivated cosmetic change to a control redesigned and shipped across
v0.1.100–v0.1.102.

**(7) Safe mode — the branch's implementation is the one CLAUDE.md documents as
broken, twice over.** It does
`document.getElementById('cm-maturity-min-pg').click()` inside a literal
`javascript:void(function(){…})()` onclick. Both halves are known-bad:

- **It fails OPEN.** `maturityTrack` emits **no `<input>`** for an out-of-band
  stop and the max track's low bound is the **saved** `mr.Min`. From a saved band
  of `R..XXX` the `min-pg` radio exists but `max-pg13` does not, so "click both
  and submit" POSTs `min=pg&max=xxx` — a button labelled *Safe mode* that
  persists the **full** range. (Under `main`'s staging model it also commits
  nothing at all, since only Apply submits.)
- **The inline `javascript:` tripped site-wide XSS canaries.** This control is in
  the nav, so it is on every page; `3dac17e`'s message records the branch being
  red everywhere for it.

`main`'s `safeModeControl` is an htmx button **outside** the form with its own
CSRF-protected POST carrying the literal `min`/`max`, so `closest('form')` is
null and the payload is deterministic — validated by `handleSetMaturity`'s
existing `min > max` → 400. No radios driven, no script.

## Test coverage: `main` is a strict superset

Every branch test maps to an equivalent or stronger test on `main`, and `main`
adds five the branch has none of (`TestRunPopulatesTheComfyModelCache`,
`TestLibraryScanInvalidatesTheComfyModelCache`, `TestComfyModelIndexDecodesOnce`,
`TestComfyModelIndexIsLazy`, `TestCloudPanelPopulatesTheComfyModelCache`). The
branch's five `TestChoicesContain*` are covered by
`internal/comfy/model_choices_test.go`.

## The ◎ state is REACHABLE — checked the way static analysis cannot

This repo's signature defect is code that never runs, and `comfy_model_cache`
already shipped inert once (writers with zero non-test callers). Positive
evidence that `main`'s wiring works, read from a **copy** of the operator's real
database:

```
rows  blob_bytes  updated
   1     4661986  2026-08-02T19:53:10Z
```

One populated row, refreshed today — against the **0 rows across 71 workflows**
recorded before the fix. The feature this branch was opened for is live.

**Residue, unchanged by this evaluation:** there is still no manual "Refresh"
control, so `0019`'s header continues to name a population trigger the code does
not implement. The branch did not add one either.
