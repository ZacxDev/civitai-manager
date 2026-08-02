# civitai-manager — session handoff (2026-08-02, post-v0.1.102)

_Point-in-time snapshot. Verify against `git log` / live state before acting. Durable
conventions and lessons live in the repo `CLAUDE.md` — read it first; this doc is STATE
and OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)

> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read
> `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first.
> Orientation: latest release **v0.1.102** (tagged, verified, dogfooded), `main` @
> `19372da`, **zero unreleased commits**, migrations at **0019** on main and on my real
> DB. Two PRs may be in flight — `gh pr list` is the authority. **GOPRIVATE is NOT
> needed.** The shared checkout is now on **`main`** and clean; all other sessions are
> closed, so the shared-checkout hazard that shaped every brief last session no longer
> applies. Standing OK to push+tag+release without asking — run the real gate (`go build
> ./... && go vet ./... && go test ./... && gofmt -l ./internal/ ./e2e/ ./*.go`; **`gofmt`
> is NOT covered by `go vet`**, **`./cmd/` does not exist**, and **`e2e/uxaudit` is a
> NESTED module** a root `go test ./...` never compiles — it has its own CI job), plus
> `/audit-pr` scaled to blast radius and **a delta re-audit after EVERY fix round**; then
> **push `main` BEFORE tagging**, verify the tarball, refresh the `:8972` dogfood (kill by
> `pgrep -x cm` + `/proc/<pid>/exe`, wait until NO cm process remains — **a free port is
> not a released binary** — then verify the served build by pid + `--version`). If
> `go.mod` changes at all, re-run `nix build .` for `vendorHash`. **You CAN drive a real
> browser** — it has found a real bug in every visual branch across seven sessions. Loop:
> feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate
> → audit → delta re-audit → ship → verify tarball → refresh dogfood.

## Current state

- **Latest release: v0.1.102**, tag `19372da`, 11 assets. Verified end-to-end rather than
  "the job was green": checksum **OK**, binary reports `0.1.102 (commit 19372da…)`,
  attestation `rc=0` **with a negative control** (one appended byte → `rc=1` / 404, so the
  quiet pass is real).
- **`main` @ `19372da` — nothing unreleased.**
- **Base clone is on `main`**, `--ff-only` clean.
- **Dogfood on `:8972`** serves v0.1.102, pid confirmed via `/proc/<pid>/exe`.
  ⚠ It runs from `…/766dd9a1-…/scratchpad/dogfood/cm`, which **dies with that session's
  temp dir**. Re-download the release tarball rather than trusting the path.
- Real DB at **schema 19**.
- ⚠ **Three 23 MB backups** in `~/.config/civitai-manager/` (`.pre-0017`, `.pre-0018`,
  `.pre-comfy-cache`) ≈ 68 MB. The first two are long superseded — delete when happy.
- `go.mod`/`go.sum` **untouched across the entire v0.1.84 → v0.1.102 run**.

## In flight — two dispatched agents; check `gh pr list` first

1. **Port of `feat/comfy-model-cache`.** 1 ahead / 62 behind; tip `46a10ed` is safe on
   `origin` at the same sha. Its migration `0019` is **byte-identical** to main's and
   `internal/store/comfy_cache.go` is already there — the store layer needs no porting.
   Genuinely unmerged: the **three-state resource chips** (✓ library / ◎ ComfyUI / ✗
   missing) with hover popovers, the `ChoicesContain` export, and three maturity tweaks.
   `git merge-tree --write-tree origin/main 46a10ed` **exits 1** — real conflicts in
   `app.css`, `layout.go`, `workflow_pages.go`, `workflow_handlers.go`,
   `workflow_resources.go` and two test files, because `main` redesigned the maturity
   control (Apply button) after the branch forked. The agent was told to treat the old
   branch as a **specification, not a patch**, and that dropping the obsolete maturity
   items is a legitimate outcome. **Do not delete `feat/comfy-model-cache` until that PR
   merges.**
2. **Make `coreNodeClasses` authoritative** from `/object_info` module paths
   (`comfy_extras.*` / `nodes` vs `custom_nodes.*`). Measured bug: ComfyUI ships **790
   built-ins**, the hand-written table knows ~50, and **44 of 70 workflows (62%)** contain
   a real built-in it calls custom. Scope is the CivitAI **cloud** path only
   (`resolve.go`, `cloud_pages.go`) — the local-run attribution path goes Manager → static
   index → Registry and never touches the table. Restoring a confident banner is
   explicitly **out of scope**.

## What shipped this session (v0.1.101 → v0.1.102, 27 commits)

| PR | what |
|---|---|
| **#49** | pinned the `canQueue` agreement; consolidated three copies into one predicate |
| **#50** | **8 axe violations → 0** — Fork out of the run-preset tablist, graph SVG named |
| **#51** | deleted 5 unreachable functions; **`deadcode` reachability gate in CI, 3× GOOS** |
| #47, #48 | handoff + `CLAUDE.md`/skills documentation pass |
| #52 | the 0.1.102 version bump |

User-visible: the run-preset strip is screen-reader navigable and the workflow graph
announces itself; the nodepack install path is reachable for UI-format workflows;
`--web-scan-timeout` finally does something after 89 inert releases.

## 🔴 The new CI gate, and how not to fight it

`.github/deadcode.sh` + `.github/deadcode-allow.txt`, every PR, three GOOS.

**Two tiers, and the second is the one that matters.** Tier A is `-test` mode and is
nearly blind: one `reflect.Value.Call` in cobra's help templating drags the **entire test
corpus** into the reachable set, so `-test` can only catch functions literally nothing
references — it would have caught **none** of the 17 items the manual sweeps found. Tier B
(default mode) is the real signal: functions with **no production caller**.

**The allowlist is a debt ledger, not an exemption list** — the set is *asserted*, so the
gate fails when it GROWS *and* when it SHRINKS. If an entry stops being dead, delete the
line and take the win. Adding a line to turn a red build green is a last resort.

⚠ **The 15 entries were MEASURED, not investigated.** Five were spot-checked: they are
**superseded wrappers, not dropped features** — `runModesPanel` is a 2-line delegate to
`runModesPanelWith` while production calls `runModesPanelSelected`, and the live page
renders both panels. Cleanup debt, **not** a user-visible regression.

🔴 **The harness trap that produced a false green:** `GOOS=windows go run
golang.org/x/tools/cmd/deadcode@latest ...` **cross-compiles the TOOL** and dies with
`exec format error`. With stderr suppressed that reads as "0 dead functions", and an
intersection got built on top of it before it was caught. **Build the tool once for the
host, then set `GOOS` for the analysis only.** The CI script does this and carries its own
negative control: a fake tool exiting `0` with empty output still fails, because tier B's
expected set is non-empty.

## Open threads (ranked)

1. **Proposal C — preflight on the workflow page, before the Run click.** "Needs 1 node
   pack + 3 models" shown *before* the user commits to a run. A and B already shipped
   (#46 — `preflightFailureResult` reports missing nodes AND models in one pass). C was
   blocked on a caching policy so the **4.66 MB** `/object_info` is not refetched per
   render — exactly what `0019` + `comfy_cache.go` exist for, both on `main` and currently
   unused. **Hold until the port PR lands**; they touch the same workflow page.
2. **`e2e/uxaudit/fakes.go:166-167` states a mechanism #46 deleted.** It claims `realRun`
   "returns EARLY on any conversion warning — before `comfy.Preflight`". Both halves are
   now false: the never-submit disjunction at `run_handlers.go:589-607` runs **after**
   preflight, and on the failure that fixture produces, warnings render **with** the
   missing-models panel, not instead of it. The `input_order` requirement it justifies
   probably still holds for the **opposite** reason (unmapped widgets → preflight sees no
   model refs → `report.OK` stays true → warnings-only branch). **Probe before rewriting:**
   delete `"input_order"` from the `CheckpointLoaderSimple` entry of `fakeObjectInfoJSON`,
   run the walk, record which panel renders. If missing-models still appears, the whole 🔴
   block goes rather than gets corrected.
3. **Orphan route `GET /workflows/run/resolve-model`** — registered, loopback-gated, makes
   outbound egress, and **nothing links to it**. ~8 tests exercise it, reading as coverage
   of the missing-model resolve feature; production renders
   `resolveModelFragmentWithReason` instead. Delete it, or document it as a debug endpoint.
4. **Two systematic guards** that would have caught most of this class mechanically:
   a **route-emitter check** (diff registered `mux.Handle*` paths against emitted
   `hx-*`/`href` literals, with an allowlist; ~60 lines) and a **reverse class-coverage
   test** (~30 lines) for CSS rules nothing emits. The `deadcode` gate covers Go functions
   only — these are the routing and CSS equivalents.
5. **Work the tier B ledger down** — 15 superseded wrappers. Do this **after** the two
   in-flight PRs, or the list goes stale the way the last one did.
6. **3 dead `.cm-*` CSS rules**: `.cm-crumb-name`, `.cm-vstatus-date`, `.cm-gen-sep`.
   **`Server.attributeFn`** — a test seam nothing sets, in production or in any test.
7. **The "why is this tag not shown" affordance was never built.** `StopwordTags`' comment
   described it as existing; it does not. `internal/web/workflow_facets.go:125` drops
   stopword tags silently with no on-screen explanation. (That function is now deleted;
   the gap it described is not.)
8. **11 stale agent worktrees** under `.claude/worktrees/`. All merged; safe to prune.

## Open investigations — live diagnosis state

### The ComfyUI model cache repopulates on use; empty is a designed state
`cacheComfyObjectInfo` is wired in `run_handlers.go` (realRun) and `cloud_handlers.go`,
with invalidation after a library scan. **Proven end-to-end on the real DB**: a
**4,661,987-byte** row with an RFC3339 `updated_at` (i.e. `nowRFC3339()`, so the app wrote
it) appeared during dogfooding. Until it populates, the ◎ ("in ComfyUI") chip state cannot
render and chips correctly fall back to ✓/✗.
⚠ **Scan invalidation is a heuristic**, not a guarantee — a scan means *our* file set
changed, not ComfyUI's. So after a scan, ◎ chips revert to ✗ until the next run.

### `web_scan_timeout`: live since v0.1.102's line, with an upgrade hazard
Inert from **v0.1.13 to v0.1.101 — 89 releases**. Now enforced, default **6h** (the value
`scanJobBudget` already imposed, so unset behaviour is unchanged).
🔴 **`docs/configuration.md` shipped `web_scan_timeout: "2m"` inside the annotated sample
config** for that whole period, and no code path writes the key — so hand-copying the docs
was the ONLY way to hold an explicit value. Anyone who copied it now has an enforced 2m.
**And a deadline firing during the HASH phase persists ZERO rows** — hashing is phase 1,
`local_files` are written in phase 3; measured, a 150 ms deadline against a 12 × 200 MB
fixture saved nothing. For a large library that is a total loss, every time.
`docs/configuration.md` now carries the upgrade warning; **a release note would be kind.**

## How to verify the current release

```sh
gh release download v0.1.102 -R ZacxDev/civitai-manager \
  -p 'civitai-manager_0.1.102_linux_amd64.tar.gz' -p 'checksums.txt'
sha256sum -c --ignore-missing checksums.txt      # must print OK
tar xzf civitai-manager_0.1.102_linux_amd64.tar.gz && ./civitai-manager --version
gh attestation verify civitai-manager_0.1.102_linux_amd64.tar.gz -R ZacxDev/civitai-manager
AUDITLOOP_CHROMIUM=/run/current-system/sw/bin/brave make ux-audit
```

🔴 **The axe walk must report 24 captures / 0 violations.** A different **capture count**
means the walk changed shape; **0 captures** means it regressed to the API-format fixture
no real workflow uses — the failure that let it certify a dead surface for four releases.
Zero *violations* is now the correct number, so the earlier "8 is correct" note is retired.

`gh attestation verify` is **silent on success** in some shells. Do not read a quiet
`rc=0` as proof without a negative control — append one byte to a copy and confirm `rc=1`.

## Corrections made this session, so they are not re-derived

- **`git tag --contains` in a worktree returns the same count as the base clone** (89, not
  2). Worktrees share the ref store; the earlier claim was wrong.
- **The CI blind-spot bullet in `CLAUDE.md` had already gone false** — a dedicated
  `uxaudit` job now compiles the nested module. Only the *local* root-module exclusion
  still bites.
- **`pickFileFromModelRaw` never lived in `internal/comfy/download_target.go`.** The
  offer-don't-perform behaviour is `pickFileFromModel` + `renderSubstituteOffer` in
  `internal/web/run_download.go`; `TypeSubdir` is the part that really is in
  `download_target.go`. Corrected in `CLAUDE.md` with a tombstone.
- **"Nothing tests the `canQueue` agreement" was wrong.** Three of four drift directions
  were already caught; only the button-falls-back-to-single-run direction survived, because
  one test seeds an API-format workflow (so the UI branch never runs) and another
  hand-builds the view struct (pinning the renderer, not the handler's derivation).
- **The allowlist "dead UI" scare was wrong** — superseded wrappers, not dropped features.

## Cleanup / side effects

- Every state-mutating verification used a **throwaway DB** on a scratch port. The real DB
  was written to only by intended migrations.
- ComfyUI's queue was confirmed empty before and after every run-path check — **nothing
  was ever submitted**, and no Install button was ever clicked.
- Browser verification drove the operator's **live Brave** in its own tab, closed each time.
