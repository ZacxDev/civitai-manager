# civitai-manager — session handoff (2026-07-27)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons now live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.51**, `main` clean & synced. **`export GOPRIVATE=github.com/civitai/*`** before any go build/vet/test. Standing OK to push+tag+release without asking — but always run the real gate (build/vet/test +`-race` on web/store/comfy) + `/audit-pr` (scaled to blast radius — see CLAUDE.md) + **HTTP-level live-verify against the REAL civitai API** (fake-reader tests miss real-API bugs — 3 caught this session) against the `:8972` dogfood, then ship v0.1.x and verify the tarball. No real browser here. Established loop: process feedback → recon (Explore/subagent) → clarifying Qs + recommend → subagent(s) in **worktree isolation** with small compilable commits + complete tests → real gate (arbiter over the agent's "green" AND stale LSP diagnostics; git status clean) → audit/verify → ship → verify tarball → refresh the `:8972` dogfood (4-step binary swap, see CLAUDE.md). Then pick up an OPEN THREAD below or whatever feedback I paste.

## Current state
- **Latest release: v0.1.51** (`main` @ `d6359f5`). **11 releases this session (v0.1.41→v0.1.51).**
- `main` clean, synced with origin, no stray worktrees/branches.
- **Dogfood: v0.1.51 on `:8972`** against the real DB (`~/.config/civitai-manager/civitai-manager.db`; schema at migration `0011`). Rebuilt/restarted after each merge.
- Local ComfyUI (v0.27) at `http://127.0.0.1:8188` (default `comfy_url`) for run/convert verification; `civitai` CLI at `/home/zach/go/bin/civitai` for real-data checks.
- Standing release authorization recorded in auto-memory `civitai-manager-release-authorization`.

## What shipped this session (v0.1.41 → v0.1.51) — condensed
Full detail: `git show <tag>` + the design docs. All gated + (mostly) audited + live-verified + tarball-verified.
- **ComfyUI cloud** — C1 remote CivitAI Comfy Cloud (v0.1.41, whatif live-verified + a **real-Buzz end-to-end run verified**, produced a real 512² image); `minimumDurationSeconds` affordability gate + run-anyway (v0.1.42); UI→API converter so UI-format workflows can cloud-run (v0.1.43). See `claudedocs/COMFYUI-INTEGRATION-DESIGN.md`.
- **Converter structural fixes** (v0.1.46) — rgthree UI-only nodes, Get/Set teleports, **subgraph expansion**, object-form widgets; abort-count on the real library 20→0. **Array-typed input `type` fix** (v0.1.51) — the reported "cannot unmarshal array" run failure (wf 571 now converts).
- **Model detail** — version tabs + reorder + larger showcase (v0.1.45); **version tabs grouped by base model** (selector→filtered tabs, >8 versions & >1 base model) (v0.1.50); card polish — SVG stat icons, "Updated" bottom-left, no Workflows badge, single-import deep-link (v0.1.51).
- **Suggestion/search cards** — lazy showcase carousel + sticky deeplinked version popovers (v0.1.44).
- **Workflow discovery** — D1 browse page (v0.1.44; **`types=` plural fix v0.1.47**), D2 import→unzip→store w/ graph-hash dedup + migration 0011 (v0.1.48); "Popular this month" feed (v0.1.50). See `claudedocs/WORKFLOW-DISCOVERY-DESIGN.md`.
- **App discovery** — A1 browse + click-to-play against `/api/v1/apps` (v0.1.49; **app `id` ULID-string decode fix** caught live). See `claudedocs/APP-DISCOVERY-DESIGN.md`.

## Open threads / next steps (feature backlog — NO mid-diagnosis bugs open)
1. **ComfyUI custom-node cloud gap** (the real remaining cloud limitation) — a bare `comfy:nodepack` URN is rejected at submit; needs a `comfyNodepackSnapshot` step → `nodepacklayer` AIR (post-paid). Large/uncertain; see COMFYUI doc. Auto-deriving the nodepack URN is feasible via ComfyUI-Manager (`cnr_id`+ver) but insufficient alone.
2. **Workflow Discovery D3/D4** — D3: per-model "Related workflows" (approximate, name/baseModel/tag — no reverse API). D4: dedup-on-import UI, large-zip-via-queue, "convert to runnable" nudge. See doc §4.
3. **App Discovery A2/A3** — A2: per-app detail fragment (`/api/v1/apps/{slug}`). A3: polish. (Catalog is live for this user — own apps visible — so both are verifiable now.)
4. **Cloud fast-follow** — the affordability gate is effectively inert (whatif cost=0 for per-second-metered CustomComfy); `minimumDurationSeconds` is wired but real runaway-spend protection is the server-side live-balance guard.
5. **Deferred/minor** — `TestScanStatusRaceSafe` is a pre-existing rare full-suite `-race` assertion flake (never a DATA RACE; passes isolated); the recurring `h.StyleAttr deprecated` cosmetic vet notes.

## Needs the user's eyes (no browser on this host — HTTP/markup-verified only)
- The **sticky popover + version deeplink** (v0.1.44), the **version-group pill selector + 22rem showcase** (v0.1.45/0.1.50), the **SVG stat icons + bottom-left Updated line** (v0.1.51), and the **Apps browse/play** cards — all markup/HTTP-verified, not DOM-verified.

## How to verify (the loop that held up)
subagent (small compilable commits) → real gate `GOPRIVATE=github.com/civitai/* go build/vet/test` + `-race` on web/store/comfy (the arbiter — over the agent's "green" AND stale `<new-diagnostics>`; `git status` must be clean) → `/audit-pr` (scaled to blast radius) → **HTTP live-verify against the real civitai API / dogfood** (throwaway temp-DB + token for mutating/egress checks; never the user's real DB) → push+tag+release → pull tarball, checksum, run `--version`.

## Cleanup pending
- **Dogfood v0.1.51 running unattended** on `:8972` (real DB). `pkill -f "dogfood/cm serve"` to stop; the scratchpad `dogfood/cm` binary can be cleared.
- **Side effects of live-verify:** a real cloud generation ("scenic mountain landscape") exists in the user's CivitAI account generation history (from the real-Buzz verify); a test image `OneWordChallenge_00001_.png` was generated into the local ComfyUI `output/` earlier. Both harmless.
- No temp verify instances/token-configs left running (all cleaned per-step).
