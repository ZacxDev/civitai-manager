# civitai-manager — session handoff (2026-07-31)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.96**, `main` @ `be85cbf`, clean & synced, migrations at **0018**, real DB at schema 18, **nothing unmerged**. **GOPRIVATE is NOT needed.** Every 2026-07-30/31 feedback list is shipped, including the library-dashboard rework and the nav+/disks rework. The remaining threads are pre-existing or deliberately deferred — **the three decisions from v0.1.94 are all CLOSED and shipped in v0.1.95**. Standing OK to push+tag+release without asking — run the real gate (`go build ./... && go vet ./... && go test ./... && go test -race ./internal/web/... ./internal/comfy/... ./internal/store/... ./internal/diskusage/... && gofmt -l ./internal/ ./e2e/ ./*.go`; **`gofmt` is NOT covered by `go vet`**, **`./cmd/` does not exist**, `./e2e/` must be named explicitly) + `/audit-pr` scaled to blast radius + **a delta re-audit after EVERY fix round**, then **push `main` BEFORE tagging**, verify the tarball, refresh the `:8972` dogfood. If `go.mod` changes at all, re-run `nix build .`. **You CAN drive a real browser** — it found a real bug in EVERY visual branch across three sessions. Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate → audit → delta re-audit → ship → verify tarball → refresh dogfood.

## Unfinished business — NONE in this repo
Everything merged. Three branches (`docs/session-learnings-2026-07-30`, `feat/library-dashboard`, `feat/nav-and-disks`) are all in `main`; delete them and their worktrees when convenient.

- ⚠ Still uncommitted, **outside this repo**: `~/workspace/devrc/scripts/browser-bridge/SKILL.md` (the real source behind `~/.claude/skills/browser/SKILL.md`, which is a read-only home-manager **out-of-store symlink — the working copy IS the live skill**, so do NOT commit it to a branch and check `main` back out; that would revert the skill you are using). Records the sibling-agent tab collision + "verify the page before trusting a screenshot". That repo uses PRs; the commit is yours to make.

## Current state
- **Latest release: v0.1.96** (`main` @ `be85cbf`), clean, `HEAD == origin/main`. **Tarball verified** — `checksums.txt` OK, binary runs `0.1.96 (commit be85cbf…)`, `gh attestation verify` on the TARBALL exits 0.
- `go.mod`/`go.sum` **untouched across the entire v0.1.84–v0.1.96 run** — so `vendorHash` has needed no re-verification. Checked every release, not assumed.
- Dogfood **v0.1.96 on `:8972`** — the RELEASED binary, at `…/c54c3347-…/scratchpad/dogfood/cm` (moved off the old dead session's scratchpad). Live-verified on the RELEASED binary: `h1` reads Overview (Dashboard gone), the Library menu is a `popover` (no `<details>` in the nav), `/disks` 200.
- ⚠ **Kill it by `pgrep -x cm` + an `/proc/<pid>/exe` check, never `pkill -f`** — `pgrep -f 'dogfood/cm serve'` matches its own shell and killed my command mid-swap this session (exit 144), despite that trap being documented in `CLAUDE.md` the same day.
- Real DB at **schema 18**; it is in **WAL mode**, so the `.db` file's mtime looks stale (Jul 28) while `-wal` is current — do not read that as "nothing has written".
- ⚠ **Two pre-migration backups are sitting in `~/.config/civitai-manager/`** — `civitai-manager.db.pre-0017.bak` and `.pre-0018.bak`, **23 MB each**. Both migrations were destructive only to the fail-open `community_cache`. **Delete them once you are happy**; nothing needs them.
- Untracked **`opencode.json`** still in the repo root; not mine, left alone.

## What shipped v0.1.84 → v0.1.96
- **v0.1.84** batch gallery · **v0.1.85** rail batch collapse · **v0.1.86** subgraph run inputs · **v0.1.87** first UI feedback list · **v0.1.88** second (header download, imported-workflows carousel, community NSFW, ecosystem-follows-selection) · **v0.1.89** import button icon+text with a heading that stops moving · **v0.1.90** the numeric-COMBO decode fix · **v0.1.91** video output capture · **v0.1.92** the PG..XXX maturity range · **v0.1.93** discovery UX (already-imported offers View, the version's own base model is named, use case is a picker) · **v0.1.94** the library-dashboard rework (rescan modal, one status card with chip popovers, matched/unmatched tabs, bigger subscribe + info popover) AND the nav rework + a new `/disks` page · **v0.1.95** the `/disks` hung-mount watchdog, the nav dropdown as a `popover`, and the home page renamed Overview · **v0.1.96** the workflow list de-duped by source (70 workflows → 36 items), the reveal control stopped lying, prompt inputs visible on load, and 🔴 a workflow post is no longer treated as downloadable.
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
4. `internal/cli/backfill_workflow_post_test.go` — its docstring claims it guards iota renumbering. It cannot (both sides reference constants by name); it *does* catch a duplicated/missing switch arm. Fix the docstring.

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
