# civitai-manager — session handoff (2026-07-30)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.85**, `main` @ `a3ffaf7`, clean & synced, migrations at **0016**, ONE worktree and ONE branch. **GOPRIVATE is NOT needed.** **R2 is now COMPLETE** — the batch gallery page, the `Batch i/N` tile caption link, AND the recent-outputs rail collapse all shipped; nothing from the design's gallery section is outstanding. Queue ×N is also **live-verified against real ComfyUI** (single-mode only — see thread #1). Standing OK to push+tag+release without asking — run the real gate (`go build ./... && go vet ./... && go test ./... && go test -race ./internal/web/... ./internal/comfy/... ./internal/store/... && gofmt -l ./internal/ ./e2e/ ./*.go`; **`gofmt` is NOT covered by `go vet`**, and **`./cmd/` does not exist**) + `/audit-pr` scaled to blast radius + **a delta re-audit after EVERY fix round** (this session that round found two more real defects, one of them a fix that was written INERT), then **push `main` BEFORE tagging** (a tag push does NOT fast-forward-check), verify the tarball, refresh the `:8972` dogfood (4-step swap: kill by PID → confirm port `000` → cp → start; **`pkill -f` matches its own shell** and killed my command once). If `go.mod` changes at all, re-run `nix build .`. **You CAN drive a real browser**: the `browser` skill drives my live Brave — `browser --instance work open <url>` → `activate` → `screenshot`/`eval`, hit-test with `document.elementFromPoint`. That is how both v0.1.82's popover bug and v0.1.84's focus-ring regression were found; markup tests structurally cannot see paint order. Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate → audit → delta re-audit → ship → verify tarball → refresh dogfood.

## Current state
- **Latest release: v0.1.85** (`main` @ `a3ffaf7`), clean and synced. **Tarball verified** — `checksums.txt` OK, binary runs, reports `0.1.85 (commit a3ffaf7…)`, `gh attestation verify` on the TARBALL exits 0 (silently on this `gh` build — the exit code is the signal; verifying the *extracted binary* 404s by design, since attestation subjects come from `checksums.txt`).
- `go.mod`/`go.sum` **untouched** by both v0.1.84 and v0.1.85, so `vendorHash` needed no re-verification. Checked, not assumed.
- **Repo is tidy**: ONE worktree, ONE branch (`main`). Every agent worktree and merged branch from R2, the gallery and the rail work removed.
- Dogfood **v0.1.85 on `:8972`** against the real DB (binary at `…/dbce7753-…/scratchpad/dogfood/cm` — an OLD session's scratchpad; if that dir is ever GC'd, rebuild it somewhere durable). Smoke-checked after the swap: `/` and `/outputs` 200, unknown batch id 404, rail shows 5 tiles and **0 badges** — correct, since the real DB has no batches, so the rail is byte-unchanged for the user.
- Local **ComfyUI 0.27.1** at `:8188` with **ComfyUI-Manager V3.41**.
- An untracked **`opencode.json`** still sits in the repo root. Not mine; left alone.

## What shipped in v0.1.84 — R2's view half
`GET /outputs/batch/{id}`: the N generations of one batch in run order, with the
params card hoisted out of the grid and rendered ONCE. Plus a clickable
`Batch i/N` link in every `/outputs` tile caption — the only discovery path to the
page, which is why the two shipped together.

**Three defects the process caught that NO mechanical gate could see:**

1. **The tile restructure silently killed the keyboard focus ring on the
   PRE-EXISTING `/outputs` page.** Wiring a link into the caption required turning
   `generationTile` from an `<a>`-wrapping-everything into a `<div>` + a full-bleed
   `z-10` overlay anchor (a nested `<a>` is invalid HTML and dead to clicks). That
   put the ring — drawn at `outline-offset: +2px`, *outside* the border box — inside
   the tile's own `overflow-hidden` clip. Focused, `:focus-visible` true, outline
   computed, **nothing painted**. WCAG 2.4.7. Fixed by `.cm-tile-link:focus-visible
   { outline-offset: -3px }` in `app.css`.
2. **That fix was first written as `outline-offset: 2px`** — the exact value that
   causes the bug — under a long, correct comment arguing it must be negative. It
   passed build, vet, test, `-race` and `gofmt` cleanly, because **CSS is not
   compiled**. Caught only by reading the diff.
3. **The batch page stated something false**, twice. First draft: "Every run in
   this batch shares the parameters below — only the seed differs", sitting directly
   above run #1's per-run seed. The *fix* for that still said "…except the seed",
   which is also false — `Prompt id`, `Captured`, `Status` and `Images` are per-run
   too. Only the **delta re-audit** caught the second one.

