# civitai-manager — session handoff (2026-07-29, later session)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.78**, `main` clean & synced. **GOPRIVATE is NOT needed** — `github.com/civitai/cli` is public; a sum-db 500 today is a REAL failure. Standing OK to push+tag+release without asking — but always run the real gate (build/vet/test + `-race` on web/store/comfy/comfyext, **plus `gofmt -l`, which `go vet` does NOT cover and which let 3 unformatted files through this session**) + `/audit-pr` scaled to blast radius (**re-audit the DELTA after a fix round**) + **HTTP-level live-verify** against the `:8972` dogfood / real ComfyUI (`:8188`) / real CivitAI, then ship v0.1.x and verify the tarball. No headless browser — the **`browser` skill** drives the live Brave; for screenshots use X capture (`DISPLAY=:0 XAUTHORITY=~/.Xauthority`, `xdotool search --name`, `i3-msg [id=…] focus`, `maim -i`, `magick -crop`). Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) with small compilable commits + complete tests → real gate → audit/verify → ship → verify tarball → refresh `:8972` (4-step swap: pkill → confirm port 000 → cp → start). Then pick up an OPEN THREAD or whatever feedback I paste.

## Current state
- **Latest release: v0.1.78** — custom-node resolution (clawgate task #74). `main` @ `3571789`.
- `main` clean and synced; `gofmt` clean; full gate green on the committed tree.
- Local **ComfyUI 0.27.1** at `:8188`, **ComfyUI-Manager V3.41** live. Dogfood on `:8972`
  (still the v0.1.77 binary from a prior session's scratchpad — **refresh it**).
- ⚠ **`comfy-mtb` was installed into the user's ComfyUI** by this session's end-to-end
  verification (authorized), and ComfyUI was restarted (queue was idle). Remove via
  Manager's UI or `POST /manager/queue/uninstall` if unwanted.

## What shipped: v0.1.78 — custom-node resolution
A workflow needing an uninstalled custom node used to fail preflight with no
attribution and no action. Now it names the missing node types, says which pack
provides each, and offers a gated install. Design + all live measurements:
**`claudedocs/CUSTOM-NODE-RESOLUTION-DESIGN.md`** (read it before touching this).

**The load-bearing decision:** the ticket said `git clone` + `pip install` into the
user's venv. We do NOT. ComfyUI-Manager is already installed with its API on
loopback, so the install is **delegated to it** — we never execute third-party
code, never resolve a venv, never write `custom_nodes/`. Cost: one-click install is
conditional on Manager; attribution + the manual command are unconditional.

**Verified end-to-end** against the real ComfyUI + Manager: attribution named
`comfy-mtb` 0.5.4 with the right repo/class → first Install click **only offered**
→ second installed → the `installed(disk) − imported` diff reported "pending a
restart" → the restart endpoint restarted ComfyUI → the re-run **no longer reports
the node missing**. (It then fails on ComfyUI's own input validation, because the
probe graph feeds the node no image — that is the proof the node loaded.)

## Open threads / next steps
1. **🔴 No independent adversarial audit ran on v0.1.78.** FOUR `/audit-pr` subagent
   attempts died on API 500/529. I audited it myself and found no deploy-blockers,
   but I wrote the spec the implementing agents followed, so that review is
   structurally weak. **`/code-review ultra` on `3571789` (or the pre-merge branch)
   is the clean way to close this.** Highest-value follow-up in this doc.
2. **Task #77 — civitai-manager funnel UX audit** (clawgate). The trustworthy half is
   deterministic axe: **`color-contrast` serious on EVERY view** (~5 elements each,
   dark-theme muted text fails WCAG-AA) + `empty-table-header` on dashboard. The
   persona/keyboard claims are screenshot inference from a plugin-push run with no
   DOM grounding — **DOM-verify before acting on those**. ⚠ A contrast pass changes
   Tailwind classes, and `internal/web/assets/output.css` is a committed purged
   build — regenerate it or the fix ships invisible (the v0.1.71 "transparent Fix
   CTA" class of bug).
3. **macOS still UNVERIFIED end-to-end** — nobody here has a Mac. The cask's
   deliberate `com.apple.quarantine` strip is evidenced as *necessary*, never
   confirmed *sufficient*. Real fix = Developer ID + notarization + a stapled
   `.pkg`/`.dmg` ($99/yr; GoReleaser's `notarize.macos` is OSS and runs on Linux).
4. **Homebrew 6.0 tap-trust flag spelling unverified** — check `brew tap --help` on
   a real 6.x install before relying on the documented copy.
5. **Launch drafts written, not posted** — `claudedocs/LAUNCH-CIVITAI-ARTICLE.md`,
   `LAUNCH-REDDIT-POST.md`. r/comfyui first, r/StableDiffusion later (once-per-tool
   cap). ⚠ r/StableDiffusion's `post_requirements.json` blacklists the literal
   string `nsfw` in title AND body. Both need flair.
6. **`devrc` changes are LIVE but still UNCOMMITTED** in `~/workspace/devrc` — commit
   there or the next `home-manager switch` from a clean tree loses them.
7. **Deferred/minor** — v0.1.78's own 🟡/🟢: reboot reports "requested" without
   confirming (`ManagerAlive` exists, is unit-tested, and is deliberately NOT wired —
   documented in `manager.go`); `wfID` is parsed but not used to scope
   `attributedPack` (the run job is a server-global singleton, so a stale page could
   install a pack attributed under the current run — not attacker-controlled);
   one goroutine per missing class (network concurrency IS capped at 6);
   `nodepack_cache` has no eviction; stale-serve is unbounded. Older: re-running a
   captured generation does not restore a mode selection; subgraph-interior params
   uneditable; outputs disk cap measures recorded (DB) bytes, not the tree.
8. **Custom-node CLOUD support is still absent** — CivitAI CustomComfy rejects a bare
   `comfy:nodepack` URN; needs `comfyNodepackSnapshot` → `nodepacklayer` AIR
   (post-paid). v0.1.78 is local-only, deliberately.

## Needs the user's eyes
- **Thread 1** (independent audit) — the one real process gap on v0.1.78.
- **How this feature gets described.** Live, only **1 of 4** attributed packs was
  installable (`cnr_latest` is often `null`, not just `"nightly"`). So it is
  **attribution with opportunistic install**, NOT "one-click custom node install".
  Do not overclaim in README/release notes/launch copy.
- A real `brew install --cask` on macOS (thread 3).
- Everything visual is HTTP/markup-verified only; no browser DOM verification.

## Cleanup / side effects
- `comfy-mtb` installed + ComfyUI restarted (see Current state).
- Dogfood on `:8972` is a **stale v0.1.77** binary — refresh with the 4-step swap.
- Temp DB, temp `serve` on `:8993`, seeded workflow, and scratch binaries were all
  removed; repo tree verified clean.
- A research subagent left ComfyUI-Manager's `CHANGELOG` as `cl-main.md` in the repo
  root despite reporting "no files were written" — removed. **Agent self-reports
  about side effects are not reliable; check `git status --porcelain` yourself.**
