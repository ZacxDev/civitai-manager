# civitai-manager — session handoff (2026-07-30)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.84**, `main` @ `5cfbf62`, clean & synced, migrations at **0016**, ONE worktree and ONE branch. **GOPRIVATE is NOT needed.** R2's batch gallery is now **fully shipped** — `GET /outputs/batch/{id}` + the `Batch i/N` tile caption link — **except the recent-outputs RAIL half**, which was deliberately scoped out and is thread #1 below. Standing OK to push+tag+release without asking — run the real gate (`go build ./... && go vet ./... && go test ./... && go test -race ./internal/web/... ./internal/comfy/... ./internal/store/... && gofmt -l ./internal/ ./e2e/ ./*.go`; **`gofmt` is NOT covered by `go vet`**, and **`./cmd/` does not exist**) + `/audit-pr` scaled to blast radius + **a delta re-audit after EVERY fix round** (this session that round found two more real defects, one of them a fix that was written INERT), then **push `main` BEFORE tagging** (a tag push does NOT fast-forward-check), verify the tarball, refresh the `:8972` dogfood (4-step swap: kill by PID → confirm port `000` → cp → start; **`pkill -f` matches its own shell** and killed my command once). If `go.mod` changes at all, re-run `nix build .`. **You CAN drive a real browser**: the `browser` skill drives my live Brave — `browser --instance work open <url>` → `activate` → `screenshot`/`eval`, hit-test with `document.elementFromPoint`. That is how both v0.1.82's popover bug and v0.1.84's focus-ring regression were found; markup tests structurally cannot see paint order. Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate → audit → delta re-audit → ship → verify tarball → refresh dogfood.

## Current state
- **Latest release: v0.1.84** (`main` @ `5cfbf62`), clean and synced. **Tarball verified** — `checksums.txt` OK, binary runs, reports `0.1.84 (commit 5cfbf62…)`; `gh attestation verify` on the tarball exits 0 (silently on this `gh` build — the exit code is the signal; verifying the *extracted binary* 404s by design, since the attestation subjects come from `checksums.txt`).
- `go.mod`/`go.sum` **untouched** by v0.1.84, so `vendorHash` was not re-verified and did not need to be. Checked, not assumed.
- **Repo is tidy**: ONE worktree, ONE branch (`main`). All four agent worktrees and seven merged branches from R2 + the gallery work removed.
- Dogfood **v0.1.84 on `:8972`** against the real DB (binary at `…/dbce7753-…/scratchpad/dogfood/cm` — an OLD session's scratchpad; if that dir is ever GC'd, rebuild it somewhere durable).
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
1. **The recent-outputs RAIL still does not group batches** — the one piece of the
   design's gallery section deliberately left out of v0.1.84 (scope decision). The
   design (`RUN-PRESETS-AND-BATCH-DESIGN.md:673-678`) asks for a batch to collapse
   to ONE tile with a `×N` badge, fetching `outputsRailLimit * 2` and clamping to 12
   groups, precisely because *"an 8-item batch would otherwise consume 8 of 12
   slots."* **This was confirmed live, not inferred**: a 3-item seeded batch takes
   three of the rail's slots in the browser. `outputs_rail.go` contains no `batch`
   reference. Higher blast radius than the page was — that query runs on EVERY page
   render.
2. **Queue ×N has STILL never been live-verified against a real ComfyUI.** Nothing
   this session changed that: the gallery verification seeded a temp DB directly and
   never submitted a prompt. Batch runs **actually submit**, so verify against a
   **throwaway temp DB**, be ready to Stop, and never start with a 25-item batch.
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
- Dogfood v0.1.84 running unattended on `:8972` against the real DB.