## What shipped in v0.1.85 — the rail half, completing R2
A batch now collapses to ONE rail entry with a `×N` badge linking to the batch
page. Mechanism: `rail()` over-fetches `railFetchLimit` rows, `groupRailGenerations`
collapses them in memory, then clamps to `outputsRailLimit` **GROUPS**. No second
query, no per-group lookup.

🔴 **The defect worth remembering, because it shipped GREEN THROUGH A
MUTATION-VERIFIED TEST.** The first attempt set `railFetchLimit = outputsRailLimit
* 2 = 24`, but `maxBatchCount = 25`. One batch of size *s* in the window leaves
`1 + (window − s)` entries, so the rail **under-filled from batch size 14** — and
`batchQuickPicks` ships `{2,4,8,16}`, so **one click on "16" dropped the rail from
12 tiles to 9 on EVERY page**, persisting until 14 newer runs aged past it. A
25-batch rendered ONE entry badged `×24` while the page it linked to showed 25.
Measured sweep at 24: `1..13→12, 14→11, 16→9, 20→5, 24→1, 25→1`.

Why the test missed it: `TestRailOverFetchesSoBatchesCannotUnderFillIt` genuinely
pins the property (mutation-verified independently — the naive version fails with
`rail has 6 entries, want 12`), but its fixture is 12 batches × **2** rows =
exactly 12 collapsed-away rows, the boundary. **One more row and it would have
caught this.** Mutation-verification proves a test CAN fail; it says nothing about
whether the fixture reaches the interesting case. That is the lesson.

Fix: `railFetchLimit = outputsRailLimit + maxBatchCount - 1` — written as the
EXPRESSION so raising the batch cap cannot silently re-break it. Verified to give
12 entries at every batch size 1..25, and 36 < `recentGenerationsCap` (50).
**Honest limit, stated in the code:** this repairs the SINGLE-batch worst case
exactly, not the general one — two back-to-back 25-batches still collapse to 2, and
no fixed window can promise 12 groups without becoming the unbounded per-render
scan that cap exists to forbid.

## 🔴 Three hard-won areas — read before touching

**`internal/web/run_presets.go` — five audit rounds, five real defects.** A preset
stores the FULL param set but a save captures only what is ON SCREEN, which depends
on the mode. Fixed by **merging, not replacing**, with **adoption excluded**.
Accepted limits, recorded so they are not "fixed" by someone unaware: a mode pick is
**replaceable but not removable** by a save; a tuple-identical **role swap** can
pre-fill the wrong field (unfixable short of link-graph analysis).

**`.cm-lift` + popovers (v0.1.82).** `.cm-lift` sets `transform` on
`:hover`/`:focus-within`, creating a **stacking context**, so a popover inside it
cannot out-z-index the next card's escaping absolutely-positioned descendants. Fix
raises the CARD to **z-index 25** — above in-card decoration (20), below the sticky
nav (30). **Do not raise it higher.**

**`generationTile`'s layering (v0.1.84).** Three things are load-bearing and none
is obvious from the markup:
- The detail link is a **`z-10` full-bleed overlay anchor**, not a wrapper. It needs
  an explicit `aria-label` (no text content of its own).
- The caption bar is `z-20 pointer-events-none`; **only** the batch link carries
  `pointer-events-auto`. That is what lets the whole tile stay clickable while one
  sub-element diverts.
- `.cm-tile-link:focus-visible`'s **negative** `outline-offset` is the only reason
  the tile has a visible focus ring at all. It beats the base rule because
  `:where(a, …)` has zero specificity — verified by reading the computed value in a
  browser, not by reasoning. `TestTileFocusRingIsDrawnInward` pins the SIGN (not the
  value, so the ring can be re-tuned) and is mutation-verified.

