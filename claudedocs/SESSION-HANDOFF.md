# civitai-manager — session handoff (2026-07-30)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.83**, `main` @ `66ba652`, clean & synced, migrations at **0016**. **GOPRIVATE is NOT needed.** R2 (batch queue ×N) is **merged and shipped** — but its **gallery half is not**: `ListGenerationsByBatch` has no production caller and `GET /outputs/batch/{id}` does not exist, so batches are grouped in the DATA but nowhere in the UI. That is the natural next piece; don't read the design doc's gallery section as done. Standing OK to push+tag+release without asking — run the real gate (`go build ./... && go vet ./... && go test ./... && go test -race ./internal/web/... ./internal/comfy/... ./internal/store/... && gofmt -l ./internal/ ./e2e/ ./*.go`; **`gofmt` is NOT covered by `go vet`**, and **`./cmd/` does not exist**) + `/audit-pr` scaled to blast radius + **a delta re-audit after EVERY fix round** (a fix introduced the next finding six times), then **push `main` BEFORE tagging** (a tag push does NOT fast-forward-check — it tagged a commit missing upstream work on v0.1.80), verify the tarball, refresh the `:8972` dogfood (4-step swap: pkill → confirm port `000` → cp → start). If `go.mod` changes at all, re-run `nix build .` — `vendorHash` is not derived from anything the Go build reads and has shipped broken publicly once. **You CAN drive a real browser**: the `browser` skill drives my live Brave — `browser --instance work open <url>` → `activate` (not optional; a background tab is throttled and may never paint) → `screenshot`/`eval`, and hit-test with `document.elementFromPoint` to diagnose layout bugs. That is how the v0.1.82 popover bug was found; markup tests structurally cannot see paint order. Brave also works as `AUDITLOOP_CHROMIUM=/run/current-system/sw/bin/brave` for the repo's own `e2e/uxaudit` axe harness. ⚠ I hit a **monthly spend limit** last session and a subagent died mid-file — budget dispatches accordingly; `CLAUDE.md` has the recovery. Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate → audit → delta re-audit → ship → verify tarball → refresh dogfood.

## Current state
- **Latest release: v0.1.83** (`main` @ `66ba652`), clean and synced. Tarball verified — checksum + `--version` + 1 build attestation.
- **R2 (batch queue ×N) is MERGED and SHIPPED**, along with its six audit fixes. Migration **0016** (`0016_generation_batches.sql`) is the latest and is **confirmed applied to the real DB** (all three batch columns present).
- ⚠ **`nix build .` was re-verified for v0.1.83** because `go.mod` changed (`golang.org/x/net` moved indirect → direct, same version, `go.sum` unchanged). `vendorHash` still matches. Do not skip that check when `go.mod` moves — and note a self-authored "(empty = unaffected)" echo prints unconditionally, so it is not a check.
- `main` clean, one worktree, one branch. **Repo tidied**: 30 agent worktrees removed, 91 merged branches deleted, `git gc` run — `.git` is 7.1 MB.
- Dogfood **v0.1.83 on `:8972`** against the real DB. Local **ComfyUI 0.27.1** at `:8188` with **ComfyUI-Manager V3.41**.
- Local **ComfyUI 0.27.1** at `:8188` with **ComfyUI-Manager V3.41**. `comfy-mtb` was installed there by the v0.1.78 verification (remove via Manager if unwanted).
- An untracked **`opencode.json`** sits in the repo root. Not mine; left alone. Decide whether to commit or gitignore it.

## What shipped v0.1.78 → v0.1.82
- **v0.1.78** — custom-node resolution (clawgate #74, closed). Delegates installs to ComfyUI-Manager rather than cloning/pip-ing ourselves. See `claudedocs/CUSTOM-NODE-RESOLUTION-DESIGN.md`.
- **v0.1.79** — real WCAG contrast fixes, **dark only**. The ticket blamed dark muted text; both halves were wrong. Root cause was structural: one token per intent doing two incompatible jobs. Light deliberately untouched (brand fidelity); its 25 remaining failures are **pinned as debt** so they cannot drift.
- **v0.1.80** — PR A (browse de-dup, Today/This year periods) + PR B (detail-page rework) + **R1 run presets** + uxaudit work.
- **v0.1.81** — C1 (library list + workflow details + Generate section) + C2-A (HF provenance + source links + open-folder) + C2-B (cloud toggle) + missing-models recovery.
- **v0.1.82** — the popover stacking fix (below).

**The entire UI feedback list from this session is shipped.** Nothing outstanding from it.

## 🔴 Two hard-won areas — read before touching

**`internal/web/run_presets.go` — five audit rounds, five real defects.** Root cause: a preset stores the FULL param set but a save captures only what is ON SCREEN, which depends on the mode. Fixed by **merging, not replacing**, with **adoption excluded** (for modes the reason is worse than for widgets: a stamped hash leaves only a STRUCTURE check, and an inserted group keeps `"5:0"` valid while naming a DIFFERENT pipeline). Accepted limits, recorded so they are not "fixed" by someone unaware: a mode pick is **replaceable but not removable** by a save; a tuple-identical **role swap** can pre-fill the wrong field (unfixable short of link-graph analysis — the drift banner says so).

**`.cm-lift` + popovers (v0.1.82).** `.cm-lift` sets `transform` on `:hover`/`:focus-within`, creating a **stacking context**, so any popover inside it cannot out-z-index the next card's escaping absolutely-positioned descendants (`.cm-carousel-wrap` is `position:relative; z-index:auto`, so its children rise at their own z — reveal overlay 10, caption bar 20). The fix raises the CARD to **z-index 25** — above in-card decoration (20), below the sticky nav (30). **Do not raise it higher**; 60 would paint over the nav, rail scrim (44/45) and lightbox (50). `position: relative` on the base `.cm-lift` is load-bearing: `z-index` has no effect on a `position: static` box. The rule keys on `.cm-updated`/`.cm-vstatus` so it covers every popover using that mechanism, and includes `.cm-pop-open` because the JS controller holds it ~200 ms after the pointer leaves.

## Open threads / next steps
1. 🔴 **The batch GALLERY page is not wired — R2's data half shipped, its view half did not.** `ListGenerationsByBatch` exists, is tested, and now returns real rows (migration 0016's columns are populated as of v0.1.83) — but it has **no production caller**: `GET /outputs/batch/{id}` does not exist. So the design's "a batch's generations are grouped and labeled in the gallery" is **possible, not delivered**. This is the natural next piece and it is small.
   ⚠ Do NOT read the R2 design doc's gallery section as done.
