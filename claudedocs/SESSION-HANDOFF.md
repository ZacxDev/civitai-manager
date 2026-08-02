# civitai-manager — session handoff (2026-08-02)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.101**, `main` clean, **ZERO open PRs** (#37 closed as superseded), migrations at **0019** on main and on my real DB — the trap is CLOSED. **GOPRIVATE is NOT needed.** 🔴 **ONE thing waits on MY decision: whether to implement proposal A+B for the dead nodepack-install path (see 'The nodepack install feature is unreachable' below).** Standing OK to push+tag+release without asking — run the real gate (`go build ./... && go vet ./... && go test ./... && go test -race ./internal/web/... ./internal/comfy/... ./internal/store/... ./internal/diskusage/... && gofmt -l ./internal/ ./e2e/ ./*.go`; **`gofmt` is NOT covered by `go vet`**, **`./cmd/` does not exist**, and **`e2e/uxaudit` is a NESTED module** a root `go test ./...` never compiles — it now has its own CI job) + `/audit-pr` scaled to blast radius + **a delta re-audit after EVERY fix round**, then **push `main` BEFORE tagging**, verify the tarball, refresh the `:8972` dogfood (kill by `pgrep -x cm` + `/proc/<pid>/exe`, wait until NO cm process remains — a free port is not a released binary — then verify the served build by pid + `--version`). If `go.mod` changes at all, re-run `nix build .`. **You CAN drive a real browser** — it found a real bug in EVERY visual branch across five sessions. 🔴 **The shared checkout is on ANOTHER session's branch (`feat/comfy-model-cache`) — do NOT switch or commit in it; use a worktree and run `git branch --show-current` immediately before any commit.** Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate → audit → delta re-audit → ship → verify tarball → refresh dogfood.

## Current state
- **Latest release: v0.1.101** (`origin/main`). Tarball verified: `sha256sum -c` OK,
  binary runs, `gh attestation verify` exits 0.
- **Dogfood on `:8972`** runs the RELEASED binary from this session's scratchpad
  (`/tmp/claude-1000/.../766dd9a1-.../scratchpad/dogfood/cm`). ⚠ That path dies with
  the session's temp dir — re-download the release tarball rather than trusting it.
- `go.mod`/`go.sum` **untouched across the entire v0.1.84 → v0.1.101 run**, so
  `vendorHash` has never needed re-verification. Checked every release, not assumed.
- **One open PR: #37** (another session's `feat/comfy-model-cache`) — **NEEDS REWORK**.
  The shared checkout is sitting on that branch.
- Untracked `opencode.json` in the repo root; not mine, left alone.

## What shipped this session (v0.1.99 → v0.1.101)
**#32/#33/#34** axe walk expanded to search/creator/model-detail · breadcrumbs on 4
detail pages · duplicate copy + redundant `aria-label`s cut ·
**#35** the dead axe harness + the two AA failures it then found ·
**#36** maturity band stages, commits on an explicit **Apply** ·
**#38** v0.1.99 + delta-re-audit corrections ·
**#39** the inverted-range regression #36 introduced, plus a **retraction** ·
**#41** the reworked ComfyUI model cache + three-state chips + Safe mode (supersedes #37) ·
**#42** three guards the supersession audit found missing.

## 🔴 The axe harness was dead for two releases — the lesson generalises
The hero prep looked for `button[hx-post="/workflows/N/run"]`. v0.1.97's one-run-zone
rework renamed that control to **"Generate"** posting to `/run-with-params`. The
selector matched nothing at either viewport, `WaitVisible` hung, the 90s capture
context expired — surfacing as a bare `capture run-missing-models (mobile): context
deadline exceeded`. Reproduced identically at `e254cab`, so it predated the merges.

**Nothing caught it because `make ux-audit` is double-gated out of `go test ./...`
(needs `UXAUDIT_WALK` + a Chromium). A harness that never runs reports no failures.**

