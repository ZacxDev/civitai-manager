# civitai-manager — session handoff (2026-07-31)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.93**, `main` @ `1e2cee9`, clean & synced, migrations at **0018**, real DB at schema 18. **GOPRIVATE is NOT needed.** ⚠ **ONE branch was IN FLIGHT when the session ended — reconcile it BEFORE anything else** (see "Unfinished business"): `feat/library-dashboard`, a worktree agent that may be incomplete. `main` is `cb8e534` (a docs merge on top of the v0.1.93 tag at `1e2cee9`). Every 2026-07-30/31 feedback list is shipped; the remaining threads are pre-existing bugs found and deliberately not fixed. Standing OK to push+tag+release without asking — run the real gate (`go build ./... && go vet ./... && go test ./... && go test -race ./internal/web/... ./internal/comfy/... ./internal/store/... && gofmt -l ./internal/ ./e2e/ ./*.go`; **`gofmt` is NOT covered by `go vet`**, **`./cmd/` does not exist**, `./e2e/` must be named explicitly) + `/audit-pr` scaled to blast radius + **a delta re-audit after EVERY fix round**, then **push `main` BEFORE tagging**, verify the tarball, refresh the `:8972` dogfood. If `go.mod` changes at all, re-run `nix build .`. **You CAN drive a real browser** — it found a real bug in EVERY visual branch across three sessions. Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate → audit → delta re-audit → ship → verify tarball → refresh dogfood.

## 🔴 Unfinished business — do this FIRST
One branch is still outstanding; the docs one is already merged. `main` is clean and green.

1. **`feat/library-dashboard`** — worktree at `.claude/worktrees/agent-a7edf12b358a1eabd`, based on `1e2cee9`, **locked**, agent still running when the session ended. Scope: rescan moved into a button/modal, the scan card shown only when never-scanned or 0 models, duplicates+summary combined, chips with popover detail, matched/unmatched tabs, an improved update-available card, a larger subscribe button + an info popover explaining what subscribe does. **Treat it as possibly half-written** — the LSP was reporting a cascade of `undefined:` errors in `internal/web/library_pages.go` from that worktree as the session closed, which is either a mid-file agent or the usual stale-index false alarm. Follow `CLAUDE.md`'s mid-file recovery order: `git log 1e2cee9..HEAD` → `git status --porcelain` → `go build ./...` (the first compile error is usually the exact line the agent stopped on). This is UI + popovers + a modal → **browser-verify it**, and gate on the COMMITTED tree.
2. ~~`docs/session-learnings-2026-07-30`~~ — **merged to `main`** (`cb8e534`). Added this session's lessons to `CLAUDE.md`: the vacuous-guard-test taxonomy, the two browser/CSS debugging bullets, the `preload="metadata"` measurement, the shell tripwires, and a correction to the browser bullet that had outlived its own advice (it still said "`activate` is not optional" long after the skill demoted `activate` to a last-resort that steals the operator's screen — `wake` is the un-throttle). Delete the branch once you have looked at it.
- Also uncommitted, **outside this repo**: `~/workspace/devrc/scripts/browser-bridge/SKILL.md` (the real source behind `~/.claude/skills/browser/SKILL.md`, which is a read-only home-manager symlink). Records the sibling-agent tab collision + the "verify the page before trusting a screenshot" rule. That repo's commits are the user's to make.

## Current state
- **Latest release: v0.1.93** (`main` @ `1e2cee9`), clean, `HEAD == origin/main`, release published.
- `go.mod`/`go.sum` **untouched across the entire v0.1.84–v0.1.93 run** (`git log e0981e7..HEAD -- go.mod go.sum` is empty) — so `vendorHash` has needed no re-verification. Checked every release, not assumed.
- Dogfood **v0.1.93 on `:8972`** (pid 2317751), binary at `…/dbce7753-…/scratchpad/dogfood/cm` — **an OLD session's scratchpad; if that dir is GC'd, rebuild somewhere durable.**
- Real DB at **schema 18**; it is in **WAL mode**, so the `.db` file's mtime looks stale (Jul 28) while `-wal` is current — do not read that as "nothing has written".
- ⚠ **Two pre-migration backups are sitting in `~/.config/civitai-manager/`** — `civitai-manager.db.pre-0017.bak` and `.pre-0018.bak`, **23 MB each**. Both migrations were destructive only to the fail-open `community_cache`. **Delete them once you are happy**; nothing needs them.
- Untracked **`opencode.json`** still in the repo root; not mine, left alone.

## What shipped v0.1.84 → v0.1.93
- **v0.1.84** batch gallery · **v0.1.85** rail batch collapse · **v0.1.86** subgraph run inputs · **v0.1.87** first UI feedback list · **v0.1.88** second (header download, imported-workflows carousel, community NSFW, ecosystem-follows-selection) · **v0.1.89** import button icon+text with a heading that stops moving · **v0.1.90** the numeric-COMBO decode fix · **v0.1.91** video output capture · **v0.1.92** the PG..XXX maturity range · **v0.1.93** discovery UX (already-imported offers View, the version's own base model is named, use case is a picker).
- **Closed since the last handoff:** the numeric-COMBO blocker (was 🔴 open thread #1, fixed v0.1.90); the import-button decision (option C shipped v0.1.89, proposal marked RESOLVED); and the "no way to get a SFW community feed" default change — the maturity RANGE supersedes it, and content outside the band is now **never emitted** rather than blurred.

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
Full taxonomy is being written into `CLAUDE.md` (branch `docs/session-learnings-2026-07-30`); the short version, because it recurred all session:

`CLAUDE.md`'s existing rule ("a green guard is not evidence until you have proven it can FAIL") is **necessary and NOT sufficient**. Eleven guard tests were vacuous, each for a different reason, and **most sat over correct implementations** — so nothing was broken, but nothing was guarded either. The two missing halves:

- **Re-run the mutation YOURSELF.** Two agents reported "mutation-verified" for mutations that had never been run. And a `sed` that matches nothing looks *exactly* like a passing test — print the diff and confirm the mutation landed.
- **Confirm the fixture reaches the interesting case.** Assert the intermediate state, not just the outcome. Several tests passed because the guarded code path never executed at all.

The nastiest instance was **shared infrastructure**: a `title=` escape hatch in `TestLongUntrustedStringsCanBreak` that two agents hit independently within an hour, both watching a deleted `min-w-0` go undetected. Fixed at the checker; `internal/web/ux_audit_web_test.go` now carries a comment saying not to add it back.

## Cleanup / side effects
- Verifications wrote images + videos into the user's real ComfyUI output dir (`SaveImage` writes there; unavoidable). Left in place.
- The real DB was written to only by the intended 0017/0018 migrations; every seeded check used a throwaway temp DB.
- Browser verification drove the user's **live Brave** (`work` profile) in its own tab throughout.
- ⚠ **Session-limit deaths mid-task have now happened across three sessions.** All recovered mechanically because the tree stayed buildable. The danger is not a broken tree but a **half-written fix that looks finished** — which is exactly the risk on `feat/library-dashboard` above.