## Open threads / next steps
1. 🔴 **Queue ×N is UNUSABLE on ~27% of the library, and the multi-mode seed risk
   is therefore UNTESTABLE today.** Measured across the user's 70 UI workflows:
   **51 expose a top-level `KSampler`/`KSamplerAdvanced` (seedable); 19 do NOT**, so
   Queue ×N offers "N identical runs" on those. Root cause is narrow and verified:
   `runInputLayouts` (`internal/comfy/run_inputs.go:102,110`) yields a
   `RunInputSeed` for **exactly those two node types** and nothing else. The 19 split
   into custom samplers (`SamplerCustomAdvanced`, `ClownsharKSampler_Beta`,
   `KSamplerSelect`, `DetailDaemonSamplerNode`) and samplers nested inside
   **subgraphs**, which `DetectRunInputs` deliberately stops at.
   ⚠ **This is a capability gap, not a bug** — the app detects the missing seed and
   OFFERS ("queue N identical runs anyway?") rather than silently producing N
   identical images. That is the "offer, don't perform" shape working correctly.
   **The consequence for testing:** all 3 genuinely multi-mode workflows in the
   library (581, 588, 589 — the only ones with an exclusive `toggleRestriction`;
   582–587 of pack 1386234 carry only `default` togglers and are correctly NOT
   multi-mode) expose **no editable seed at all**, for the raw AND every
   mode-applied variant. So `TestQueueSeedKeysComeFromTheModeAppliedGraph`'s
   near-miss cannot be corroborated with real images until seed detection reaches
   those samplers. All three are also un-runnable locally anyway (`MissingModels`
   non-empty for every mode; custom nodes ARE all installed).
   Fix shape if pursued: extend `runInputLayouts` to the common custom samplers
   and/or resolve seeds through subgraph interiors. Both have real design questions
   (which widget is the seed on `SamplerCustomAdvanced`? do you descend every
   subgraph?) — this is feature work, not a patch.
   ⚠ **`CLAUDE.md` says multi-mode template packs "are the norm."** In THIS library
   they are 3 of 70. That line is about the packs the mode-detection work was built
   against, not the library at large — read cold it primes you to expect multi-mode
   to be the common case, and it isn't.
2. ✅ **Queue ×N IS NOW LIVE-VERIFIED against real ComfyUI 0.27.1 — it works, no bugs
   found.** Three real batches (3, 5, and 6-stopped-at-1) on workflow 575
   `zz_illustrious_txt2img`, against a throwaway temp DB on `:8976`. What was
   confirmed, in the order it matters:
   - **Batch identity is really written** — shared `batch_id`, `batch_index` 1..N,
     `batch_total` = N as requested. Migration 0016 populated end-to-end for the
     first time (it was DEAD once; see the R2 note below).
   - **Seeds genuinely differ per item** — 3 distinct seeds across 3 items, and a
     **deliberately posted `seed=12345` was overridden**, proving `withFreshSeeds`
     actually fires rather than passing the form value through.
   - 🔴 **The strongest check: 3 DISTINCT image sha256s on disk.** The DB recording
     different seeds does not prove ComfyUI *used* them; different image bytes does.
   - Non-seed params byte-identical across items.
   - The batch page renders both shapes correctly with real rows: `3 runs.` for a
     complete batch and `1 of 6 runs captured — the batch was stopped or some runs
     failed.` for the stopped one.
   - **Stop works**: "1 of 6 completed, 5 cancelled", and ComfyUI's own queue was
     left EMPTY — the remainder is never submitted, not submitted-then-abandoned.
   ⚠ **What this does NOT cover: the multi-mode path.** Workflow 575 is
   SINGLE-mode (no rgthree Fast Groups Bypasser). The known near-miss — seed keys
   must come from the **mode-applied** graph, or a bypassed pipeline's seed gets
   randomised while the selected one stays fixed — can only manifest on a MULTI-MODE
   template pack. That specific risk is still unverified live; it is pinned only by
   `TestQueueSeedKeysComeFromTheModeAppliedGraph`. **Re-run this exercise against a
   multi-mode pack** (e.g. something from pack 1386234) and check the image hashes
   differ, which is the one instrument that would catch it.
   Practical note for a repeat: at 512×512/6 steps a 5-item batch finishes in **under
   9 seconds**, so a Stop test needs SLOW items — 1024×1024/40 steps gave a usable
   mid-batch window at ~14s.
3. **Two known-loose spots in the new code**, both judged not worth blocking on:
   - `TestBatchPageHostileIDsAre404NotError` proves the *predicate* rejects hostile
     ids, **not that the handler's guard is what does it**. Verified: neutering only
     the handler's `ValidBatchID` check leaves the whole web suite green, because
     `ListGenerationsByBatch` carries its own guard and returns zero rows → 404. Two
     guards, same predicate; the handler's is untested and there is no cheap seam
     (`s.store` is a concrete `*store.Store`, not an interface). The test comment
     says this plainly — keep it that way.
   - `batch_gallery_web_test.go`'s overlay assertion matches the exact class string,
     so a harmless class **reorder** fails it. The failure message names
     `cm-tile-link`, so a reader is well-guided; splitting into two independent
     `Contains` checks would lose nothing.
4. **`generationDetailPage`'s h1 has the same unbounded-untrusted-label shape** the
   batch h1 was just fixed for (workflow names are unbounded; 80 unbreakable chars
   at `text-2xl` ≈ 1150px, past a 390px viewport). Pre-existing, out of scope for
   v0.1.84, and covered by NO test.