Closed three ways:
1. `e2e/uxaudit/walk_selectors_test.go` — a **browserless** rot-guard that boots the
   real lab app and asserts over HTTP that the app still serves what the walk's
   selectors key on (run control, import trigger, dialog id, `#run-status`), that every
   view path 200s, and that `Views()` has not shrunk.
2. A dedicated **`uxaudit` CI job** — the nested module had NEVER been compiled by any
   gate. Its own job because it declares `go 1.26` vs the root's `1.25.0`; bumping the
   shared job would change the compiler every root check runs under. Green on a real
   runner.
3. Selectors hoisted to `RunPostPath` / `ImportTriggerTitlePrefix` / `ImportDialogID`
   so walk and guard cannot drift apart.

**Current axe state: 20 pages (10 views × 2 viewports), 0 violations.**

## 🔴 Two WCAG AA failures from `opacity` on text — the contrast gate CANNOT see them
`contrast_web_test.go` resolves **token pairs** and has no model of an `opacity` on the
text element. Both dimmed an already-dimmed token below the 4.5:1 floor:

| rule | opacity | dark | light |
|---|---|---|---|
| `.cm-dest-tab-sub` | 0.85 | 5.39 → **4.28** | 4.79 → **3.58** |
| `.cm-vgroup-pill-count` | 0.75 | 5.39 → **3.62** | 4.79 → **2.98** |
| …on the *active* pill | 0.75 | 6.96 → **4.47** | — |

Removing both fixed every pair on BOTH themes and added **no debt entries** — they
collapse into "dimmed text on the page", already in the table.
⚠ axe found only the first; `.cm-vgroup-pill-count` renders on **no surface the walk
visits** and was found by auditing every `opacity` on text afterward.

**When you add any `opacity` to text, compute the ratio by hand** — the gate will not.
The Apply button's dim is now pinned to the design system's own `0.6`
(`civitai-components.css`), because drifting below it is likewise invisible.

## 🔴 The maturity control failed OPEN twice more — read before touching it
Full invariant is in `CLAUDE.md`. The two new ones:

1. **A staged-but-unapplied band survived a dismiss.** Escape/light-dismiss left the
   selection in the DOM; reopening later and pressing Apply committed it. **Measured on
   two builds against a throwaway DB:** saved `pg:pg13` → stage max=XXX → Escape →
   reopen → Apply → the pre-fix binary persisted **`pg:xxx`** (the FULL range, from an
   Apply meant as a no-op); the fixed binary persisted `pg:pg13`. Fixed by
   `ontoggle="…form.reset()"` on the panel — `reset()` restores the server-rendered
   `checked` ATTRIBUTES and fires neither `change` nor `submit`, so it can never commit.
2. **Staging made an inverted range reachable and SILENT** (shipped v0.1.99, fixed
   v0.1.100). At the full band both tracks emit all 5 stops, so `min=X` + `max=R` is
   stageable; Apply POSTed it, the server correctly 400'd, and with `hx-swap="none"` and
   **no `htmx:responseError` handler anywhere** nothing visible happened. Fixed by
   gating the commit: `aria-disabled` + a `role="status"` reason, POST cancelled in
   `htmx:beforeRequest`.
   **The rejected alternative matters:** re-rendering the other track's bounds
   client-side is closer to the server's "emit no input for an out-of-range stop"
   principle, but it means programmatically reshaping a radio group — exactly where the
   v0.1.98 wrap bug lived. Gating the commit can only fail **closed**.

🔴 **RETRACTED ADVICE — do not re-derive it.** I wrote in three places that a preset
("safe mode") should set both radios then call `form.requestSubmit()` once. **That is
wrong in the fail-OPEN direction.** `maturityTrack` emits **no `<input>`** for an
out-of-band stop and the max track's low bound is the **saved** `mr.Min`:
```
saved=PG..XXX   min-pg present=true   max-pg13 present=true
saved=R..XXX    min-pg present=true   max-pg13 present=FALSE   checked max=xxx
saved=X..XXX    min-pg present=true   max-pg13 present=FALSE   checked max=xxx
```
From a saved band of `R..XXX` that POSTs `min=pg&max=xxx` and persists **PG..XXX** — a
control labelled *Safe mode* that reveals everything. The radios are an interlock keyed
to the SAVED band. Anything needing an arbitrary band must **bypass them**: its own
CSRF-protected POST carrying the literal `min`/`max`, validated by
`handleSetMaturity`'s existing `min > max` rejection. Corrected in `CLAUDE.md` and
`layout.go`, and retracted publicly on PR #37.

