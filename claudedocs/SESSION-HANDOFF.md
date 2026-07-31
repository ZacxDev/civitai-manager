# civitai-manager — session handoff (2026-07-31)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.88**, `main` @ `36cbc78`, clean & synced, migrations at **0017**, ONE worktree and ONE branch. **GOPRIVATE is NOT needed.** Both 2026-07-30 UI feedback lists are shipped. **One thing is waiting on YOU: `claudedocs/import-workflows-button-proposal.md` — three options, nothing built, pick one.** The remaining threads are pre-existing bugs found and deliberately not fixed; **#1 blocks the app's own run path.** Standing OK to push+tag+release without asking — run the real gate (`go build ./... && go vet ./... && go test ./... && go test -race ./internal/web/... ./internal/comfy/... ./internal/store/... && gofmt -l ./internal/ ./e2e/ ./*.go`; **`gofmt` is NOT covered by `go vet`**, **`./cmd/` does not exist**, `./e2e/` must be named explicitly) + `/audit-pr` scaled to blast radius + **a delta re-audit after EVERY fix round**, then **push `main` BEFORE tagging**, verify the tarball, refresh the `:8972` dogfood. If `go.mod` changes at all, re-run `nix build .`. **You CAN drive a real browser** — it found a real bug in EVERY visual branch across two sessions. Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate → audit → delta re-audit → ship → verify tarball → refresh dogfood.

## Current state
- **Latest release: v0.1.88** (`main` @ `36cbc78`), clean and synced. **Tarball verified** — checksum OK, binary runs `0.1.88 (commit 36cbc78…)`, `gh attestation verify` on the TARBALL exits 0 (silently; the exit code is the signal — verifying the *extracted binary* 404s by design).
- `go.mod`/`go.sum` **untouched across v0.1.84–v0.1.88** — `vendorHash` has needed no re-verification. Checked every release, not assumed.
- **Repo tidy**: ONE worktree, ONE branch, one server (the dogfood).
- Dogfood **v0.1.88 on `:8972`** (binary at `…/dbce7753-…/scratchpad/dogfood/cm` — an OLD session's scratchpad; if that dir is GC'd, rebuild somewhere durable).
- ⚠ **Migration 0017 ran against the real DB** (first destructive migration to touch it). `community_cache` was dropped and recreated with `nsfw` in the PK; it is a pure fail-open cache, so the cost was one refetch. **A pre-migration backup is at
  `~/.config/civitai-manager/civitai-manager.db.pre-0017.bak`** (23 MB, schema 16) — delete it once you are happy.
- Untracked **`opencode.json`** still in the repo root; not mine, left alone.

## What shipped v0.1.84 → v0.1.88
- **v0.1.84** batch gallery · **v0.1.85** rail batch collapse · **v0.1.86** subgraph run inputs (13 workflows can finally batch) · **v0.1.87** the first UI feedback list · **v0.1.88** the second: model download in the header, imported-workflows carousel, community NSFW, ecosystem-follows-selection.

## 🔴 Open threads — all PRE-EXISTING, found and deliberately not fixed
1. 🔴 **`InputSpec.UnmarshalJSON` mishandles a NUMERIC combo — it blocks the app's own run path.** `internal/comfy/client.go:264`: `json.Unmarshal(arr[0], &s.Choices)` on `[0.25,0.5,1.0,2.0,4.0]` does NOT leave `Choices` nil as its comment claims — `encoding/json` errors AND leaves **five empty strings**. `detectBadOptions` then raises an **unfixable BadOption with five blank fix options**. Hits ANY workflow with a numeric COMBO; it halted a real batch at item 1 on `RIFE VFI · scale_factor`.
2. 🔴 **Bypassing a subgraph instance does not bypass it.** Measured: with instance 93 at mode 4, `flattenSubgraphs` still emits **all 7 interior clones at `mode=0`**, because `convert.go:158-167` flattens BEFORE the mode drop at `:198` and each clone carries the *interior* node's own mode. v0.1.86's refusal to expose bypassed interiors is conservative in the right direction; its comment records the measurement.
3. **4 custom-sampler workflows still expose no seed.** `runInputLayouts` yields `RunInputSeed` for exactly `KSampler`/`KSamplerAdvanced`. Design questions are real (which widget is the seed on `SamplerCustomAdvanced`?).
4. **`.cm-updated-pop` does not flip near the right viewport edge** (`left: 0`, no flip). Bucketing to 6 tabs made it rarer, not gone.

## Needs YOUR decision
- **`claudedocs/import-workflows-button-proposal.md`** — three options with markup + screenshots, nothing built. Recommendation is **C**, on reasoning worth reading: the card is titled "Import workflows" before import and "Workflows" after, so the section appears to rename itself; C stabilises the heading and lets the button carry the verb. Also noted: the action is labelled TWICE today, and the button is already full-bleed by accident.
- ⚠ **The community feed's character changed on SFW models too, with no opt-out.** Measured: a SFW LoRA went `None×12` → `Mature 10 / X 1 / Soft 1`. Since the toggle is two-state and the level is a constant, there is now **no way to get a SFW community feed on any model page** (previously every feed was SFW by accident). Follows directly from the fix and everything is blurred by default — but it is a default change to sign off on, not discover. `Soft` is the milder alternative.

