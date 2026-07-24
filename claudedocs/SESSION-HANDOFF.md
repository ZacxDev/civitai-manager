# civitai-manager — session handoff

_Point-in-time snapshot. Verify against `git log`/live code before acting._

## Current state
- **Latest release: v0.1.27** (`main` @ `958ecb9`). 9 releases this session (v0.1.19 → v0.1.27).
- Local `main` is clean. **`export GOPRIVATE=github.com/civitai/*`** before any `go build`/`vet`/`test` (private `github.com/civitai/cli` SDK dep) — otherwise the build fails on sumdb verification. See repo `CLAUDE.md` (read it first).
- **Standing release authorization:** the user granted (2026-07-24) blanket OK to push+tag+release WITHOUT per-release approval — still run the full gate + `/audit-pr` + live-verify first, then ship and report. (Recorded in auto-memory `civitai-manager-release-authorization`.)

## What shipped v0.1.20 → v0.1.27
- **v0.1.20** — results-view render caps (matched 200 / unmatched 500 / candidates 500) + sort-before-cap (biggest rendered) + cap-immune "Quarantine all &lt;reason&gt;" buttons.
- **v0.1.21** — scan-progress focus (scan form hidden while a scan runs; lives inside `#scan-results`); dashboard/search overhaul: search-card image carousels (manager-side raw-JSON parse), "Popular this month" default (TTL cache), integrated `/subscribe/search`, library-derived subscribe suggestions.
- **v0.1.22** — downscaled civitai image thumbnails for showcase tiles (`civitaiThumbURL`, host-gated to image.civitai.com; lightbox keeps original).
- **v0.1.23** — keyword search sorts Most Downloaded; **lazy community-image feed** on the model page (`GET /models/{id}/community`, `SearchImages` by version, `hx-trigger=revealed`, 15s bounded); showcase carousel moved into the model header; description overflow constrained (`.cm-model-desc`).
- **v0.1.24** — Library defaults to Model files tab when set up; author `@link` on matched cards; **version breakdown** (in-library / available / newer + links) + subscribe toggle; **global NSFW control in navbar** (3-state, HX-Refresh; per-page control removed); model-page version badges.
- **v0.1.25** — perf pass: indexed per-model lookups (`ListLocalFilesByModel` + `FindModelSubscription`) replacing full-table scans on the Model files tab.
- **v0.1.26** — subscribe UX: inline **options + confirm + feedback** control (`/models/{id}/subscribe-options|subscribe-control`); suggestion titles cache-first + lazy (`/models/{id}/title`); **search NSFW-param fix** (images were withheld — `nsfw=true` under blur/show, `false` under hide; popular cache re-keyed per flag); compact K/M counts; audit-fix: subscribe FAILURE now shows feedback.
- **v0.1.27** — subscribe control reflects real subscribed/not state on the model page + search/creator cards (one `ListSubscriptions` per render; no per-card scan).

## Cross-repo (user ships CivitAI code)
- `civitai/civitai` (prod) + `civitai/cli` (private SDK) work landed in the batch-by-hash era (see git). The SDK already exposes `SearchModels`/`SearchImages`/`SearchCreators`/`GetModel`/`GetModelVersion(sByHashes)` — no SDK change was needed for any v0.1.2x feature. `civitai/cli` branch protection has a `pins-vs-published` required check the USER resolves.

## The verify/ship loop that held up
subagent (COMMIT IN SMALL COMPILABLE INCREMENTS) → **real gate with GOPRIVATE** (`go build/vet/test`, +`-race` on web/store) — this is the arbiter over BOTH the agent's "green" claim AND the editor's stale diagnostics → **`/audit-pr`** on every web/endpoint PR (it surfaced a real 🟡 on nearly all of them; fold them in before merge) → **live-verify by HTTP-level request reproduction** against the `:8972` dogfood binary (no real browser here — see CLAUDE.md; use throwaway temp-DB seeders for mutating actions) → push+tag+release (standing auth) → **pull tarball, checksum, run `--version`** (deployed ≠ verified).

## Gotchas re-confirmed this session
- **Signature-changing subagents ALWAYS trip a stale `<new-diagnostics>` cascade** — `WrongArgCount` on shared page-builders, `stubReader does not implement Reader (missing GetModelVersionsByHashes)`, `X redeclared`, phantom `zz_dbg_test.go`, `go.mod needs tidy`. `go vet` (compiles tests) + `go test` with GOPRIVATE refutes them; `go mod tidy -diff` empty refutes the tidy warning. Do NOT "fix" these before running the real gate.
- **No real browser** (MCP Playwright broken; chromium not installed). HTTP-level reproduction verifies the server response, not the DOM/JS — state that caveat when reporting.

## Open threads / next steps
1. **F2 — loopback-gate the outbound-proxy GETs** (`/models`, `/search`, `/creators`, `/subscribe/search`, `/models/{id}/community`, `/models/{id}/title`, popular default). Only matters on a non-loopback `--addr`; SSRF-shaped but low-severity (fixed civitai.com host, integer ids). Decide gate-all vs accept-and-document. The lone tracked follow-up.
2. **Deferred/minor:** `compactCount` `999_999 → "1000K"` (intended edge); the recurring `h.StyleAttr is deprecated` warnings (cosmetic; whole codebase uses it); CI Node-20→24 actions-bump warning on releases.

## Cleanup pending
- **Dogfood server is running unattended** — v0.1.27 build on `:8972` against the real DB (`~/.config/civitai-manager/civitai-manager.db`). Stop it when done.
- Session **scratchpad** has `serveN.log` + a `dogfood/cm` binary — clear when convenient.
