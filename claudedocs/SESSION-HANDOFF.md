# civitai-manager — session handoff (2026-08-02)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.101**, `main` @ `d63b39f` with **22 UNRELEASED commits** including three user-facing fixes, **zero open PRs** (a `docs/session-learnings` PR may be in flight — check), migrations at **0019** on main and on my real DB. **GOPRIVATE is NOT needed.** 🔴 **First decision: cut v0.1.102, or work the queue and release once.** Then the queue, in this order because they collide otherwise: (1) the `canQueue` guard, (2) the 2 a11y bugs the expanded axe walk exposed, (3) `deadcode` in CI + delete the dead functions — LAST, because that list is STALE (`webScanTimeout` is live again). Standing OK to push+tag+release without asking — run the real gate (`go build ./... && go vet ./... && go test ./... && go test -race ./internal/web/... ./internal/comfy/... ./internal/store/... ./internal/diskusage/... && gofmt -l ./internal/ ./e2e/ ./*.go`; **`gofmt` is NOT covered by `go vet`**, **`./cmd/` does not exist**, and **`e2e/uxaudit` is a NESTED module** a root `go test ./...` never compiles — it has its own CI job) + `/audit-pr` scaled to blast radius + **a delta re-audit after EVERY fix round**, then **push `main` BEFORE tagging**, verify the tarball, refresh the `:8972` dogfood (kill by `pgrep -x cm` + `/proc/<pid>/exe`, wait until NO cm process remains — a free port is not a released binary — then verify the served build by pid + `--version`). If `go.mod` changes at all, re-run `nix build .`. **You CAN drive a real browser** — it found a real bug in EVERY visual branch across six sessions. 🔴 **The shared checkout is on ANOTHER session's branch (`feat/comfy-model-cache`) — do NOT switch or commit in it; use a worktree and run `git branch --show-current` immediately before any commit.** Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate → audit → delta re-audit → ship → verify tarball → refresh dogfood.

## Current state
- **Latest release: v0.1.101.** `main` @ `d63b39f` is **22 commits ahead** of it.
- **Dogfood on `:8972`** runs the RELEASED v0.1.101 binary from this session's scratchpad
  (`…/766dd9a1-…/scratchpad/dogfood/cm`). ⚠ That path dies with the session's temp dir —
  re-download the release tarball rather than trusting it.
- `go.mod`/`go.sum` **untouched across the entire v0.1.84 → v0.1.101 run**.
- **Zero open PRs** at time of writing, except possibly `docs/session-learnings`
  (a CLAUDE.md update dispatched at session end — check `gh pr list`).
- Real DB at **schema 19**; `comfy_model_cache` currently **0 rows** (see below — expected).
- ⚠ **THREE 23 MB backups** in `~/.config/civitai-manager/` (`.pre-0017`, `.pre-0018`,
  `.pre-comfy-cache`) ≈ 68 MB. The first two are long superseded; delete when happy.
- Untracked `opencode.json` in the repo root; not mine, left alone.

## Unreleased on `main` (22 commits) — three are user-facing
| PR | what |
|---|---|
| **#44** | the axe walk now covers the UI format users actually have (24 pages, 8 violations) |
| **#45** | `--web-scan-timeout` actually bounds the scan — **inert since v0.1.13, 89 releases** |
| **#46** | the node-pack install UI is reachable; ambiguous packs are ranked |
| #42 | three coverage gaps from the supersession audit |

