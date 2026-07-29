# civitai-manager — session handoff (2026-07-29)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.77**, `main` clean & synced. **GOPRIVATE is NO LONGER needed** — `github.com/civitai/cli` went public; a bare `go build`/`go test` works (if you see a sum-db 500 now, that's a REAL failure, not env). Standing OK to push+tag+release without asking — but always run the real gate (build/vet/test + `-race` on web/store/comfy/comfyext; the arbiter over an agent's "green" AND over stale LSP diagnostics; `git status` clean) + `/audit-pr` scaled to blast radius (**and re-audit the DELTA after a fix round — a fix introduced the next finding five times this session**) + **HTTP-level live-verify** against the `:8972` dogfood / real ComfyUI (`:8188`) / real CivitAI, then ship v0.1.x and verify the tarball. No headless browser — but the **`browser` skill** drives the user's live Brave, and for screenshots the reliable path is X capture (`DISPLAY=:0 XAUTHORITY=~/.Xauthority`, `xdotool search --name`, `i3-msg [id=…] focus`, `maim -i`, `magick -crop`) since `captureVisibleTab` fails on any non-composited window. Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) with small compilable commits + complete tests → real gate → audit/verify → ship → verify tarball → refresh `:8972` (4-step swap: pkill → confirm port 000 → cp → start). Then pick up an OPEN THREAD or whatever feedback I paste.

## Current state
- **Latest release: v0.1.77** (`main` @ `ccc7014` + docs commits). **9 releases this session (v0.1.69 → v0.1.77).** Tarballs verified each time (checksum + `--version`).
- `main` clean and synced. **One intentional uncommitted change: `CLAUDE.md`** (multi-mode + substitution invariant) — commit it with this handoff.
- **Dogfood: v0.1.77 on `:8972`** against the real DB, started with `--comfy-model-path /home/zach/workspace/fast/comfyui/ComfyUI/models --comfy-root /home/zach/workspace/fast/comfyui/ComfyUI`.
- Local **ComfyUI 0.27.1** at `:8188` (pid was 1332527). **The civitai-manager helper extension is INSTALLED and live** there (`/civitai-manager/ping` → 200, `/extensions/civitai-manager/civitai_manager.js` → 200).
- **Distribution is live**: GitHub releases (6 platforms) + `.deb`/`.rpm` + **Homebrew cask** (`ZacxDev/homebrew-tap`, `Casks/civitai-manager.rb` @ 0.1.77, merged) + **Nix flake** (`nix run github:ZacxDev/civitai-manager`) + **`curl | sh` installer** + **build provenance** (`gh attestation verify --owner ZacxDev <tarball>` → exit 0, verified with a negative control).
- **Landing page live**: <https://zacxdev.github.io/civitai-manager/> (Pages from `main` `/docs`), all five screenshot slots filled with real captures.
- `HOMEBREW_TAP_GITHUB_TOKEN` set — fine-grained, scoped to `homebrew-tap` only, Contents + PRs read/write, **expires ~2026-10-26**. When it lapses the cask step fails while the rest of the release succeeds.

## What shipped this session (v0.1.69 → v0.1.77) — condensed
- **v0.1.69** output-gallery follow-ups: capture on the download-and-run path; `comfy.readBounded` returns `ErrResponseTooLarge` instead of silently truncating; total disk cap (`outputs_max_bytes`, default 20GB, `"0"` unlimited, 1 MiB floor) evicting oldest-first.
- **v0.1.70** global collapsible **outputs rail** on every page, **sticky nav**, shell `max-w-6xl` → **1800px** (+ `.cm-cardgrid`/`.cm-masonry` density since Tailwind stops at 1536px); **upstream-resolved run parameters** (follows converted-widget links to the node that holds the value — wf587 went 2 fields → 11 with both prompts); **faithful graph preview** (collapsed nodes were drawn expanded, moving wire endpoints up to 340px — that was the "different workflow" report).
- **v0.1.71** UX-audit batch: dead classes (the Fix CTA rendered transparent), mobile overflow, contrast/a11y (`<h1>`s, focus ring, AA light-theme override), polish.
- **v0.1.72–73** **ComfyUI helper extension** (`internal/comfyext`, go:embed) + Open-in-ComfyUI that actually opens (form `target=_blank` → 303 into ComfyUI). Fixed the **zombie-helper** bug: deleting the helper leaves ComfyUI's routes in memory, so `ping` kept answering 200 while the JS 404'd — detection now needs **both** legs.
- **v0.1.74** **ecosystem/use-case discovery** (one curated taxonomy shared by Discover + library; 19 ecosystems from a live 600-model harvest; hostile facet values whitelisted); `CLAUDE.md` corrections (GOPRIVATE, NSFW two-state).
- **v0.1.75** **Install-and-run fixed** — the primary CTA was dead (filename resolution fails on the *common* case because CivitAI renames files across versions), and fixing it exposed a silent 6.6 GB wrong-file install. Now: substitution **offered, never performed**; type checked against the **destination folder**; requests bound to the workflow.
- **v0.1.76** **multi-mode templates** (wf581 runs), unified browse surface, `?model=<id>` source-post scoping, Open-in-ComfyUI on run failures.
- **v0.1.77** packaging: Nix flake **was broken for everyone** (drifted `vendorHash`, nothing in CI built it) + CI gate, Homebrew casks (PR mode), `.deb`/`.rpm`, installer, attestations.

