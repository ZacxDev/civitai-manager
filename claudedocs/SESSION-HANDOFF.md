# civitai-manager — session handoff (2026-07-28)

_Point-in-time snapshot. Verify against `git log`/live state before acting. Durable
conventions + lessons live in the repo `CLAUDE.md` — read it first; this doc is
STATE + OPEN THREADS only._

## ⏭️ Kickoff (paste to start next session)
> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first. Orientation: latest release **v0.1.68**, `main` clean & synced. **`export GOPRIVATE=github.com/civitai/*`** before any go build/vet/test (private civitai/cli SDK dep). Standing OK to push+tag+release without asking — but always run the real gate (build/vet/test +`-race` on web/store/comfy; the arbiter over the agent's "green" AND stale LSP diagnostics; `git status` clean) + `/audit-pr` (full for endpoints/egress/untrusted-input/migrations/concurrency/filesystem/security; gate+live-verify+self-review for pure-UI/bugfix) + **HTTP-level live-verify against the `:8972` dogfood / real ComfyUI (:8188) / real CivitAI** (fake-reader tests miss real-API bugs), then ship v0.1.x and verify the tarball (checksum + `--version`). No real browser here for headless DOM — BUT the **`browser` skill** can drive the user's live Brave (screenshot/eval/nav) if they have `:8972` focused; CLI at `~/workspace/devrc/scripts/browser-bridge/browser`. Loop: feedback → recon → clarifying Qs + recommend → worktree-isolated subagent(s) with small compilable commits + complete tests → real gate → audit/verify → ship → verify tarball → refresh `:8972` dogfood (4-step swap: pkill → confirm port 000 → cp → start; build to a fresh `-o` path or the running server holds it "text file busy"). Then pick up an OPEN THREAD or whatever feedback I paste.

## Current state
- **Latest release: v0.1.68** (`main` @ `9961134`). **17 releases this session (v0.1.52 → v0.1.68).**
- `main` clean, synced with origin; no stray worktrees/branches.
- **Dogfood: v0.1.68 on `:8972`** against the real DB (`~/.config/civitai-manager/civitai-manager.db`; schema now at migration **0012**), started with `--comfy-model-path /home/zach/workspace/fast/comfyui/ComfyUI/models` so **Download & run** + **Open-in-ComfyUI** work. Rebuilt/restarted after each merge.
- Local ComfyUI **0.27.1** at `http://127.0.0.1:8188` (default `comfy_url`; models dir `/home/zach/workspace/fast/comfyui/ComfyUI/models`, local+writable). `civitai` CLI at `/home/zach/go/bin/civitai`. `fabricatedXL_v70.safetensors` + `ultralytics/bbox/face_yolov9c.pt` were installed into ComfyUI this session (587's deps).
- Standing release authorization in auto-memory `civitai-manager-release-authorization`.

## What shipped this session (v0.1.52 → v0.1.68) — condensed
Full detail: `git show <tag>` + the design docs. All gated + audited (scaled/full) + live-verified + tarball-verified.
- **The 587 "Basic_V37" saga (fully closed — it produced nothing at session start, now one-click runs to an image):** empty-conversion actionable error (v0.1.52) · bypassed-source splice + rgthree helpers (v0.1.53) · annotation nodes in virtualNodeTypes (v0.1.54) · **widget-promotion slot fix** (top-level v0.1.56 = 46 workflows corrected; subgraph-interior v0.1.60 = 14 more) · **Download & run** installs a missing checkpoint from CivitAI into ComfyUI's dir (v0.1.57) · preflight "Incompatible options" for `value_not_in_list` combos (v0.1.59) + honest header (v0.1.61) · **HuggingFace fallback** resolver + from-scratch SSRF-hardened client (v0.1.62) · install a model-file BadOption from CivitAI→HF in one action (v0.1.63) · **silent inert-combo normalization** mirroring ComfyUI's serializeValue (v0.1.64).
- **Resolve/substitute/Fix-popover** for missing models (v0.1.55) — CivitAI multi-match cards + installed substitutes + install-and-run.
- **Nav rename** (Search→Models, Discover→Workflows) + **library workflow list org** (sort/filter/group, client-side) (v0.1.53).
- **Security-audit hardening** (v0.1.65) — subgraph-expansion DoS cap (10000), https-only download assertion, loopback-gate quarantine/download/subscribe. See `claudedocs/SECURITY-AUDIT-v0.1.64.md` (0 🔴, 3 🟡 closed).
- **Library/workflow UX overhaul** (v0.1.66) — `#wf-<id>` deeplink+highlight, **Open in ComfyUI** (saves to userdata; see thread 1), provenance/source links, showcase-image list cards, one-primary-action tab redesign + guided empty states.
- **Editable run "Parameters"** (v0.1.67) — `comfy.DetectRunInputs`/`ApplyWidgetOverrides` (prompt/seed/steps/cfg/sampler/scheduler/denoise/dims, ephemeral, stored-workflow-untouched) + workflow-detail showcase carousel.
- **Durable output gallery** (v0.1.68) — auto-capture every successful run's PNGs into `~/.config/civitai-manager/outputs/` (best-effort/off-mutex/panic-guarded capture hook), migration 0012 (generations + generation_images), `/outputs` masonry + per-generation detail (Re-run reconstructs runOptions incl. WidgetOverrides; Delete removes rows+files) + per-workflow section. See `claudedocs/OUTPUT-GALLERY-DESIGN.md`.

## Open threads / next steps (NO mid-diagnosis bugs open — all follow-ups)
1. **Open-in-ComfyUI true auto-open** — ComfyUI 0.27.1's frontend has **no `?workflow=` URL loader** (only `?template=` → `loadTemplateFromUrl`; verified by grepping the installed `comfyui_frontend_package` bundle). So the feature SAVES the graph into ComfyUI's Workflows menu (`civitai-manager/` folder) but the deep-link does NOT auto-open it in the editor on this version. Copy is honest ("open it from the Workflows menu"); the `?workflow=` URL stays for forward-compat. To truly auto-open: needs a newer ComfyUI frontend that honors `?workflow=`, or an API/websocket graph-injection path.
2. **Output-gallery follow-ups** (all 🟢 from the v0.1.68 audit, deliberately deferred): capture is wired into `startRun` only — the **download-and-run path is NOT captured** (fast-follow); `comfy.Client.View` silently **truncates a >64 MiB image** (bump cap / reject-on-truncate); **no total-outputs disk cap** (add a size/count cap + eviction).
3. **ComfyUI custom-node cloud gap** (the real remaining CivitAI-cloud limitation) — a bare `comfy:nodepack` URN is rejected at submit; needs a `comfyNodepackSnapshot` step → `nodepacklayer` AIR (post-paid). Large/uncertain; see COMFYUI-INTEGRATION-DESIGN.md.
4. **Workflow Discovery D3/D4** (per-model related-workflows; dedup-on-import UI) · **App Discovery A2/A3** (per-app detail; polish). See the design docs.
5. **Deferred/minor** — `LoadImage.image` clipspace values are leniently accepted by ComfyUI even when absent from `/object_info` choices → the Incompatible-options detection over-reports them (benign; add an allowlist). `TestScanStatusRaceSafe` remains a pre-existing rare full-suite `-race` **assertion** flake (never a DATA RACE; passes isolated/on retry) — do NOT push a tag in the same compound as the gate.

## Needs the user's eyes (HTTP/markup-verified, not real-DOM this session)
The whole v0.1.66 UX overhaul + v0.1.67 Parameters panel + v0.1.68 `/outputs` gallery were verified at the HTTP/markup level and (for capture/run) via real ComfyUI runs — but the **animations, lightbox clicks, htmx onchange filters, and the redesign's visual polish were NOT DOM-verified** (I did not hijack the user's active Brave tab). Use the `browser` skill for a real visual pass when the user has `:8972` focused.

## Cleanup / side effects
- Dogfood v0.1.68 running unattended on `:8972` (real DB). `pkill -f "dogfood/cm serve"` to stop; scratchpad `dogfood/cm` binary can be cleared.
- Real generations exist in the user's CivitAI account history + local ComfyUI `output/` from live-verify runs (587 substitute/detector/params runs, 575 param + gallery-capture runs). The gallery-capture test generation was created into the real outputs dir then deleted (self-cleaned). All harmless.
- No temp verify instances/seeders/worktrees left (all cleaned per-step).
