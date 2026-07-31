# civitai-manager — session handoff (2026-07-31)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.87**, `main` @ `e345d7f`, clean & synced, migrations at **0016**, ONE worktree and ONE branch. **GOPRIVATE is NOT needed.** R2 is complete and the whole UI feedback list from 2026-07-30 is shipped. The next threads are all **pre-existing bugs found but deliberately not fixed** — see #1–#4 below; #1 blocks the app's own run path. Standing OK to push+tag+release without asking — run the real gate (`go build ./... && go vet ./... && go test ./... && go test -race ./internal/web/... ./internal/comfy/... ./internal/store/... && gofmt -l ./internal/ ./e2e/ ./*.go`; **`gofmt` is NOT covered by `go vet`**, **`./cmd/` does not exist**, `./e2e/` must be named explicitly) + `/audit-pr` scaled to blast radius + **a delta re-audit after EVERY fix round**, then **push `main` BEFORE tagging**, verify the tarball, refresh the `:8972` dogfood. If `go.mod` changes at all, re-run `nix build .`. **You CAN drive a real browser** (`browser --instance work open <url>` → `activate` → `screenshot`/`eval`, hit-test with `document.elementFromPoint`) — it found a real bug in EVERY visual branch this session. Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate → audit → delta re-audit → ship → verify tarball → refresh dogfood.

## Current state
- **Latest release: v0.1.87** (`main` @ `e345d7f`), clean and synced. **Tarball verified** — checksum OK, binary runs, reports `0.1.87 (commit e345d7f…)`, `gh attestation verify` on the TARBALL exits 0 (silently; the exit code is the signal — verifying the *extracted binary* 404s by design, attestation subjects come from `checksums.txt`).
- `go.mod`/`go.sum` **untouched across v0.1.84–v0.1.87**, so `vendorHash` needed no re-verification. Checked each time, not assumed.
- **Repo tidy**: ONE worktree, ONE branch. All agent worktrees/branches pruned.
- Dogfood **v0.1.87 on `:8972`** (binary at `…/dbce7753-…/scratchpad/dogfood/cm` — an OLD session's scratchpad; if that dir is GC'd, rebuild somewhere durable). Smoke-checked after the swap: `/`, `/outputs`, `/library`, `/models/4384` all 200, the new `related-workflows` fragment 200.
- Local **ComfyUI 0.27.1** at `:8188`. Untracked **`opencode.json`** still in the repo root; not mine, left alone.