2. **Queue ×N has never been live-verified against a real ComfyUI.** Everything is HTTP-level (`httptest` through the real handler) plus real-SQLite assertions. Batch runs **actually submit prompts**, so verify against a **throwaway temp DB** and be ready to Stop — never the real DB, and never a 25-item batch as a first test.
3. **Two of R2's audit findings are worth re-reading before touching that code** (`CLAUDE.md` has the general lessons; these are the specifics):
   - Batch attribution was set on `runOptions` and written by `InsertGeneration`, but `captureGeneration` never bridged them — migration 0016 was **dead**, every row NULL. It stayed green because `TestBatchCaptureRecordsBatchIdentity` **stubs `captureFn`** and never runs the broken function. A stub sitting exactly over the broken seam is the shape to watch for.
   - Seed keys must come from the **mode-applied** graph. Reading `wf.Graph` instead randomises a **bypassed** pipeline's seed and leaves the selected one fixed — a silently identical N-item batch, on exactly the multi-mode template packs that are the norm. Now pinned by `TestQueueSeedKeysComeFromTheModeAppliedGraph`.
4. **Nothing visual is unverified as of v0.1.82.** A live-browser sweep checked the library list, resource chips + folder button, the `:target` deep-link, the Generate section, the cloud toggle, and PR B's version tabs on the real 31-version model 4384. All passed; the one bug found became v0.1.82.
3. **Two non-brand light-theme AA failures remain by decision**: `.cm-size-large` 2.24:1 and muted-text-on-tint 4.12:1. Pinned as debt.
4. **CivitAI returns `items: []` for a keyword query + any narrow period** (Day/Week/Month). Pre-existing upstream; the period filter looks broken when combined with a search term. Consider "no results for this window — try a wider period".
5. **macOS still UNVERIFIED end-to-end.** The cask's quarantine strip is evidenced *necessary*, never confirmed *sufficient*. Real fix = Developer ID + notarization + a stapled `.pkg`/`.dmg`.
6. **Homebrew 6.0 tap-trust flag spelling unverified.**
7. **Launch drafts written, not posted** — `LAUNCH-CIVITAI-ARTICLE.md`, `LAUNCH-REDDIT-POST.md`. r/comfyui first. ⚠ r/StableDiffusion blacklists the literal string `nsfw` in title AND body.
8. **`devrc` changes are LIVE but still UNCOMMITTED** in `~/workspace/devrc`.
9. **Smaller residuals**: the adoption notice's "no parameter … matches" clause is loose now the count can include a mode; an empty-capture write does not update the mode; a stored pick naming a vanished selector is sticky until Adopt; the reveal handler has a TOCTOU window between its final `stat` and the opener's own path resolution (needs fd-based APIs Go does not expose portably).
10. **Deliberately dropped:** the v0.1.78 independent-audit gap. The user chose to skip it.

## Needs the user's eyes
- **Nothing blocking, but R2 is now a merge decision rather than a build decision** — it is written and gate-green, and the gate is the audit verdict, not more design. The design's seven questions remain resolved in its "✅ RESOLVED" section; no new decisions were needed to build it.
- Clawgate **#77** (funnel UX) is still `open`; its axe half is addressed by v0.1.79, its persona/keyboard half was explicitly unverified screenshot inference and remains untouched.
- The untracked `opencode.json`.

## Cleanup / side effects
- ⚠ **A subagent was cut off mid-file by a monthly spend limit**, leaving a non-compiling tree plus uncommitted files (since recovered). **Further dispatches may fail the same way** — budget for it, and use the mechanical recovery order in `CLAUDE.md` rather than assuming a partial tree is broken throughout.
- **R2 worktrees are still on disk**: `.claude/worktrees/agent-a1ab36937d67dfaf3` (`feat/batch-queue-r2`) and a detached-HEAD audit worktree at `2a7803f` under the scratchpad. Leave both until the audit reports and R2 merges; `git worktree prune` afterwards.
- Dogfood v0.1.82 running unattended on `:8972` against the real DB. `pkill -f "dogfood/cm serv[e]"` to stop.
- `comfy-mtb` installed into the user's ComfyUI; ComfyUI restarted once (queue was idle).
- **v0.1.80 was tagged prematurely once** (tag pushed while the `main` push was rejected). Cancelled before publish, tag deleted, `origin/main` merged, re-tagged correctly. The lesson is in `CLAUDE.md`'s release flow.
- Browser verification used its own tab on the `work` profile and closed it; the user's two profile sessions were untouched.
