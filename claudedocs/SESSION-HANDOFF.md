# civitai-manager — session handoff

_Point-in-time snapshot. Verify against `git log`/live code before acting._

## Current state
- **Latest release: v0.1.19** (`main` @ `7982d54`+). 22 PRs merged, 20 releases this session.
- Local `main` is clean. **`export GOPRIVATE=github.com/civitai/*`** before any `go build`/`test` (private `github.com/civitai/cli` SDK dep) — otherwise the build fails on sumdb verification.
- Repo now has a **`CLAUDE.md`** (dev conventions/architecture) — read it first next session.

## What shipped (high level)
Three original pillars + a lot more: subscribe/auto-download (verified, fleet-jitter), civitai-themed two-tab web UI, library management (streaming multi-disk **discovery**, concurrent **scan** → **one batch by-hash POST** match → duplicate/superseded/broken analysis → reversible quarantine). Recent turns: model-page gallery, stop-button + discovered-count, concurrent scan, match-default-on, **batch by-hash matching** (v0.1.18), and the **results-view overhaul + broken-flood fix** (v0.1.19).

## Cross-repo work (user ships CivitAI code)
- **`civitai/civitai` (production):** merged 4 by-hash PRs — #3318 modelId in batch, #3320 deterministic single lookup, #3321 longer edge cache TTL; plus their #177 (scaffold-pin bump). Follow-up idea from the analysis: a Redis cache on the single by-hash endpoint (deferred — batch adoption removed most of that load).
- **`civitai/cli` (private SDK):** #176 merged the batch `GetModelVersionsByHashes`. NOTE: branch protection has a `pins-vs-published` required check (scaffold-pin drift, unrelated to Go changes) that blocks even `--admin` — the USER resolves it.

## Open threads / next steps
1. **F1 — row cap (recommended next, v0.1.20):** the results view renders ALL rows/cards with no cap. The 45k broken-PNG flood that crashed the tab is fixed, but a pathological library with tens of thousands of **unmatched** files could re-create the huge-DOM crash. Add a cap + "showing N of M" on the unmatched table and matched cards. (From the PR #22 audit.)
2. **F2 — gate outbound-proxy GETs:** `/library/model-card/{id}` (and pre-existing `/models`, `/search`, `/creators`) trigger outbound `GetModel` and aren't loopback-gated. Only matters on a non-loopback `--addr`. Decide whether to gate all proxy GETs. (Pre-existing parity, not a v0.1.19 regression.)
3. **Deferred/minor:** the `#7 TOCTOU` + 🟢 nits from earlier audits; the **F5 upstream issue** (civitai design-system light-mode card hairline — filed at civitai/civitai-app-starters#181, mostly addressed in @civitai/* 0.1.2); the recurring **CI Node-20 → Node-24 actions bump** warning on releases.

## Operational notes
- **Release/verify loop that held up:** dispatch feature work to a subagent (COMMIT IN SMALL COMPILABLE INCREMENTS) → run the **verify-agent gate** (with GOPRIVATE) → **`/audit-pr`** for web/concurrency/quarantine/migration/security PRs → merge → tag `vX.Y.Z` → watch `release.yml` → **pull the released tarball, checksum, run it** (deployed ≠ verified) → refresh Brave.
- **Stale `<new-diagnostics>` after subagents are almost always false** (go.mod-tidy + "undefined: X" cascade, esp. after branch/worktree switches). The gate is the arbiter — never act on raw diagnostics.
- **Browser-verify client-side htmx bugs** (MCP Playwright broken on this NixOS host → system chromium via executablePath, never silently skip).
- Three usage-limit interruptions this session were recovered cleanly because agents committed in small compilable increments and `main` was never left broken.

## Cleanup pending (not done)
- **Review serves still running:** v0.1.19 on `:8972` (against the real-scan review DB at `scratchpad/v0118review`, now cleaned to 0 broken / 292 matched). Stop it when done.
- The session **scratchpad** (`.../scratchpad/`) has downloaded release binaries + review DBs — clear when convenient. (Do NOT touch the user's own `cli-*` worktrees.)
