# civitai-manager — session handoff (2026-08-01)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.98**, local `main` @ `a0dd46a` — **5 commits AHEAD of `origin/main`, unpushed** — **3 open PRs (#32/#33/#34), already integration-verified safe together**, migrations at **0018**, real DB at schema 18. **GOPRIVATE is NOT needed.** 🔴 **Do these first, in this order: (1) push `main`, (2) merge #32/#33/#34, (3) re-gate the merged tree, (4) run the axe audit** — #32 expands the walk, so that run is the first time the harness sees more than 3 of ~16 surfaces. ⚠ **`~/workspace/devrc` has TWO uncommitted tracked files** — `scripts/browser-bridge/SKILL.md` (live NOW) and `claude/RULES.md` (needs a home-manager switch). That repo uses PRs; ask me before committing there. ⚠ **Two things are waiting on MY decision** (see the handoff): an Apply button for the maturity control (every arrow keypress currently reloads and loses focus), and whether to PR the devrc edits. Standing OK to push+tag+release without asking — run the real gate (`go build ./... && go vet ./... && go test ./... && go test -race ./internal/web/... ./internal/comfy/... ./internal/store/... ./internal/diskusage/... && gofmt -l ./internal/ ./e2e/ ./*.go`; **`gofmt` is NOT covered by `go vet`**, **`./cmd/` does not exist**, `./e2e/` must be named explicitly — and `e2e/uxaudit` is a NESTED module a root `go test ./...` never compiles) + `/audit-pr` scaled to blast radius + **a delta re-audit after EVERY fix round**, then **push `main` BEFORE tagging**, verify the tarball, refresh the `:8972` dogfood (kill by `pgrep -x cm` + `/proc/<pid>/exe`, wait until NO cm process remains — a free port is not a released binary — then verify the served build by pid + `--version`). If `go.mod` changes at all, re-run `nix build .`. **You CAN drive a real browser** — it found a real bug in EVERY visual branch across four sessions. 🔴 **In a shared checkout, run `git branch --show-current` immediately before any commit** — a subagent's `git checkout -b` landed my handoff on its branch last session and `main` never moved. Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate → audit → delta re-audit → ship → verify tarball → refresh dogfood.

## 🔴 Three open PRs — VERIFIED SAFE TOGETHER, but read this before merging
All three are `MERGEABLE/CLEAN` with green CI, and another agent opened them from `claudedocs/BREADCRUMBS-AND-COPY-DESIGN.md`:

| PR | branch | what | size |
|---|---|---|---|
| **#32** | `uxaudit-walk-expand` | expands the axe walk to search / creator / model-detail | +35/−1, 2 files |
| **#33** | `feat/breadcrumbs` | breadcrumbs on the 4 detail pages the design doc identified | +127/−31, 7 files |
| **#34** | `fix/copy-reduction` | cuts duplicate copy, drops redundant `aria-label`s | +15/−77, 17 files |

🔴 **Per-PR green proves nothing about the merged tree** — so I built the integration branch and checked. **Result: clean.** All three merge without conflict, and on the merged tree `go build`/`go vet`/`gofmt`/`go test ./...`, `go test -race ./internal/web/...`, **and the nested `e2e/uxaudit` module** are all green. That last one matters: `go test ./...` does **not** reach `e2e/uxaudit`, and **#32 and #34 both edit `e2e/uxaudit/walk.go`**.

⚠ **#32 and #33 are based on `origin/main`, which is 2 commits behind local `main`** (see Current state). Push local `main` first, then merge, then re-gate — the integration check above was run against local `main`, so it already reflects the correct order.

## ⚠ Unsaved work OUTSIDE this repo — two files in `devrc`, both uncommitted
`~/workspace/devrc` has **two modified tracked files**, neither committed. Your `RULES.md` treats a doc/lesson sitting in a working tree as one routine `checkout` away from silent, unreported deletion, and that repo uses PRs:
- `scripts/browser-bridge/SKILL.md` — the re-throttle-after-reload warning (htmx silently stops firing until you `wake` again). **This edit is LIVE right now** — see the resolution rule below.
- `claude/RULES.md` — widens the shared-checkout branch rule to cover `commit`, and records the live-vs-copy test. **This edit is NOT live until a home-manager switch.**

🔴 **How to tell whether a managed-dotfile edit is live: `readlink -f <path>`.** Terminates inside `~/workspace/devrc` → the working copy IS the live file (`mkOutOfStoreSymlink`), edit is immediate. Terminates in `/nix/store` → it is a read-only copy, needs a switch. An agent this session asserted the browser skill was a copy because the two files were byte-identical — identity was simply the consequence of their being **one file**. `diff` cannot answer this; `readlink -f` can.