## Open threads / next steps
1. **macOS is UNVERIFIED end-to-end** — nobody here has a Mac. The cask ships a deliberate `com.apple.quarantine` strip (Homebrew quarantines binary-stanza casks unconditionally; `--no-quarantine` was deleted in 5.2; a quarantined non-notarized CLI is killed by `syspolicyd`). Evidence says the bypass is *necessary*; nobody has confirmed it's *sufficient*, nor that `postflight` runs late enough. Proper fix = notarization: GoReleaser's `notarize.macos` is **OSS** and runs on Linux — it needs the **$99/yr Apple Developer** membership, not GoReleaser Pro.
2. **Homebrew 6.0 tap-trust flag spelling unverified** — third-party taps must be explicitly trusted and no longer auto-tap. Documented in the tap README + `docs/install.md` with that caveat; check `brew tap --help` on a real 6.x install before relying on the copy.
3. **Launch drafts are written, not posted** — `claudedocs/LAUNCH-CIVITAI-ARTICLE.md`, `claudedocs/LAUNCH-REDDIT-POST.md`. Recommended order **r/comfyui first** (no self-promo rules), **r/StableDiffusion later** (once-per-tool cap, so spend the one shot after r/comfyui surfaces the recurring objection). ⚠ **r/StableDiffusion's `post_requirements.json` blacklists the literal string `nsfw` in title AND body** (server-side reject) — the drafts have paste markers guarding this. Both need flair.
4. **Custom-node gap** — the last dependency hole: only *model files* auto-resolve. Filed as **clawgate task #74** with a full spec (detect missing `class_type` vs `/object_info` → attribute to a node pack → gated install reusing the `comfyext` safety discipline). wf581 now converts on all four modes and stops precisely here (Comfyroll, RIFE, MMAudio).
5. **`devrc` changes are UNCOMMITTED and INERT** — `claude/RULES.md`, `claude/commands/audit-pr.md`, `claude/commands/verify-agent.md` are home-manager-managed via `/nix/store`, so they need `home-manager switch --flake ~/workspace/devrc --impure`. (`scripts/browser-bridge/SKILL.md` is a direct symlink and is already live.)
6. **Deferred/minor** — re-running a captured generation does **not** restore a mode selection (needs a migration); subgraph-interior parameters still uneditable; the outputs disk cap measures *recorded* (DB) bytes, not the tree; `LoadImage.image` clipspace values over-report as incompatible options.

## Needs the user's eyes
- **A real `brew install --cask` on macOS** (thread 1) — the one thing that can't be done here.
- Everything visual is HTTP/markup-verified only; no browser DOM verification. The five landing-page screenshots are real captures, so the UI is at least *seen* now.
- The launch drafts before posting — especially the anticipated-replies section.

## Cleanup / side effects
- Dogfood v0.1.77 running unattended on `:8972` (real DB). `pkill -f "dogfood/cm serv[e]"` to stop.
- The **PAT was pasted into a session transcript** — scoped narrowly and expiring 2026-10-26, but rotating it is the clean move.
- ComfyUI was restarted twice this session (once by me from the wrong cwd, which created a stray 1.6 GB venv in the civitai-manager repo — removed; the flake `$PWD`-anchoring bug that caused it is fixed in `~/workspace/fast/comfyui`, **uncommitted there**).
- Real generations exist in the user's ComfyUI output dir and gallery from live verification. Temp instances, seeders and worktrees were cleaned per-step.
