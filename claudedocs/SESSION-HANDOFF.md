# civitai-manager — session handoff (2026-07-31)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.94**, `main` @ `010c367`, clean & synced, migrations at **0018**, real DB at schema 18, **nothing unmerged**. **GOPRIVATE is NOT needed.** Every 2026-07-30/31 feedback list is shipped, including the library-dashboard rework and the nav+/disks rework. The remaining threads are pre-existing or deliberately deferred — **read "Needs your decision" first, it has three real ones**. Standing OK to push+tag+release without asking — run the real gate (`go build ./... && go vet ./... && go test ./... && go test -race ./internal/web/... ./internal/comfy/... ./internal/store/... ./internal/diskusage/... && gofmt -l ./internal/ ./e2e/ ./*.go`; **`gofmt` is NOT covered by `go vet`**, **`./cmd/` does not exist**, `./e2e/` must be named explicitly) + `/audit-pr` scaled to blast radius + **a delta re-audit after EVERY fix round**, then **push `main` BEFORE tagging**, verify the tarball, refresh the `:8972` dogfood. If `go.mod` changes at all, re-run `nix build .`. **You CAN drive a real browser** — it found a real bug in EVERY visual branch across three sessions. Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate → audit → delta re-audit → ship → verify tarball → refresh dogfood.

## Unfinished business — NONE in this repo
Everything merged. Three branches (`docs/session-learnings-2026-07-30`, `feat/library-dashboard`, `feat/nav-and-disks`) are all in `main`; delete them and their worktrees when convenient.

- ⚠ Still uncommitted, **outside this repo**: `~/workspace/devrc/scripts/browser-bridge/SKILL.md` (the real source behind `~/.claude/skills/browser/SKILL.md`, which is a read-only home-manager **out-of-store symlink — the working copy IS the live skill**, so do NOT commit it to a branch and check `main` back out; that would revert the skill you are using). Records the sibling-agent tab collision + "verify the page before trusting a screenshot". That repo uses PRs; the commit is yours to make.

## Current state
- **Latest release: v0.1.94** (`main` @ `010c367`), clean, `HEAD == origin/main`. **Tarball verified** — `checksums.txt` OK, binary runs `0.1.94 (commit 010c367…)`, `gh attestation verify` on the TARBALL exits 0.
- `go.mod`/`go.sum` **untouched across the entire v0.1.84–v0.1.94 run** — so `vendorHash` has needed no re-verification. Checked every release, not assumed.
- Dogfood **v0.1.94 on `:8972`** — the RELEASED binary, at `…/c54c3347-…/scratchpad/dogfood/cm` (moved off the old dead session's scratchpad). Live-verified: nav renders exactly `Apps · Disks · Find models · Find workflows · Library▾(Model files, Workflows)`, `/disks` 200, `/trash` 302, `/assets/favicon.svg` 200.
- ⚠ **Kill it by `pgrep -x cm` + an `/proc/<pid>/exe` check, never `pkill -f`** — `pgrep -f 'dogfood/cm serve'` matches its own shell and killed my command mid-swap this session (exit 144), despite that trap being documented in `CLAUDE.md` the same day.
- Real DB at **schema 18**; it is in **WAL mode**, so the `.db` file's mtime looks stale (Jul 28) while `-wal` is current — do not read that as "nothing has written".
- ⚠ **Two pre-migration backups are sitting in `~/.config/civitai-manager/`** — `civitai-manager.db.pre-0017.bak` and `.pre-0018.bak`, **23 MB each**. Both migrations were destructive only to the fail-open `community_cache`. **Delete them once you are happy**; nothing needs them.
- Untracked **`opencode.json`** still in the repo root; not mine, left alone.

## What shipped v0.1.84 → v0.1.94
- **v0.1.84** batch gallery · **v0.1.85** rail batch collapse · **v0.1.86** subgraph run inputs · **v0.1.87** first UI feedback list · **v0.1.88** second (header download, imported-workflows carousel, community NSFW, ecosystem-follows-selection) · **v0.1.89** import button icon+text with a heading that stops moving · **v0.1.90** the numeric-COMBO decode fix · **v0.1.91** video output capture · **v0.1.92** the PG..XXX maturity range · **v0.1.93** discovery UX (already-imported offers View, the version's own base model is named, use case is a picker) · **v0.1.94** the library-dashboard rework (rescan modal, one status card with chip popovers, matched/unmatched tabs, bigger subscribe + info popover) AND the nav rework + a new `/disks` page.
- **Closed since the last handoff:** the numeric-COMBO blocker (was 🔴 open thread #1, fixed v0.1.90); the import-button decision (option C shipped v0.1.89, proposal marked RESOLVED); and the "no way to get a SFW community feed" default change — the maturity RANGE supersedes it, and content outside the band is now **never emitted** rather than blurred.

## 🔴 Needs YOUR decision — three real ones, all raised by the v0.1.94 audits
1. **`/disks` has no timeout and no `context`.** `diskRows()` calls `syscall.Statfs` serially on the request goroutine for every configured directory. A hung NFS/CIFS mount blocks in **uninterruptible sleep** — the goroutine cannot be cancelled — so `/disks` hangs forever and leaks a goroutine per reload. `internal/cli/commands.go` sets only `ReadHeaderTimeout`, no `WriteTimeout`. Loopback-only + single-user caps the severity, and directory count is user-controlled (`library_paths` + the scan-dir table) so fan-out is unbounded. A bounded worker pool or a per-probe watchdog is cheap. **Deliberately not fixed — it changes behaviour.**
2. **Swap the nav dropdown from `<details>` to `popover`?** `<details>` gives keyboard support and needs no JS, but provides **neither click-outside-to-close nor Escape** — so below 1024px the panel is a `position: fixed` full-width sheet dismissable only by finding the summary again. `popover` gives light-dismiss + Escape natively and keeps the no-JS property. Both agents recommended the swap; neither made it, because it is a real interaction change to primary navigation needing browser verification at both breakpoints.
3. **The home page's `<h1>` still reads "Dashboard"** even though the nav item is gone (verified live: it is the heading, not a nav link). Rename it, or leave it as the page's own name?

Related, **pre-existing and NOT introduced by this work**, but worth knowing since v0.1.94 is where the "no path off-loopback" property gets written down:
- **`extraPathsAllowed()` keys on the configured BIND ADDRESS, not the peer.** Not header-spoofable (probed with `X-Forwarded-For`, `X-Real-IP`, `Host: localhost` — all still gated), but a reverse proxy in front of a `127.0.0.1` bind looks local to every remote caller. Shared with `/library/browse` and `/library/scan`, so `/disks` adds no divergence — it does newly put absolute paths behind it.
- **`POST /trash/{id}/restore` leaks original paths in its FAILURE TEXT**, off-loopback, proven with a real 200 (`internal/library/quarantine.go:472,475,503` wrap `f.OriginalPath`; rendered raw by `library_handlers.go:637`).

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

## Cleanup / side effects
- Verifications wrote images + videos into the user's real ComfyUI output dir (`SaveImage` writes there; unavoidable). Left in place.
- The real DB was written to only by the intended 0017/0018 migrations; every seeded check used a throwaway temp DB.
- Browser verification drove the user's **live Brave** (`work` profile) in its own tab throughout.
- ⚠ **Session-limit deaths mid-task have now happened across three sessions.** All recovered mechanically because the tree stayed buildable. The danger is not a broken tree but a **half-written fix that looks finished** — which is exactly the risk on `feat/library-dashboard` above.