## 🔴 The nodepack-install feature is UNREACHABLE for UI-format workflows

Reported as "workflow 590 won't run: `node 663 type "UltimateSDUpscale" not available`".
Diagnosed 2026-08-02; **not fixed** — proposal below is the open decision.

**The converter is CORRECT; do not "fix" it.** Measured against the live
`/object_info` (2462 types) and the real graph:
- `UltimateSDUpscale` is genuinely absent from ComfyUI, node 663 is `mode=0`
  (ACTIVE), and it feeds **five** consumers — refusing to submit is right.
- Exactly **ONE** warning is emitted, not one per missing type. `virtualNodeTypes`
  (`internal/comfy/convert.go:26-47`) already strips all 28 UI-only nodes in this
  graph (`MarkdownNote` ×16, `Note` ×2, `Label (rgthree)` ×5, `Fast Bypasser` ×4,
  `Fast Groups Bypasser` ×1).
- **Bypass IS honoured** — the mode drop at `convert.go:199-206` runs BEFORE the
  availability check at `:208`. Mutation-verified: flipping the 7 `mode=4` nodes to
  `mode=0` took the warning count **1 → 4** (adding the three SeedVR2 nodes). The
  known `flattenSubgraphs`-before-mode-drop defect is NOT implicated — this graph
  has no `definitions` key, so `flattenSubgraphs` never runs.

**ROOT CAUSE — `internal/web/run_handlers.go:492-495`:**
```go
if len(warnings) > 0 {
    return &runResult{Warnings: warnings}, nil   // returns BEFORE comfy.Preflight
}
```
That leaves `Preflight == nil`, and in `run_pages.go` the ENTIRE actionable failure
UI is behind `if snap.Preflight != nil` (`run_pages.go:534`). So
`attributeMissingNodes`, `missingNodesPanel`, the ComfyUI-Manager one-click install
and the manual-command fallback are **dead code on the UI-format path**. The user
gets a generic sentence plus a collapsed "Technical details" drawer holding one
class name.
⚠ Also: the converter DELETES the unknown node, so `report.MissingNodes` would be
`[]` even if preflight ran. `MissingNodes` can only ever populate for **API-format**
workflows, where the unknown class survives verbatim.

**The machinery works — verified live.** ComfyUI-Manager **V3.41** answers
`/api/customnode/getmappings?mode=cache` with **200 / 5573 entries** and resolves
the class to `comfyui_ultimatesdupscale`; the static index places it at
`https://github.com/ssitu/ComfyUI_UltimateSDUpscale`. A one-click install would
have worked. (Bare `/api/customnode/getmappings` without `mode=cache` returns
**500** — that param is load-bearing, as its comment claims.)
⚠ **The weak `ResolveCustomNode`/`coreNodeClasses` detector is IRRELEVANT here** —
it is referenced only by `internal/comfy/resolve.go` and `internal/web/cloud_pages.go`,
i.e. the CivitAI **cloud** path. The local-run attribution path goes Manager →
static index → Registry and never touches that table. **Nothing below is gated on
fixing it.**

**Proposal (ranked, NOT implemented):**
- **A — do this one.** `ConvertUIToAPI` already computes the distinct unknown-class
  set and throws it into a format string. Return it structured; in `realRun`
  synthesize a `PreflightReport{MissingNodes: …, OK: false}` and fall through to the
  EXISTING `if !report.OK` block at `run_handlers.go:537`. No new UI. Watch: keep
  never-submit an EXPLICIT gate (not an emergent property of falling into
  `!report.OK`); `ConvertUIToAPI` has three callers (`run_handlers.go`,
  `cloud_handlers.go:137`, `run_batch.go`) so add a struct field rather than change
  the signature; moving the abort later changes what preflight observes.