## What shipped v0.1.84 → v0.1.87
- **v0.1.84** — batch gallery: `GET /outputs/batch/{id}` + the `Batch i/N` tile caption link (R2's view half).
- **v0.1.85** — the recent-outputs rail collapses a batch to ONE entry with a `×N` badge.
- **v0.1.86** — run inputs reach INSIDE subgraph definitions, so the 13 workflows whose only samplers live in a subgraph can finally batch with real per-item seeds.
- **v0.1.87** — the whole 2026-07-30 UI feedback list: model-page popover/hidden-panel/version-buckets, output provenance + per-workflow outputs strip, workflow↔model discovery both directions, and the fully-clickable collapsed rail edge.

## 🔴 Open threads — all PRE-EXISTING bugs, found and deliberately not fixed
1. 🔴 **`InputSpec.UnmarshalJSON` mishandles a NUMERIC combo — it blocks the app's own run path.**
   `internal/comfy/client.go:264`: `json.Unmarshal(arr[0], &s.Choices)` on a numeric
   combo (`[0.25,0.5,1.0,2.0,4.0]`) does NOT leave `Choices` nil as its comment
   claims — `encoding/json` returns a type error **and** leaves a slice of 5 empty
   strings. `detectBadOptions` then sees 5 choices, matches none, and raises an
   **unfixable BadOption with five blank fix options**. Proven with a throwaway probe
   (`IsCombo=true Choices=[]string{"","","","",""} len=5`). Hits ANY workflow with a
   numeric COMBO — it halted a real batch at item 1 on `RIFE VFI · scale_factor`, and
   is why v0.1.86's live verification had to go straight to ComfyUI rather than
   through the app.
2. 🔴 **Bypassing a subgraph instance does not bypass it.** Measured on the real
   fixture: with instance 93 at mode 4, `flattenSubgraphs` still emits **all 7
   interior clones at `mode=0`**, because `convert.go:158-167` flattens BEFORE the
   mode drop at `convert.go:198` and each clone carries the *interior* node's own
   mode. v0.1.86's refusal to expose bypassed interiors is conservative in the right
   direction and its comment records this measurement — start from that, not a
   rediscovery.
3. **The 4 custom-sampler workflows still expose no seed.** `runInputLayouts`
   (`internal/comfy/run_inputs.go:102,110`) yields `RunInputSeed` for exactly
   `KSampler` and `KSamplerAdvanced`. After v0.1.86 the subgraph 13 are fixed; the
   remaining non-seedable are 4 with top-level custom samplers
   (`SamplerCustomAdvanced`, `ClownsharKSampler_Beta`, `KSamplerSelect`,
   `DetailDaemonSamplerNode`, `WanVideoSampler*`, `Tiled KSampler`) and 2 with no
   sampler at all. Design questions are real: which widget is the seed on
   `SamplerCustomAdvanced`? Feature work, not a patch.
4. **`.cm-updated-pop` does not flip near the right viewport edge** (`left: 0`, no
   flip), so a date popover on a right-most version tab overflows off-screen.
   Bucketing to 6 tabs makes it much rarer; it is not gone.

## Open threads — smaller
5. **Per-resource DB query in the workflow list.** `workflowUsesChips` →
   `resolver.resource(base)` is a live `LocalFileByBasename` per resource; the
   adjacent popover already did 2, so this is 3 (+50%), not a new N+1 class. Fix is a
   per-request basename memo on the shared resolver — which is why it was fenced out.
6. **`discover_facets.go` pre-existing**: 260 facet combos vs `maxFacetFeedEntries =
   256`, and `facetFeed` has no singleflight so concurrent misses each fan out.
7. **API-format workflows capture no prompt** — `DetectRunInputs` needs
   `widgets_values`, so provenance is UI-format-only in practice. The UI says "no
   prompt input was detected in the graph this run submitted" rather than guessing.
8. **Two non-brand light-theme AA failures remain by decision**: `.cm-size-large`
   2.24:1, muted-text-on-tint 4.12:1. Pinned as debt.
9. **CivitAI returns `items: []` for a keyword query + any narrow period** — upstream
   post-filter behaviour. Don't "fix" locally; consider "no results for this window".
10. **macOS UNVERIFIED end-to-end**; **Homebrew 6.0 tap-trust flag spelling
    unverified**; **launch drafts written, not posted** (r/StableDiffusion blacklists
    the literal string `nsfw` in title AND body); **`devrc` changes LIVE but
    UNCOMMITTED** in `~/workspace/devrc`.

## 🔴 The lesson of this session: FOUR green tests that proved nothing
Every one was written by a careful agent, three sat over **correct** implementations,
and each failed differently. `CLAUDE.md`'s rule ("a green guard test is not evidence
until you have proven it can FAIL") is necessary but was **not sufficient** — two of
these were *claimed* mutation-verified.

1. **Calibrated one row short.** `TestRailOverFetchesSoBatchesCannotUnderFillIt` genuinely
   pinned the design property and was mutation-verified — but its fixture was 12
   batches × 2 rows = exactly 12 collapsed rows, and the bug triggers at 14. One more
   row and it would have caught the shipped defect.
2. **A false certificate.** A comment claimed "Mutation-verified: …" for a guard that
   stayed green when deleted. The mutation had never been run.
3. **A fixture that cannot reach the code.** The rune-boundary test used a 2-byte rune,
   so the walk-back loop never executed, and checked for U+FFFD without ever
   marshalling — so it could not appear. Deleting the guard left it green.
4. **True for an incidental reason.** `TestRailExpandControlIsDesktopOnly` matched the
   FIRST `@media (min-width: 1024px)` in `app.css` — ~1000 lines before the rail CSS —
   and only compared string indexes. Inserting the exact bug it is named for, outside
   every media block, left it green.

**What actually works:** re-run the mutation *yourself* rather than reading the
reported message (I caught one "verified" claim where my own sed didn't match and the
test passed vacuously); and check the FIXTURE reaches the interesting case, not just
that the test can fail.

## 🔴 Two more hard-won areas — read before touching
**`generationTile`'s layering (v0.1.84).** The detail link is a **z-10 full-bleed
overlay anchor** needing an explicit `aria-label`; the caption bar is `z-20
pointer-events-none` with **only** the batch link `pointer-events-auto`; and
`.cm-tile-link:focus-visible`'s **negative** `outline-offset` is the only reason the
tile has a visible focus ring at all (the app-wide ring is drawn outside the border
box, which the tile's `overflow-hidden` clips away).

**A covered popover is NOT automatically a z-index problem (v0.1.87).** The reported
"version hover has a z-index issue" was fixed with **no z-index change at all**.
Hit-testing named the culprit: a `hidden` `.cm-vgroup` panel still at `display: flex`
(an author rule beats the UA `[hidden]` rule), transparent so it painted nothing yet
still won hit-testing. It ALSO meant the base-model pills were filtering nothing.
Diagnose with `elementFromPoint` + an ancestor stacking-context walk BEFORE touching a
number; the ledger in `app.css` is a set of ceilings something else depends on.

## Cleanup / side effects
- ⚠ **TWO session-limit deaths mid-task** this session (plus a monthly limit last
  session). Both recovered mechanically because the tree stayed buildable — this is
  the small-compilable-commits rule earning its place. Recovery order is in
  `CLAUDE.md`; the danger is not a broken tree but a **half-written fix that looks
  finished** (one left an `outline-offset: 2px` under a comment arguing it must be
  negative — green on every gate, because CSS is not compiled).
- ⚠ **Concurrent agents collide in the browser.** Siblings share a session id, so
  `browser open` can return `reused: true` pointing at ANOTHER agent's tab. One agent
  screenshotted a different `cm` instance by mistake. If you run visual agents
  concurrently, each must `open` its own tab and pass `--tab <id>` on EVERY op, and
  verify `location.port` before trusting a screenshot.
- ⚠ **An un-qualified `pkill -f` killed a sibling's scratch server.** Kill by PID.
  `pkill -f "<pattern>"` also matches its own shell (it killed one of my commands
  outright, exit 144).
- ⚠ **A stale server can hold a port after you delete its DB** — it keeps the file
  handle, so a `until curl` readiness loop succeeds instantly against the WRONG
  server. If a seeded check returns something impossible, verify the process and the
  DB size before believing it (this cost me three wrong conclusions in a row).
- The v0.1.86 live verification wrote **9 images** into the user's real ComfyUI output
  dir (`ComfyUI_01061_`–`01069_`), and the subgraph verification wrote 3 videos under
  `output/video/wan22/base/`. Unavoidable — `SaveImage` writes there and the app copies
  from it. Left in place; the user's own outputs are not ours to prune.
- Real DB never written to by any verification (every seeded check used a throwaway
  temp DB; the real one was opened `mode=ro`).
- `internal/comfy/testdata/wf557_subgraph_samplers.json` is a REAL user workflow whose
  structure is verbatim but whose two user-content strings were replaced with
  placeholders — **the repo is public**. Keep it that way if the fixture is refreshed.