4b. **Two 🟢 rail follow-ups deliberately kept out of v0.1.85** (audit-raised,
   scope-fenced): (a) a collapsed batch tile captions with `generationLabel` (the
   WORKFLOW name) when `PresetName` is captured on every row and reads far better —
   `×3 Hi-res 8-step` beats `×3 batched-wf`; (b) the rail link's accessible name is
   `"LABEL ×3 LABEL"` — the label duplication is pre-existing (alt + caption) and
   this change inserts `×3` into the middle of it. `h.Alt("")` on the thumb (it is
   decorative; the caption already names it) fixes the duplication.
4c. **A `truncate`-without-`min-w-0` hole is closed but narrow.** `ux_audit_web_test.go`
   now requires the pair, because bare `truncate` is only safe on a DIRECT flex item
   — on a descendant of a `flex-1` parent its `white-space: nowrap` makes the
   ancestor's min-content the whole string and the row blows out. A per-chunk class
   scan cannot tell those apart, so the pairing is the deterministic proxy. Do NOT
   relax it back to bare `truncate`.
5. **Two non-brand light-theme AA failures remain by decision**: `.cm-size-large`
   2.24:1 and muted-text-on-tint 4.12:1. Pinned as debt.
6. **CivitAI returns `items: []` for a keyword query + any narrow period**
   (Day/Week/Month). Pre-existing upstream. Consider "no results for this window —
   try a wider period".
7. **macOS still UNVERIFIED end-to-end.** The cask's quarantine strip is evidenced
   *necessary*, never confirmed *sufficient*. Real fix = Developer ID +
   notarization + a stapled `.pkg`/`.dmg`.
8. **Homebrew 6.0 tap-trust flag spelling unverified.**
9. **Launch drafts written, not posted** — `LAUNCH-CIVITAI-ARTICLE.md`,
   `LAUNCH-REDDIT-POST.md`. r/comfyui first. ⚠ r/StableDiffusion blacklists the
   literal string `nsfw` in title AND body.
10. **`devrc` changes are LIVE but still UNCOMMITTED** in `~/workspace/devrc`.
11. **Smaller residuals**: the adoption notice's "no parameter … matches" clause is
    loose now the count can include a mode; an empty-capture write does not update
    the mode; a stored pick naming a vanished selector is sticky until Adopt; the
    reveal handler has a TOCTOU window between its final `stat` and the opener's own
    path resolution.
12. **Deliberately dropped:** the v0.1.78 independent-audit gap.

## Needs the user's eyes
- **Nothing blocking.** v0.1.84 is shipped, released, tarball-verified and
  dogfooded.
- Clawgate **#77** (funnel UX) is still `open`; its axe half is addressed by
  v0.1.79, its persona/keyboard half was explicitly unverified screenshot inference
  and remains untouched.
- The untracked `opencode.json`.

## Cleanup / side effects
- ⚠ **A subagent hit a SESSION limit mid-file again** (a monthly limit did the same
  last session). Recovery was mechanical and the tree still COMPILED — the danger
  was not a broken tree but a **half-written fix that looked finished**. Use the
  order in `CLAUDE.md` and read the actual diff, not just the build result.
- **`pkill -f "<pattern>"` matched its own shell** and killed the command running
  it (exit 144). Kill background servers **by PID**.
- Browser verification drove the user's **live Brave** (`work` profile) in its own
  tab; the user's previous tab was re-activated and mine closed. The `work` profile
  had disconnected earlier in the session and came back on its own.
- All agent worktrees removed; `git worktree list` shows only the main checkout.
- Dogfood v0.1.85 running unattended on `:8972` against the real DB.
- **The rail's browser verification was done on the `work` profile by the lead**, not
  on the implementing agent's `personal`-profile pass — `personal` is the profile
  that holds the user's active window, and an earlier agent reported its extension as
  pre-CDP. Re-verifying on `work` also covered the post-fix 36-row path, which the
  agent flagged it had NOT re-screenshotted. Confirmed there: 12 tiles, `×25` badge
  legible on BOTH themes, `pointer-events: none` so the click reaches the tile link.
- ⚠ **`browser eval` returns raw JSON, so `| jq -r '.result.data.value'` fails** when
  the value is itself a JSON string — pipe to `tail` and read it, or the parse error
  reads like a broken bridge. Also: a tab can silently go `hidden` mid-session; the
  bridge now self-announces it. For a SERVER-RENDERED page a hidden-tab DOM read is
  still valid (nothing is JS-built) — only screenshots need `activate`.
- ⚠ **The Queue ×N live verification wrote 9 images into the user's real ComfyUI
  output dir** — `ComfyUI_01061_.png` … `ComfyUI_01069_.png`, 2026-07-30 16:16–16:20.
  That is unavoidable: the workflow's `SaveImage` node writes there, and the app
  COPIES from it. They were left in place (the user's own outputs directory is not
  mine to prune). The app-side temp DB, its outputs dir, and the scratch binary were
  all deleted; the real DB was never written to (still 5 generations, all batch
  columns NULL).