## What shipped this session (v0.1.99 → v0.1.101)
**#32/#33/#34** axe walk expanded · breadcrumbs · duplicate copy cut ·
**#35** the dead axe harness + the two AA failures it found · **#36** maturity stages,
commits on Apply · **#38** v0.1.99 · **#39** the inverted-range regression #36 introduced,
plus a retraction · **#40** v0.1.100 · **#41** the reworked ComfyUI cache + three-state
chips (supersedes **#37**, closed) · **#42** guards · **#43** v0.1.101 ·
**#44/#45/#46** as above.

## 🔴 The session's central finding: code that never runs
Three instances found **by accident**, nested so each hid the one below — the app had
unreachable features, the harness that should have caught them was walking a branch no
user reaches, and CI never compiled that harness's module at all. A deliberate sweep then
found **17 more**. The full lesson and the instruments (`deadcode` under both GOOS
intersected; the one-directional `class_coverage` gap; the empty-table runtime signal)
belong in `CLAUDE.md` — read them there, not here.

**Still OPEN from the sweep — none of this is fixed:**
1. 🔴 **`canQueue` is guarded by nothing.** `handleWorkflowRunComfyStatus` in
   `run_handlers.go` is the only place deciding whether the ONE primary run control posts
   to `/run/queue` (batch) or `/run-with-params` (single). Mutating it to
   `canQueue = false` left **1189 tests passing, zero failures**. The count segment is
   rendered by `runZone` from a *different* `canQueue`, so the ×1/×2/×4/×8 picker stays
   fully visible and interactive while the button silently posts to the single-run
   endpoint — **pick ×8, get 1 run, no error, no notice.** Worst available failure shape.
   Fix ≈30 lines: GET the comfy-status fragment for a seeded UI-format workflow and assert
   `hx-post=".../run/queue"`; another for API-format asserting `/run-with-params`.
   Mutation-verify against exactly that line.
2. **11 functions with no production caller** — proved by deleting all of them and showing
   `go build ./...` still exits 0 (130 lines, 10 files). Two matter beyond tidiness:
   `safeHexColor` (`workflow_graph.go:54`) is a **dead sanitizer sitting beside the live
   one** (`authorHex`), a trap for the next reader; and `store.NodePackSourceRegistryClass`
   duplicates `cacheSourceRegistryPrefix` — one rule in two places.
   ⚠ **The list is STALE**: `Server.webScanTimeout` was on it and PR #45 revived it.
   Re-run the sweep before deleting anything.
3. **Orphan route** `GET /workflows/run/resolve-model` — registered, loopback-gated, makes
   outbound egress, and **nothing links to it**. ~8 tests exercise it, reading as coverage
   of the missing-model resolve feature; production renders
   `resolveModelFragmentWithReason` instead. Delete it, or document it as a debug endpoint.
4. **3 dead `.cm-*` CSS rules**: `.cm-crumb-name` (291), `.cm-vstatus-date` (1853),
   `.cm-gen-sep` (3442).
5. **`Server.attributeFn`** — a test seam nothing sets, in production or in any test.

## 🔴 Two REAL a11y bugs the expanded walk exposed (unfixed)
The axe number went **20 pages / 0 violations → 24 pages / 8 violations**. That increase
is the improvement: the old 0 was measuring a surface no user has. Both are
**pre-existing app bugs on UI-format-only surfaces** — confirmed by audit, because the PR
that exposed them touches zero `internal/web` files and both surfaces are UI-format-gated
in app code, so an API-only fixture structurally could not render them.

1. **`aria-required-children` — CRITICAL.** `internal/web/run_preset_pages.go:89-111`:
   `runPresetTabStrip` renders `<div role="tablist" aria-label="Run presets">` and
   unconditionally appends `runPresetForkButton`, a plain `<button>` with **no
   `role="tab"`** — while its sibling `runPresetTabButton` (`:116-119`) sets one. **Both**
   branches of the fork button (`:147-154` AtCap, `:155-163` normal) omit it. Fires for
   any UI-format workflow, with or without saved presets.
2. **`svg-img-alt` — SERIOUS.** `internal/web/workflow_graph.go:329`: the graph-preview
   `<svg>` carries `role="img"` with no `aria-label`/`<title>`; the label sits on the
   scroll *container*, not on the svg.

2 rules × 4 pages = the 8. Both small, both independent of everything else in the queue.

## Open investigations — live diagnosis state

### The ComfyUI model cache is EMPTY, and that is expected
`comfy_model_cache` has **0 rows**. It was cleared deliberately (see the mistake below)
and repopulates on the next local ComfyUI run or cloud-panel view — `cacheComfyObjectInfo`
is wired in `run_handlers.go` (realRun) and `cloud_handlers.go`, with invalidation after a
library scan. **Proven end-to-end on the real DB**: a **4,661,987-byte** row with an
RFC3339 `updated_at` (i.e. `nowRFC3339()`, so the app wrote it) appeared during
dogfooding. Until it repopulates the ◎ ("in ComfyUI") chip state cannot render and chips
correctly fall back to ✓/✗.
⚠ **Scan invalidation is a heuristic**, not a guarantee — a scan means *our* file set
changed, not ComfyUI's. So after a scan, ◎ chips revert to ✗ until the next run.

### `web_scan_timeout`: now live, and there is an upgrade hazard
Inert from **v0.1.13 to v0.1.101 — 89 releases**. Now enforced, default **6h** (the value
`scanJobBudget` already imposed, so unset behaviour is unchanged).
🔴 **`docs/configuration.md` shipped `web_scan_timeout: "2m"` inside the annotated sample
config** for that whole period, and no code path writes the key — so hand-copying the docs
was the ONLY way to hold an explicit value. Anyone who copied it now has an enforced 2m.
**And a deadline firing during the HASH phase persists ZERO rows** — hashing is phase 1,
`local_files` are written in phase 3; measured, a 150 ms deadline against a 12 × 200 MB
fixture saved nothing. For a large library that is a total loss, every time.
`docs/configuration.md` now carries the upgrade warning; a release note would be kind.

## Next steps (ranked)
1. **Decide: cut v0.1.102 now, or work the queue and release once.**
2. **The `canQueue` guard** — ~30 lines, certain, worst failure shape.
3. **The 2 a11y bugs** — small, independent.
4. **`deadcode` in CI + delete the dead functions — LAST**, and re-run the sweep first
   because the list is stale. `go run golang.org/x/tools/cmd/deadcode -filter
   civitai-manager ./...` under both `GOOS=linux` and `GOOS=windows`, **intersected**
   (the intersection is what excludes `internal/diskusage`'s per-GOOS helpers), with an
   allowlist for the accepted test shims. ~15s per CI run.
5. **A route-emitter check and a reverse class-coverage test** — the two systematic guards
   the sweep recommended. Route-emitter: diff registered `mux.Handle*` paths against
   emitted `hx-*`/`href` literals with an allowlist (~60 lines) — catches orphan routes.
   Reverse class-coverage (~30 lines) — catches CSS rules nothing emits.
6. **An app-wide `htmx:responseError` handler.** The maturity fix closed the one
   *reachable* silent-400; the class exists everywhere (`hx-swap="none"` + a 4xx renders
   nothing at all).
7. **Proposal C** — pre-run preflight on the workflow page ("needs 1 pack + 3 models"
   before you click Run). Real work; it is what migration 0019 unlocks. **Proposal D** —
   make `coreNodeClasses` authoritative from the cached `/object_info`; fixes the cloud
   banner's measured 790-built-ins-called-custom misfire, decoupled from everything else.
8. Work the test debt below.

## Test debt still open (from the v0.1.96 audit — code CORRECT, guards MISSING)
1. `internal/poller/poller.go` — that a permanently-skipped workflow version is still
   marked seen and still notified on is guarded by **nothing**.
2. `internal/web/run_preset_pages.go` — the "a graph with no prompt input must not
   collapse 100% of its params" fallback is unguarded; inverting it stays green.
3. `handleSubscribe` — the DASHBOARD subscribe form never consults `workflowPostFlag`, so
   pasting a workflow-post URL there still stores `AutoDownload=true`. Cosmetic (the
   poller guard holds) but it is the exact shape v0.1.96 removed everywhere else.
4. `run_zone_web_test.go` — one guard is vacuous (`newTestServer` has no `comfy_url`, so
   the fragment renders the unconfigured branch and the assertion cannot fail).

## Other open threads (all PRE-EXISTING, deliberately not fixed)
1. 🔴 **Bypassing a subgraph instance does not bypass it** — `flattenSubgraphs` emits all
   interior clones at `mode=0` because it flattens BEFORE the mode drop.
2. 4 custom-sampler workflows expose no seed (`runInputLayouts` yields `RunInputSeed` for
   exactly `KSampler`/`KSamplerAdvanced`).
3. The 64 MiB per-output cap is silent on oversize.
4. `.cm-updated-pop` does not flip near the right viewport edge.
5. `discover_facets.go`: 260 facet combos vs `maxFacetFeedEntries = 256`; no singleflight.
6. macOS UNVERIFIED end-to-end; Homebrew 6.0 tap-trust flag spelling unverified.
7. **Named coverage gap**: the ux-audit UI hero is a clean 8-node linear graph, so
   subgraphs, Get/Set teleports, rgthree nodes, object-form `widgets_values` and
   multi-mode `toggleRestriction` templates stay unexercised — and because the fixture is
   deliberately warning-free, **the conversion-warnings panel is now unaudited on both
   formats**, arguably the panel real users hit most (44/70 workflows trip the detector).

## ⚠ A mistake worth keeping
A `comfy_model_cache` row was inspected, found to be a **256-byte hand-seeded stub** with
fabricated filenames, and clearing it was agreed. By the time the `DELETE` ran the
operator had dogfooded and the **real 4,661,987-byte write had landed** — so the delete
removed real data, not the stub. Impact nil (a cache with a designed empty state, and a
`.pre-comfy-cache.bak` exists), but the rule it broke is the one that matters:
**re-verify a remembered fact at the moment you ACT on it, not when you formed the plan.**

Silver lining: that row is the proof the write path executes end-to-end on the real DB.

## How to verify the current release
```sh
gh release download v0.1.101 -R ZacxDev/civitai-manager \
  -p 'civitai-manager_0.1.101_linux_amd64.tar.gz' -p 'checksums.txt'
sha256sum -c --ignore-missing checksums.txt      # must print OK
tar xzf civitai-manager_0.1.101_linux_amd64.tar.gz && ./civitai-manager --version
gh attestation verify civitai-manager_0.1.101_linux_amd64.tar.gz -R ZacxDev/civitai-manager
AUDITLOOP_CHROMIUM=/run/current-system/sw/bin/brave make ux-audit   # 24 pages, 8 violations
```
⚠ **8 violations is the correct current number** — see the a11y section. A run reporting
**0** means the walk regressed to the API-only fixture and is measuring the wrong surface
again.

## Cleanup / side effects
- Every state-mutating verification used a **throwaway DB** (a `.backup` of the real one)
  on a scratch port. The real DB was written to only by intended migrations and by the one
  cache delete recorded above. ComfyUI's queue was confirmed empty before **and** after
  every run-path check — **nothing was ever submitted**.
- Browser verification drove the operator's **live Brave** (`work` profile) in its own tab,
  closed each time.
- **8 stale agent worktrees** remain under `.claude/worktrees/`; every branch except the
  shared checkout's own is merged. Safe to prune.

## A comment that lies, found after the sweep closed

`e2e/uxaudit/fakes.go:166-167` still asserts, present tense:

> `realRun` returns EARLY on any conversion warning — before `comfy.Preflight` — so a
> UI-format hero would render the conversion-warnings panel instead of the missing-models
> one

**Both halves are now false.** PR #46 replaced that early return with the never-submit
disjunction at `run_handlers.go:589-607`, which runs **after** `comfy.Preflight`. And when
`report.OK` is false — exactly what `fakeObjectInfoJSON` is engineered to produce — the
run takes `preflightFailureResult` and renders the missing-models panel **with** the
conversion warnings attached, not "instead of" it.

**The `input_order` requirement it justifies probably still holds — for a DIFFERENT
reason, and that reason is UNVERIFIED.** Hypothesis: without `input_order`,
`ConvertUIToAPI` never maps `widgets_values`, so `ckpt_name`/`lora_name` never reach the
API graph, preflight finds no model references, `report.OK` stays **true**, and the run
falls into the `report.OK` branch at `:596` — the warnings-only panel. Same observable
outcome as the old comment predicted, arrived at through the opposite mechanism.

**Next probe** (do this before rewriting the comment — do not reason it out):
delete `"input_order"` from the `CheckpointLoaderSimple` entry of `fakeObjectInfoJSON`,
run the uxaudit walk, and record which panel the UI-format hero actually renders. If the
missing-models panel still appears, `input_order` is no longer load-bearing at all and the
whole 🔴 block goes rather than gets corrected.

Left unfixed deliberately: the docs pass that found it was scoped to documentation, and
this is a Go source file that needs the gate. It is a ~5-line change plus one measurement.