## Open threads — smaller
5. Per-resource DB query in the workflow list (`workflowUsesChips` → `resolver.resource`): 3 queries instead of 2, not a new N+1 class. Fix is a per-request basename memo on the shared resolver.
6. `discover_facets.go` pre-existing: 260 facet combos vs `maxFacetFeedEntries = 256`; `facetFeed` has no singleflight.
7. API-format workflows capture no prompt (`DetectRunInputs` needs `widgets_values`), so output provenance is UI-format-only.
8. `/models/<id>?version=<a version of ANOTHER model>` renders that version's data throughout and the ecosystem section resolves from it. Not reachable from any UI control; the section is no more wrong than the rest of the page.
9. Two non-brand light-theme AA failures remain by decision (`.cm-size-large` 2.24:1, muted-on-tint 4.12:1).
10. macOS UNVERIFIED end-to-end; Homebrew 6.0 tap-trust flag spelling unverified; launch drafts written not posted (r/StableDiffusion blacklists the literal `nsfw` in title AND body); `devrc` changes LIVE but UNCOMMITTED.

## 🔴 SIX green tests that proved nothing — and the one that was shared infrastructure
Every one was written by a careful agent; most sat over **correct** implementations. `CLAUDE.md`'s rule ("a green guard is not evidence until you have proven it can FAIL") is necessary and **NOT sufficient** — two of these were *claimed* mutation-verified.

1. **Calibrated one row short.** The rail over-fetch test used 12 batches × 2 rows = exactly 12 collapsed rows; the bug triggers at 14.
2. **A false certificate.** A comment claimed "Mutation-verified: …" for a guard that stayed green when deleted. It had never been run.
3. **A fixture that cannot reach the code.** The rune-boundary test used a 2-byte rune, so the walk-back loop never executed, and checked for U+FFFD without ever marshalling.
4. **True for an incidental reason.** `TestRailExpandControlIsDesktopOnly` matched the FIRST `@media` in `app.css`, ~1000 lines before the rail CSS, comparing string indexes.
5. **Sliced its own fixture to the cap, then asserted the cap** (green at 12/12). Caught by its own author.
6. 🔴 **SHARED INFRASTRUCTURE: `title=` was an escape hatch in `TestLongUntrustedStringsCanBreak`.** The scan is already scoped to TEXT, so an attribute-only long string never reaches the check — the hatch's stated purpose was already covered. What it did was exempt any element with a tooltip that ALSO printed the long string as text. **Two agents hit it within an hour**, both watched a deleted `min-w-0` go undetected, and both worked around it by moving the tooltip. **Fixed at the checker** (v0.1.88); proven with a probe that fails now and passes with the clause restored.

**What actually works:** re-run the mutation *yourself* (I caught one "verified" claim where my own `sed` didn't match and the test passed vacuously — a mutation check that doesn't mutate looks exactly like a passing one), and check the **fixture reaches the interesting case**.

## 🔴 Hard-won areas — read before touching
- **A covered popover is NOT automatically a z-index problem.** v0.1.87's "version hover z-index issue" was fixed with **no z-index change**: a `hidden` `.cm-vgroup` still at `display: flex` (an author rule beats the UA `[hidden]` rule), transparent so it painted nothing yet still won hit-testing. It also meant the base-model pills were filtering nothing.
- **Fix overlap by LAYOUT when raising z-index would cost something.** v0.1.88's carousel buttons painted over the first/last card's Run CTA; raising the card above `z-5` would have buried the strip's only non-drag control. `padding` alone failed (scroll-snap scrolled the gutter away); `padding` + `scroll-padding` holds.
- **`generationTile`'s layering**: `z-10` full-bleed overlay anchor (needs an explicit `aria-label`), `z-20 pointer-events-none` caption with only the batch link `pointer-events-auto`, and a **negative** `outline-offset` as the only reason the tile has a visible focus ring.

## Two `CLAUDE.md` defects corrected this session
Both told agents something false and cost real work:
- **`--max-file-size` does NOT guard `POST /models/{id}/download`.** It is read only by the poller and the "Download & run" path. An agent trusting the doc started a real **2 GB** fetch and killed the server at 127 MB.
- **The doc named the latest migration inline** and went stale twice (said 0015 while 0016 shipped and 0017 was in flight). Two agents took their next number from it. It now points at `ls internal/store/migrations/ | tail -1` and warns to check in-flight worktrees.

## Cleanup / side effects
- ⚠ **Concurrent agents collide in the browser.** Siblings share a session id, so `browser open` can return `reused: true` pointing at ANOTHER agent's tab — it happened repeatedly, and one agent screenshotted a different `cm` instance by mistake. Each visual agent must `open` its own tab, pass `--tab <id>` on EVERY op, and verify `location.port` before trusting a screenshot.
- ⚠ **An un-qualified `pkill -f` killed a sibling's scratch server.** Kill by PID; `pkill -f` also matches its own shell (exit 144).
- ⚠ **A stale server can hold a port after its DB is deleted** — it keeps the handle, so a readiness loop succeeds instantly against the WRONG server. That cost me three consecutive wrong conclusions; verify the process and DB size before believing an impossible result.
- ⚠ **Three session-limit deaths mid-task across two sessions.** All recovered mechanically because the tree stayed buildable. The danger is not a broken tree but a **half-written fix that looks finished**.
- Verifications wrote **9 images + 3 videos** into the user's real ComfyUI output dir (`SaveImage` writes there; unavoidable). Left in place.
- The real DB was never written to by any verification except the intended 0017 migration; every seeded check used a throwaway temp DB.