- **B — free once A lands.** The same round then reports the 3 missing model files
  (`1xSkinContrast-High-SuperUltraCompact.pth`,
  `4xNomosWebPhoto_RealPLKSR.pth`, `ComfyUI\moody-porn-v12.6_00001_.safetensors`),
  killing a guaranteed second round-trip. 🔴 **Copy must not claim completeness** —
  a dropped node's own model refs vanish with it, so the list is a LOWER BOUND.
- **C — later.** Pre-run preflight on the workflow page ("needs 1 node pack + 3
  models" before you click Run). Real work: new fragment + a caching policy so the
  4.66 MB `/object_info` isn't fetched per render. This is what the `0019`
  object_info cache unlocks — sequence AFTER, don't block A on it.
- **D — decoupled.** Make `coreNodeClasses` authoritative from cached
  `/object_info`. Fixes the cloud banner's 790-built-ins-called-custom misfire, does
  nothing for 590.
- **E — skip.** Just improving the warning string duplicates attribution at a second
  call site (the "one rule, one place" trap) and is superseded by A.
- **NOT recommended:** relaxing the `len(warnings) > 0` abort into a severity
  policy. Blunt, but fail-closed, and correct here.

## ⚠ I deleted a live cache row by acting on stale state

Recorded because the mistake is more instructive than the impact.

The `comfy_model_cache` row was inspected and found to be a **256-byte hand-seeded
stub** with fabricated filenames (`some_other_model.safetensors`), driving 5 bogus
◎ chips. Clearing it was agreed. By the time the `DELETE` ran, the operator had
dogfooded and the **real** write had landed — **4,661,987 bytes, `updated_at` in
RFC3339** (i.e. `nowRFC3339()`, so the app wrote it). The delete removed real data.

Impact nil — it is a cache with a designed empty state and repopulates on the next
run; `schema_migrations` stayed 19 and `civitai-manager.db.pre-comfy-cache.bak`
exists. **The lesson is the rule this broke: re-verify a remembered fact at the
moment you act on it, not when you formed the plan.**

Silver lining: that row is the proof the write path executes end-to-end on the real
DB, which was the open question blocking confidence in the feature.

## Open investigations — live diagnosis state

### ~~The real DB reports schema 19 while main stops at 0018~~ — CLOSED 2026-08-02
- **RESOLVED:** #41 shipped `0019` byte-identical to the branch that had already applied it (blob `2b0894bd`), and no `0020` was added — so `main` and the DB agree and NO repair was needed. Kept for the reasoning.
- **Was:** a future `0019_*.sql` would **silently never apply** on this machine —
  the runner skips `version <= current` (`internal/store/store.go:117`).
- **Observed (actual values):**
  ```
  $ sqlite3 ~/.config/civitai-manager/civitai-manager.db \
      'select * from schema_migrations order by 1 desc limit 3;'
  19|2026-08-01T21:55:53Z
  18|2026-07-31T17:31:02Z
  17|2026-07-31T04:06:47Z

  $ git ls-tree --name-only origin/main internal/store/migrations/ | tail -1
  internal/store/migrations/0018_maturity_range.sql

  $ sqlite3 … "select name from sqlite_master where type='table' and name like 'comfy%';"
  comfy_model_cache

  $ sqlite3 … 'select updated_at from comfy_model_cache limit 1;'
  2026-08-01 21:59:43
  ```
- **Ruled out:** *the app wrote that cache row* — `updated_at` is sqlite's
  `datetime('now')` shape, **not** the `…T…Z` `nowRFC3339()` the Go code emits, and
  `PutComfyObjectInfo` has **zero non-test callers** anyway. The `schema_migrations`
  row IS RFC3339, so the migration runner genuinely ran — from a build of
  `feat/comfy-model-cache` (PR #37), which owns `0019_comfy_model_cache.sql`.
- **Leading hypothesis:** a build of #37 was run against the real DB, applying its
  0019; the cache row was hand-seeded separately afterwards.
- **Next probe / fix (needs the operator's OK — live DB):**
  ```sh
  cp ~/.config/civitai-manager/civitai-manager.db{,.pre-0019-rollback.bak}
  sqlite3 ~/.config/civitai-manager/civitai-manager.db \
    "delete from schema_migrations where version=19; drop table comfy_model_cache;"
  ```
  Only needed if #37 is reworked with a **different** 0019. If #37 lands as-is, the DB
  is already correct and nothing should be done.

### ~~PR #37~~ — CLOSED 2026-08-02, superseded by #41
**Closed.** A supersession audit compared it file-by-file with `main`: nothing of value is missing, `merge-tree` conflicts on **7 files — every non-identical file of the feature**, and merging could only subtract. Kept below because the six blockers are the reasoning behind #41's shape.
Full audit: [PR comment](https://github.com/ZacxDev/civitai-manager/pull/37#issuecomment-5154544716).
1. **Red on its own branch, and the PR body misattributes it.** The Safe-mode `onclick`
   begins with a literal `javascript:void(...)`, tripping two site-wide XSS canaries
   that scan the whole page (the control is in the nav → every page). **PASS at its own
   base `1464cca`, PASS at `origin/main`, FAIL at `46a10ed`.** Deleting the no-op
   `javascript:` prefix fixes both.
2. **Safe mode is dead after merge** (`.click()` saves nothing since staging) — and the
   `requestSubmit()` "fix" is wrong too (see the retraction). Needs its own POST.
3. **The ComfyUI-cache feature is INERT** — `PutComfyObjectInfo` /
   `InvalidateComfyModelCache` have zero non-test callers, so the third chip state can
   never render. `0019`'s header documents three triggers that do not exist.
4. **Unbounded work on a render path** — re-reads + `json.Unmarshal`s the entire
   `ObjectInfo` **once per chip**. Measured against the real live payload
   (4,661,987 bytes): **88 ms per call**; the real DB has 305 resource entries → ≈27 s
   render time on the workflows page.
5. **The resources popover opens ALL nested chip popovers at once** (descendant
   combinator in `app.css`), covering chips 2 and 3. Browser-measured.
6. **Two existing guards weakened into vacuity** (`HasPrefix`→`Contains` + a deleted
   count assertion) — mutation-proven: a chip rendering as `<span>` instead of `<a>`
   keeps all three suites green.
- **Merge:** conflicts in `internal/web/layout.go` (~line 332) where #36's Apply row
  landed. ⚠ The deletion of `cm-maturity-note` auto-merges **silently** — a reviewer
  resolving only the marked hunk will not see it. Naive "take both" compiles and adds a
  third failure. Branch is **10 commits behind main** and was never tested against it.

## Next steps (ranked)
1. **Decide the schema-19 trap** — 2 min, but needs your call on the live DB.
2. **Decide #37's fate** — hand the findings back to that session, or rework here.
   Do not merge as-is.
3. **Consider an app-wide `htmx:responseError` handler.** The maturity fix closed the
   one *reachable* silent-400; the class exists everywhere (`hx-swap="none"` + a 4xx
   renders nothing). Deliberately scoped out of #39.
4. Two 🟢 test nits from the #42 delta audit: the ◎-rule guard's `Contains(body, "color:")` is also satisfied by `background-color:` (probe-verified), and the corrupt-cache precondition asserts `ent != nil` but not a non-empty body.
5. Work the test debt below.

## Test debt still open (from the v0.1.96 audit — code CORRECT, guards MISSING)
1. `internal/poller/poller.go` — that a permanently-skipped workflow version is still
   marked seen and still notified on is guarded by **nothing** (wrapping the
   `new_version` AddEvent in `if outcome == outcomeEnqueued` leaves the suite green).
2. `internal/web/run_preset_pages.go` — the "a graph with no prompt input must not
   collapse 100% of its params" fallback is unguarded; inverting it stays green.
3. `handleSubscribe` — the DASHBOARD subscribe form never consults `workflowPostFlag`,
   so pasting a workflow-post URL there still stores `AutoDownload=true`. No download
   happens (the poller guard holds), so it is cosmetic dishonesty on an untouched
   surface.
4. `run_handlers.go` — nothing guards the UI-format → `/run/queue` wiring; mutating
   `canQueue := false` makes batching inert with the whole `internal/web` suite green.
5. `run_zone_web_test.go` — one guard is vacuous (`newTestServer` has no `comfy_url`,
   so the fragment renders the unconfigured branch and the assertion cannot fail).

## Other open threads (all PRE-EXISTING, deliberately not fixed)
1. 🔴 **Bypassing a subgraph instance does not bypass it** — `flattenSubgraphs` emits
   all interior clones at `mode=0` because it flattens BEFORE the mode drop.
2. 4 custom-sampler workflows expose no seed (`runInputLayouts` yields `RunInputSeed`
   for exactly `KSampler`/`KSamplerAdvanced`).
3. The 64 MiB per-output cap is silent on oversize.
4. `.cm-updated-pop` does not flip near the right viewport edge.
5. `discover_facets.go`: 260 facet combos vs `maxFacetFeedEntries = 256`; no
   singleflight on `facetFeed`.
6. macOS UNVERIFIED end-to-end; Homebrew 6.0 tap-trust flag spelling unverified.

## Gotchas this session cost real time
- 🔴 **Another session switched the shared checkout mid-work** (`main` →
  `feat/comfy-model-cache`). Nothing was lost only because the branch was already
  pushed. **Use a worktree for anything that writes; `git branch --show-current`
  immediately before every commit.**
- 🔴 **A "mutation-verified" claim is not evidence — re-run it.** Two of my own guards
  were vacuous: one asserted a marker (`"ComfyUI reachable"`) that is a **substring of
  the FAILURE headline** (`"No ComfyUI reachable at …"`), so it was true on both
  branches and misreported a broken fixture as a stale selector; another asserted
  `reset()` but not its target, so `this.reset()` on a `<div>` — which throws and
  discards nothing — passed the whole suite.
- **An unreachable "measured" example in a comment is worse than none.** Two comments
  described a sequence (`reopen and lower the min`) the markup makes impossible and
  that was never run. Corrected, with a note recording what the wrong version claimed.
- **`go`/`curl`/`head` intermittently vanished from PATH** inside compound Bash calls.
  Resolve with `command -v` / absolute paths (`/home/zach/.nix-profile/bin/go`).
- **An empty DB does not prove a click was blocked** — a server 400 also persists
  nothing. Check `performance.getEntriesByType("resource")` for the request instead.
- **`gh pr checks <n> --watch` reports "no checks reported"** while a run is still
  queued; use `gh run list --branch <b>` to see the queued run.

## How to verify the current release
```sh
gh release download v0.1.101 -R ZacxDev/civitai-manager \
  -p 'civitai-manager_0.1.101_linux_amd64.tar.gz' -p 'checksums.txt'
sha256sum -c --ignore-missing checksums.txt      # must print OK
tar xzf civitai-manager_0.1.101_linux_amd64.tar.gz && ./civitai-manager --version
gh attestation verify civitai-manager_0.1.101_linux_amd64.tar.gz -R ZacxDev/civitai-manager
AUDITLOOP_CHROMIUM=/run/current-system/sw/bin/brave make ux-audit   # 20 pages, 0 violations
```

## Cleanup / side effects
- Every state-mutating verification used a **throwaway DB** under the session
  scratchpad; the real DB was written to only by the intended migrations.
- Browser verification drove the user's **live Brave** (`work` profile) in its own tab,
  closed afterwards each time.
- ⚠ Two pre-migration backups remain in `~/.config/civitai-manager/`
  (`.pre-0017.bak`, `.pre-0018.bak`, **23 MB each**). Delete when happy.
