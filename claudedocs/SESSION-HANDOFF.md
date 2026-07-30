# civitai-manager — session handoff (2026-07-30)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.80**, `main` clean & synced. **GOPRIVATE is NOT needed**. Standing OK to push+tag+release without asking — run the real gate (`go build ./... && go vet ./... && go test ./... && go test -race ./internal/web/... ./internal/comfy/... ./internal/store/... && gofmt -l ./internal/ ./e2e/ ./*.go`; **`gofmt` is NOT covered by `go vet`**, and **`./cmd/` does not exist**) + `/audit-pr` scaled to blast radius + **HTTP-level live-verify** against the `:8972` dogfood / real ComfyUI (`:8188`) / real CivitAI, then ship and verify the tarball. **Push `main` BEFORE tagging** — a tag push does not fast-forward-check (bit me on v0.1.80). No headless browser here, but **Brave works as `AUDITLOOP_CHROMIUM`** for the repo's own `e2e/uxaudit` harness — that is how the real axe runs were obtained. Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate → audit → **delta re-audit after every fix round** → ship → verify tarball → refresh `:8972` (4-step swap).

## Current state
- **Latest release: v0.1.80** (`main` @ `eec32ce`). Tarball verified (checksum + `--version` + 1 build attestation).
- `main` clean and synced. Dogfood **v0.1.80 on `:8972`** against the real DB; **migration 0014 confirmed applied** (`run_presets` table exists).
- Local **ComfyUI 0.27.1** at `:8188` with **ComfyUI-Manager V3.41**. `comfy-mtb` is installed there from the v0.1.78 end-to-end verification (remove via Manager's UI or `POST /manager/queue/uninstall` if unwanted).
- An untracked **`opencode.json`** sits in the repo root. Not mine; left alone.

## What shipped v0.1.78 → v0.1.80
- **v0.1.78 — custom-node resolution** (clawgate #74, now `complete`). Contra the ticket, we do NOT `git clone`+`pip` into the user's venv: ComfyUI-Manager is already installed with its API on loopback, so the install is **delegated** to it. Verified end-to-end against the real ComfyUI: attribution named `comfy-mtb` 0.5.4 → first Install click only OFFERED → second installed → the `installed(disk) − imported` diff reported "pending restart" → restart worked → re-run no longer reported the node missing. Design + every live measurement: `claudedocs/CUSTOM-NODE-RESOLUTION-DESIGN.md`.
- **v0.1.79 — real WCAG contrast fixes (dark only).** The ticket blamed dark muted text; **both halves were wrong** (dark `text-dimmed` is 5.39:1 and never failed). Real failures were brand/status colours used AS TEXT, worst 1.57:1. Root cause is structural: one token per intent doing two incompatible jobs (fill under white text vs foreground on body) — on dark those are *mathematically unsatisfiable together*. Fixed by splitting `--civitai-color-<intent>-text` from the fill. **Light deliberately untouched** (brand fidelity chosen over AA); its 25 remaining failures are **pinned as debt** — the checker fails if one starts passing or its ratio moves.
- **v0.1.80 — PR A + PR B + R1 presets + your uxaudit work.**
  - *PR A*: dropped the duplicate card-based browse element; period gains **Today/This year** (3- and 6-month are **impossible** — civitai's `period` is a strict enum and returns **400**; probed twice independently); import-button copy removed.
  - *PR B*: detail-page rework — icon stats (comments dropped), merged cards, animated version tabs, publish-date popovers, download card with expand-to-metadata, already-imported detection, empty community section hidden.
  - *R1 presets*: tabs, Fork, reconciliation, mode capture. **Mode capture closed the old "a re-run does not restore the mode selection" gap** — 0014 was that migration.

## 🔴 The preset chain — read before touching `internal/web/run_presets.go`
Five audit rounds, five real defects, each capable of silently producing wrong data
or losing work. Twice a fix's own stated justification was **false under a probe**.

1. `graph_hash` became a **false certificate** (entries replaced, old hash left). Fixed *structurally*: `store.UpdateRunPreset` takes params + hash as ONE write.
2. A **fieldless save wiped the preset** and stamped it with a valid hash — no banner.
3. **Partial capture** did the same via a likelier trigger, and *invisibly* (`NewInputs` only populates when the hash does not match).
4. `ModeSelection` had the identical defect one field over.

Root cause: a preset stores the **FULL** param set but a save captures only **what is
on screen**, which depends on the mode. Fixed by **merging, not replacing** — with
**adoption excluded**. For widgets that is because carried entries came from an older
graph; **for modes the reason is worse** — a stamped hash leaves only a STRUCTURE
check, and an inserted/reordered group keeps `"5:0"` valid while it names a
**different pipeline**.

**Accepted limits, recorded so they are not rediscovered as bugs:**
- A mode pick is **replaceable but not removable** by a save (no blank option renders once selected; `mode_key=""` is byte-identical to posting nothing). Deleting the tab is the only way to clear it.
- A tuple-identical **role swap** (two same-type nodes trading ids/titles) can pre-fill a value into the wrong field with nothing flagged. Unfixable short of link-graph analysis; the drift banner says so.

## Open threads / next steps
1. **PR C — the last of the UI feedback batch. NOT started, and it needs a decision.** Scope: library workflow-list reorg (primary CTA = Run, deep-link to the run section, resources popover, view-post button) and workflow-details reorg (a combined **Generate** section, params-form UX, referenced-resource chips, graph click-to-drag, hide raw JSON). It carries **both risky items**:
   - **🔴 Decision needed:** the CivitAI cloud connect form would accept a **token through the web UI**. `comfy_cloud` is config-file-only today. **Does the token write to the config file or the DB?** Either way it is a new secret-write path over HTTP.
   - **Native file-explorer button** = the server execs `xdg-open` on an HTTP request (approved: hard-gated — loopback + CSRF + containment to a configured library root + fixed opener allowlist; note it opens on the machine running `serve`).
   - **Graph click-to-drag** is the only item needing hand-rolled vendored JS (no CDN).
2. **R2 — batch queue ×N.** Designed in `claudedocs/RUN-PRESETS-AND-BATCH-DESIGN.md`, deliberately deferred. Its riskiest edit is splitting `applyRunOutcomeLocked` so per-item settle leaves `running == true` — get it wrong and two runs submit concurrently, nondeterministically. Cap agreed at **N ≤ 25**.
3. **v0.1.78 never got an independent adversarial audit** — four `/audit-pr` subagents died on API 500/529 and it shipped on my own review, which is weak since I wrote the spec the implementing agents followed. **`/code-review ultra` on `3571789`** closes it.
4. **Two non-brand light-theme AA failures left unfixed by decision**: `.cm-size-large` at **2.24:1** and muted-text-on-tint at **4.12:1**. Neither is brand-related; both are pinned as debt.
5. **CivitAI returns `items: []` for a keyword query + any narrow period** (measured at Day/Week/Month). Pre-existing upstream behaviour, but the period filter *looks* broken when combined with a search term. Consider surfacing "no results for this window — try a wider period".
6. **macOS still UNVERIFIED end-to-end.** The cask's `com.apple.quarantine` strip is evidenced *necessary*, never confirmed *sufficient*. Real fix = Developer ID + notarization + a stapled `.pkg`/`.dmg` ($99/yr; GoReleaser's `notarize.macos` is OSS and runs on Linux).
7. **Homebrew 6.0 tap-trust flag spelling unverified** — check `brew tap --help` on a real 6.x install.
8. **Launch drafts written, not posted** — `claudedocs/LAUNCH-CIVITAI-ARTICLE.md`, `LAUNCH-REDDIT-POST.md`. r/comfyui first. ⚠ r/StableDiffusion blacklists the literal string `nsfw` in title AND body.
9. **`devrc` changes are LIVE but still UNCOMMITTED** in `~/workspace/devrc`.
10. **Smaller residuals**: the adoption notice's "no parameter … matches" clause is loose now that the count can include a mode; an empty-capture write does not update the mode; a stored pick naming a vanished selector is sticky until Adopt. All assessed low and deliberately not fixed.

## Needs the user's eyes
- **PR C's token-storage decision** (thread 1) — blocking.
- **Thread 3** — the v0.1.78 audit gap.
- **Everything visual is markup/CSS-verified only.** Animations, hover, `<details>` motion and popover visibility are asserted as rules-present-and-wired, never observed. No browser DOM/JS dispatch has been exercised anywhere in this session.
- Clawgate **#77** (funnel UX) is still `open` — its axe half is now addressed by v0.1.79, but its persona/keyboard items were explicitly unverified screenshot inference and remain untouched.

## Cleanup / side effects
- `comfy-mtb` installed into the user's ComfyUI; ComfyUI restarted (queue was idle).
- Dogfood v0.1.80 running unattended on `:8972` against the real DB. `pkill -f "dogfood/cm serv[e]"` to stop.
- **v0.1.80 was tagged prematurely once** (tag pushed while the `main` push was rejected). Cancelled before publish, tag deleted remotely + locally, `origin/main` merged, re-gated, re-tagged in the right order. Nothing was ever public in the bad state. The lesson is now in `CLAUDE.md`'s release flow.
- Agent worktrees under `.claude/worktrees/` accumulate; `git worktree prune` if they get noisy.