## 🔴 A subagent switched the shared checkout and a commit landed on ITS branch
Recorded because it nearly cost the handoff, and because the global rule did not cover it.

A docs subagent ran `git checkout -b <branch>` **in the shared main checkout** (docs-only work, so no worktree isolation). The parent then wrote this handoff and committed it — **onto the agent's branch**, believing it was on `main`. There is no error, no conflict, and `git log` afterwards looks exactly right, because you are reading the branch you accidentally landed on. `main`'s own reflog showed it had **never moved**.

Nothing was lost only because it was noticed. **A `git push origin main` at that moment would have reported success and silently left the handoff behind.** The fix was a fast-forward; the habit that prevents it is `git branch --show-current` immediately before any commit in a shared checkout, and `git reflog` is the one-command diagnosis when a branch looks like it moved backwards.

⚠ The global rule covers `pull`/`rebase`/`checkout` by "other sessions" — **not `commit`, and not your own subagent**. Both halves are being widened.

## Current state
- **Latest release: v0.1.98** (tag = `47c7cb0` = `origin/main`). **Tarball verified** — `checksums.txt` OK, binary runs `0.1.98 (commit 47c7cb0…)`, `gh attestation verify` on the TARBALL exits 0.
- ⚠ **Local `main` (`297ccd2`) is 2 commits AHEAD of `origin/main` and unpushed.** Both close findings from `claudedocs/BREADCRUMBS-AND-COPY-DESIGN.md`: `98a9d13` deletes the unreachable `trashPage`/`handleTrash`, `297ccd2` drops the `title=` that raced the CSS popover on `versionStatusFragment`. Gated green. **Push these before merging the PRs** — two of the three are based on the older `origin/main`.
- `go.mod`/`go.sum` **untouched across the entire v0.1.84–v0.1.98 run** — so `vendorHash` has needed no re-verification. Checked every release, not assumed.
- Dogfood **v0.1.98 on `:8972`** — the RELEASED binary, at `…/c54c3347-…/scratchpad/dogfood/cm` (moved off the old dead session's scratchpad). Live-verified on the RELEASED binary: `h1` reads Overview (Dashboard gone), the Library menu is a `popover` (no `<details>` in the nav), `/disks` 200.
- ⚠ **Kill it by `pgrep -x cm` + an `/proc/<pid>/exe` check, never `pkill -f`** — `pgrep -f 'dogfood/cm serve'` matches its own shell and killed my command mid-swap this session (exit 144), despite that trap being documented in `CLAUDE.md` the same day.
- Real DB at **schema 18**; it is in **WAL mode**, so the `.db` file's mtime looks stale (Jul 28) while `-wal` is current — do not read that as "nothing has written".
- ⚠ **Two pre-migration backups are sitting in `~/.config/civitai-manager/`** — `civitai-manager.db.pre-0017.bak` and `.pre-0018.bak`, **23 MB each**. Both migrations were destructive only to the fail-open `community_cache`. **Delete them once you are happy**; nothing needs them.
- Untracked **`opencode.json`** still in the repo root; not mine, left alone.

## What shipped v0.1.84 → v0.1.98
- **v0.1.84** batch gallery · **v0.1.85** rail batch collapse · **v0.1.86** subgraph run inputs · **v0.1.87** first UI feedback list · **v0.1.88** second (header download, imported-workflows carousel, community NSFW, ecosystem-follows-selection) · **v0.1.89** import button icon+text with a heading that stops moving · **v0.1.90** the numeric-COMBO decode fix · **v0.1.91** video output capture · **v0.1.92** the PG..XXX maturity range · **v0.1.93** discovery UX (already-imported offers View, the version's own base model is named, use case is a picker) · **v0.1.94** the library-dashboard rework (rescan modal, one status card with chip popovers, matched/unmatched tabs, bigger subscribe + info popover) AND the nav rework + a new `/disks` page · **v0.1.95** the `/disks` hung-mount watchdog, the nav dropdown as a `popover`, and the home page renamed Overview · **v0.1.96** the workflow list de-duped by source (70 workflows → 36 items), the reveal control stopped lying, prompt inputs visible on load, and 🔴 a workflow post is no longer treated as downloadable · **v0.1.97** ONE run zone (count folded in, local-vs-cloud as one destination), `/` is search, `/subscriptions` replaces the old home, the subscribe panel says what it actually downloads, and the workflow list de-dupes by source · **v0.1.98** dark-only shell (light CSS retained dormant), the maturity control as an icon button + popover, and the outputs rail moved LEFT as a multi-widget container with a coalesced activity widget.
- **Closed since the last handoff:** the numeric-COMBO blocker (was 🔴 open thread #1, fixed v0.1.90); the import-button decision (option C shipped v0.1.89, proposal marked RESOLVED); and the "no way to get a SFW community feed" default change — the maturity RANGE supersedes it, and content outside the band is now **never emitted** rather than blurred.

## Decisions from v0.1.94 — all CLOSED in v0.1.95
1. ~~`/disks` has no timeout~~ — **shipped.** Per-probe goroutine behind a 2s watchdog, 8 concurrent, and a 30s memo of any path that timed out. The memo is the load-bearing half: a blocked `statfs` cannot be cancelled, so without it every reload spawns another stuck goroutine. Result channel is **buffered cap 1** so an abandoned probe can complete its send and exit. Audited: the worker pool is **not** exhaustible by N hung paths (a slot is held at most 2s; the abandoned goroutine is separate).
2. ~~`<details>` → `popover`~~ — **shipped**, and it removed two hacks rather than adding one: the panel needs no `z-index` (top layer paints above every stacking context) and no `overflow` help.
3. ~~`h1` "Dashboard"~~ — **shipped** as "Overview", from one `homePageTitle` constant feeding both `<title>` and `h1`.

## ⚠ Known limitations shipped in v0.1.95 — read before touching the nav
- **Firefox and Safari do not have CSS anchor positioning**, so at ≥1024px they get the **full-width sheet** instead of the anchored dropdown. Verified *usable* (on-screen, unclipped, light-dismissable) by deleting the `@supports` block from the live CSSOM in Chromium — but **neither engine was actually run**. If you have a Firefox handy, that is the one unverified surface.
- **Only 1198×921 was browser-tested.** The bridge has no resize/emulation op, so the <1024px rules were exercised by *forcing* them with a probe stylesheet at desktop width. That verifies the CSS and the clipping claim, **not** a true 390px nav render.
- 🔴 **`.cm-navlinks { overflow: visible }` at ≥1024px is for the FOCUS RINGS, not the panel — do not delete it again.** It was deleted on the correct reasoning that the top layer cannot be clipped by ancestor overflow; that is true of the panel and false of every other control in the strip, whose focus rings are ink overflow. Measured headroom without it: **0.5px** for the links, **0px** for the Library trigger. The guard test had *cemented* the bug by forbidding the declaration — it is now inverted, with the panel-independence claim moved onto the assertions that actually express it.

## Test debt from the v0.1.96 audit — shipped code is CORRECT, the guards are missing
Recorded because "the code is right but nothing pins it" is exactly how a later refactor breaks it silently.
1. **`internal/poller/poller.go`** — the claim that a permanently-skipped workflow version is *still marked seen and still notified on* (the reason a notify-only subscription keeps working) is guarded by **nothing**. Proved: wrapping the `new_version` AddEvent in `if outcome == outcomeEnqueued` leaves the suite GREEN. `res.NewCount` is assigned before the candidate loop, so it measures the diff, not the notification. Same gap covers base-model- and size-filtered candidates.
2. **`internal/web/run_preset_pages.go`** — the "a graph with no prompt input must not collapse 100% of its params" fallback is unguarded; inverting it stays green.
3. **`internal/web/handlers.go` `handleSubscribe`** — the DASHBOARD subscribe form never consults `workflowPostFlag`, so pasting a workflow post's URL there still stores `AutoDownload=true`. No download happens (the poller guard holds), so it is cosmetic dishonesty on an untouched surface — but it is the exact shape v0.1.96 removed everywhere else.
4. **`run_handlers.go`** — nothing guards the UI-format → `/run/queue` wiring: mutating `canQueue := false` makes the whole batching feature inert (pick ×8, get 1 run, no notice) with the **entire `internal/web` suite green**. The inverse IS caught, so coverage is asymmetric. ~4 lines to fix.
5. **`run_zone_web_test.go`** — one guard is vacuous: `newTestServer` has no `comfy_url`, so the fragment renders the unconfigured branch with no button and no `hx-post` at all, and the assertion cannot fail.
6. **`run_preset_pages.go`** — a comment claims "pressing Enter in a text field still runs", but the form now has no submit button and several blocking fields, so implicit submission does not fire. Harmless (one run control is the goal) but the comment and a test rationale both assert a path that mostly does not exist.
7. `internal/cli/backfill_workflow_post_test.go` — its docstring claims it guards iota renumbering. It cannot (both sides reference constants by name); it *does* catch a duplicated/missing switch arm. Fix the docstring.

## 🔴 Three latent bugs v0.1.97 fixed — found by READING hx-include, not from any brief
Worth recording because none was reported as a bug; they surfaced while restructuring.
1. **The prominent "Generate" button silently discarded every Parameters edit.** It `hx-include`d only `#run-modes`, while a *less* prominent "Run with these parameters" submit honoured them — two buttons, different behaviour, the visible one throwing work away. Guarded; reverting the include reddens `TestPrimaryRunControlCarriesTheParametersAndTheCount`. Live-proved in a browser by intercepting the real XHR body.
2. **A count of 1 overwrote the seed you could see.** `/run/queue` passed seed keys unconditionally — harmless while only "Queue ×N" reached it, wrong the moment it became the ordinary Generate click. `TestSingleRunKeepsTheVisibleSeed`.
3. **Batching did not exist for a UI workflow with no editable parameters** — the old block lived *inside* the preset form.

## ⚠ The nodepack blocker's detector is weak — do not re-assert
`ResolveCustomNode` means only "absent from `coreNodeClasses`", a ~50-entry hand-written table whose own comment says false-positives are fine *because the user reviews the list*. Measured against a live ComfyUI (2462 types) and a real 70-workflow library: **790 built-ins exist, the table knows 47, and 44 of 70 workflows (62%) contain a real built-in it calls custom** (`WanImageToVideo` in 14, `CLIPVisionLoader` in 6). The banner's headline is therefore **conditional**, and `cloud_pages.go` says why. To make it an assertion again, first make the detector authoritative — `/object_info` distinguishes `comfy_extras.*`/`nodes` from `custom_nodes.*`.

## 🔴 v0.1.98's audit found a content-gating control FAILING OPEN — read this before touching the maturity control
- **One arrow keypress in the *reducing* direction raised the ceiling to XXX.** Native radio groups **wrap**, and a `disabled` member is *skipped* — so at a boundary the skip lands on the far end of the scale. Measured live: band "X only" → **ArrowLeft** on the max track → committed and persisted **"X to XXX"**, with the reload dumping focus to `<body>` so nothing announced it. The two `<select>`s this replaced could not do that: they **omitted** out-of-range options, and a `<select>` does not wrap. **Fixed** by emitting NO input for an out-of-band stop; guarded by asserting the *absence* of an input plus the converse (every valid band stays reachable).
- **The inverted-range guard could never have caught it** — `x:xxx` IS a valid band. It asks "does every reachable stop yield a valid range?", never "can a keypress reach a stop the user did not intend?".
- **The popover guard was vacuous**: `Contains(out, " popover")` is satisfied by the *trigger's* `popovertarget`, so a panel with no `popover` attribute — **a permanently-open sheet across every page** — passed the whole suite. Now asserted against the panel element.
- 🟡 **STILL OPEN:** every arrow keypress commits → `HX-Refresh` → full reload → focus lost. Moving XXX→PG is four reloads. An explicit **Apply button** would fix this; it is a design change, deliberately not made unilaterally.

## 🔴 A remote description could inject an `<h1>` — fixed, and the guard that missed it is instructive
`bluemonday`'s `UGCPolicy` allows `h1`–`h6`, so a CivitAI model description put its own `<h1>` into the page (measured: **two `<h1>`s** on `/models/1386234` and `/models/4384`). Descriptions render under an `<h2>`, so they are demoted to `h3`.
⚠ **The upstream comment lied**: `policies.go` says h1–h6 "take no attributes", but `<h1 class="x" id="y" onclick="alert(1)">` sanitizes to `<h1 id="y">` — `id` survives. A `strings.ReplaceAll("<h1>")` therefore misses every heading carrying one and yields **mismatched open/close tags**. And `TestEveryFullPageHasExactlyOneH1` could not see any of it: its fixture has an **empty description**, so the injected heading never reached what it measures.

## 🔴 Open threads — all PRE-EXISTING, found and deliberately not fixed
1. 🔴 **Bypassing a subgraph instance does not bypass it.** Measured: with instance 93 at mode 4, `flattenSubgraphs` still emits **all 7 interior clones at `mode=0`**, because `convert.go:158-167` flattens BEFORE the mode drop at `:198` and each clone carries the *interior* node's own mode. v0.1.86's refusal to expose bypassed interiors is conservative in the right direction; its comment records the measurement.
2. **4 custom-sampler workflows still expose no seed.** `runInputLayouts` yields `RunInputSeed` for exactly `KSampler`/`KSamplerAdvanced`. The design questions are real (which widget is the seed on `SamplerCustomAdvanced`?).
3. **The 64 MiB per-output cap is silent on oversize** — a larger captured output is dropped with no user-visible signal.
4. **`.cm-updated-pop` does not flip near the right viewport edge** (`left: 0`, no flip). Bucketing to 6 tabs made it rarer, not gone.

## Open threads — smaller
5. Per-resource DB query in the workflow list (`workflowUsesChips` → `resolver.resource`): 3 queries instead of 2, not a new N+1 class. Fix is a per-request basename memo on the shared resolver.
6. `discover_facets.go` pre-existing: 260 facet combos vs `maxFacetFeedEntries = 256`; `facetFeed` has no singleflight.
7. API-format workflows capture no prompt (`DetectRunInputs` needs `widgets_values`), so output provenance is UI-format-only.
8. `/models/<id>?version=<a version of ANOTHER model>` renders that version's data throughout and the ecosystem section resolves from it. Not reachable from any UI control; the section is no more wrong than the rest of the page.
9. Two non-brand light-theme AA failures remain by decision (`.cm-size-large` 2.24:1, muted-on-tint 4.12:1).
10. macOS UNVERIFIED end-to-end; Homebrew 6.0 tap-trust flag spelling unverified; launch drafts written not posted (r/StableDiffusion blacklists the literal `nsfw` in title AND body).

## 🔴 The session's central finding — 11 green tests that proved nothing
Full taxonomy is now **in `CLAUDE.md`** (merged); the short version, because it kept recurring — including *after* the rule was written:

`CLAUDE.md`'s existing rule ("a green guard is not evidence until you have proven it can FAIL") is **necessary and NOT sufficient**. Eleven guard tests were vacuous, each for a different reason, and **most sat over correct implementations** — so nothing was broken, but nothing was guarded either. The two missing halves:

- **Re-run the mutation YOURSELF.** Two agents reported "mutation-verified" for mutations that had never been run. And a `sed` that matches nothing looks *exactly* like a passing test — print the diff and confirm the mutation landed.
- **Confirm the fixture reaches the interesting case.** Assert the intermediate state, not just the outcome. Several tests passed because the guarded code path never executed at all.

**It kept happening after the rule was written.** The nav+disks branch shipped a guard whose own comment claimed it proved the loopback gate stops the *syscalls* — it never called the handler, and rewriting `handleDisks` into the exact probe-then-hide shape it forbade left it green. That was the audit's only merge gate, and it arrived inside a "20 mutation-verified guards" self-report. Two more of that branch's guards (the shim wiring, and both browser-found light-theme CSS fixes) had no coverage at all.

⚠ **A twelfth mode, learned the hard way this session: a mutation caught by the COMPILER proves nothing about your test.** Mutating `addLazy(...)` → `add(...)` left `addLazy` unused, so the package failed to build — which looks like a red test in a grep-filtered log but exercises no assertion. The real regression compiles. Re-run it as a *semantic* mutation (drop the flag's value, or make the branch that reads it unreachable) and confirm the package still builds before believing the red.

The nastiest instance was **shared infrastructure**: a `title=` escape hatch in `TestLongUntrustedStringsCanBreak` that two agents hit independently within an hour, both watching a deleted `min-w-0` go undetected. Fixed at the checker; `internal/web/ux_audit_web_test.go` now carries a comment saying not to add it back.

## ⚠ Dogfood swap: the port going 000 is NOT the same event as the file being released
`cp` the new binary and you can still hit **"Text file busy"** — the old process
released the listening socket but not its executable image. That happened this
session and the restart silently relaunched the **OLD** binary, which answered
200 and looked completely healthy. Wait until `pgrep -x cm` returns **nothing**
(not until the port returns `000`), then `cp`, then start — and confirm the
served build by pid + `/proc/<pid>/exe` + `--version`, never by a 200.

## Cleanup / side effects
- Verifications wrote images + videos into the user's real ComfyUI output dir (`SaveImage` writes there; unavoidable). Left in place.
- The real DB was written to only by the intended 0017/0018 migrations; every seeded check used a throwaway temp DB.
- Browser verification drove the user's **live Brave** (`work` profile) in its own tab throughout.
- ⚠ **Session-limit deaths mid-task have now happened across three sessions.** All recovered mechanically because the tree stayed buildable. The danger is not a broken tree but a **half-written fix that looks finished** — which is exactly the risk on `feat/library-dashboard` above.
