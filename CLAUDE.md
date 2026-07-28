# CLAUDE.md — dev & agent conventions

Conventions for working ON this repo. End-user docs live in `README.md`; this
file is for contributors and agents. Module: `github.com/ZacxDev/civitai-manager`
(Go 1.25). Current release line: v0.1.x (latest **v0.1.51**).

## Private dependency — you MUST set GOPRIVATE to build locally

This module depends on the **private** module `github.com/civitai/cli` (its
`pkg/civitai` SDK — auth, download, and read APIs, including the batch by-hash
lookup `GetModelVersionsByHashes`). It is pinned to a real version in `go.mod`
(no `replace` directive is active).

Because that module is private, a bare `go build` / `go test` tries to verify it
through the public checksum database and **fails** — typically a `sum.golang.org`
`500`. Export GOPRIVATE first:

```sh
export GOPRIVATE=github.com/civitai/*
go build ./...
go test ./...
```

A private-dep fetch/sum failure is an **env/config problem, not a real build
break**. If you see "verifying …: sum.golang.org … 500" or an `undefined:`
cascade from `pkg/civitai` symbols, set GOPRIVATE and re-run before concluding
anything is broken.

## Release flow — tag → GoReleaser → GitHub Release

1. Tag a semver on `main`: `git tag vX.Y.Z && git push origin vX.Y.Z`.
2. `.github/workflows/release.yml` runs **GoReleaser** (`goreleaser-action@v6`,
   `release --clean`). Builds are **`CGO_ENABLED=0`** (pure-Go SQLite driver
   cross-compiles cleanly) across **6 targets** — `{linux, darwin, windows}` ×
   `{amd64, arm64}` — producing a GitHub Release with tarballs + `checksums.txt`.

**Deployed ≠ verified.** A green Release job is not proof the binary runs. To
verify a release: download the released tarball for your platform, check it
against `checksums.txt`, extract, and run the binary (`./civitai-manager
--version`). Only then call it verified.

## Architecture (one line each)

- **`internal/library`** — the local-file engine. `scanner.go`: concurrent scan
  (walk → hash worker pool → **ONE** batch by-hash match against civitai → analyze).
  `discover.go`: streaming discovery of model installs across multiple disks.
  `analyzer.go`: flags duplicate / superseded / broken candidates. `quarantine.go`:
  reversible move + manifest (undo-able). `matcher.go`: hash → remote-model match.
- **`internal/web`** — server-rendered UI: **gomponents** + **htmx**, styled with
  the **vendored civitai design system** (theme + components CSS). Runs race-safe
  **streaming jobs** for scan (`scan_handlers.go`) and discovery
  (`discover_handlers.go`): snapshot-under-lock progress, a **Stop** action, and
  poll endpoints. `server.go`/`handlers.go` wire routes; `sanitize.go` scrubs
  untrusted model metadata (bluemonday). Discovery surfaces:
  `discover_workflows.go` — Discover-workflows browse page (`GET
  /workflows/discover`), reuses the model-search card renderer pinned to
  `types=Workflows`; an empty query shows a cached "Popular this month" feed
  (`popularWorkflows`, TTL-cached like `popularModels`).
  `discover_workflow_import.go` — `POST /workflows/discover/{id}/import`: download
  the model's Archive zip(s) → in-memory unzip (zip-bomb guards) → store each
  `.json` as a Workflow, deduped by canonical **graph content-hash** (migration
  `0011_workflow_graph_hash.sql`, `store.GraphHash`/`WorkflowExistsByGraphHash`),
  pre-linked to the source model/version. `discover_apps.go` (+ `internal/civitai/
  apps.go`) — Apps discovery (`GET /apps/discover`): new `/api/v1/apps` client
  (`ListApps`), browse + click-to-play as a scheme-validated external link. The
  model detail page uses version-tab base-model grouping + a shared `modelCardCore`
  card (stats-as-SVG-icons, "Updated" bottom-left).
- **`internal/store`** — SQLite via **`modernc.org/sqlite`** (pure Go, **no
  cgo**). Schema is embedded, **ordered** migrations (`migrations/*.sql`, via
  `go:embed`, applied in filename order). Subscriptions, queue, events,
  local-files, quarantine, model-cache, settings.
- **`internal/civitai`** — thin wrapper over the `pkg/civitai` SDK + path helpers.
  **Data gotcha:** a model's `modelVersions[]` is ordered by the creator's `index`
  (primary/featured first), **NOT by publish date** — positional `[0]` == the primary
  version == what the detail page defaults to. To find the NEWEST version, sort by
  `publishedAt` yourself (assuming `[0]` is newest caused a ship-then-revert).
  **More CivitAI API gotchas** (all cost live-caught bugs — green in fake-reader
  tests, broken against reality):
  - The models list API filters by **`types` (PLURAL)** — singular `type=` is
    silently ignored and returns mixed/unfiltered results. Always `q.Set("types",
    …)` (cost a Discover D1 bug).
  - The app-listing API (`/api/v1/apps`) item **`id` is a ULID string** (e.g.
    `apl_01K…`), NOT an int — though `creator.id` IS an int (cost an Apps A1 decode
    bug).
  - `/api/v1/apps` is **default-closed / launch-gated**: returns `{"items":[]}` for
    a normal user until CivitAI flips the marketplace flag; it auto-opens with no
    code change. Build apps features flag-tolerant.
- **`internal/comfyext`** — the **embedded ComfyUI helper extension** (`extension/`,
  `go:embed all:extension`) + its safe `Install`/`Uninstall`/`Inspect`.
  **ComfyUI has NEVER had a `?workflow=` URL param** in any frontend version
  (verified by sourcemap extraction across 1.45.20 / 1.47.10 / ~1.49 — the only
  workflow-opening params are `template`/`source`/`mode` and a cloud-only `share`;
  upstream requests have sat unanswered for 10+ months). The old deep link
  therefore silently landed the user on whatever graph was open last. The fix is
  this custom-node package: `GET /civitai-manager/ping` (feature detection),
  `POST /civitai-manager/open` (websocket broadcast → an already-open tab jumps),
  and `web/civitai_manager.js` honouring `?cm_open=<path>`. Install rules:
  `custom_nodes/` must exist, a directory we did not write (JSON marker) is NEVER
  clobbered, writes are staged+renamed, paths are containment-checked. **Route
  registration is startup-only → installing/removing needs ONE ComfyUI restart.**
  **Detection needs BOTH legs — ping AND the frontend asset** (`ExtensionPing` +
  `ExtensionAsset` on `/extensions/civitai-manager/civitai_manager.js`, requiring
  200 whose bounded body carries `comfyext.AssetMarker`). Startup-only route
  registration means a DELETED helper keeps answering `/civitai-manager/ping` with
  our exact body until ComfyUI restarts — a **zombie** — while the static asset
  route (served from disk) 404s immediately. Ping-only detection therefore reported
  "helper present" and claimed a jump while the frontend half was not loaded at all
  and nothing could happen (live-caught v0.1.72 bug). The zombie state's ONLY fix is
  a ComfyUI restart, so say that rather than pretending it worked.
  The frontend script feature-detects every undocumented API
  (`app.extensionManager.workflow.getWorkflowByPath`, `wf.load`,
  `store.openWorkflow`, `app.loadGraphData`) and fails silently — it must never
  break the user's editor. It defers the `?cm_open=` open to `afterConfigureGraph`
  because ComfyUI restores its previous workflow AFTER extension `setup()`.
  Config: `comfy_root` / `--comfy-root` (defaults to the `comfy_model_path` parent
  when that looks like a ComfyUI install). **The "Open in ComfyUI" control is a real
  `<form method=post target=_blank>`, NOT an htmx button** — the browser opens the
  tab synchronously from the click (no popup blocker, no JS) so the handler can
  303-redirect it into `<comfy_url>/?cm_open=<path>`. An htmx POST can only answer
  with markup, which is how this once shipped as "we saved it, now click this OTHER
  link". Helper install/remove lives in a collapsed **"ComfyUI helper (advanced)"**
  disclosure, never in the per-click result: an inline "Remove helper" button beside
  the success text got clicked by a user who did not know it disabled the feature.
  The two endpoints take **only** a CSRF token (no `workflow_id`), so they are
  usable from any surface and reflect nothing from the request.
- **`internal/queue`** — download queue (single active-per-item invariant).
- **`internal/poller`** — polls subscriptions, diffs version lists, enqueues new.
- **`internal/cli`** — cobra commands (`root.go`, `commands.go`, `serve`, `search`,
  `library`, `verify`); `buildinfo.go` resolves `--version`.
- **`internal/config`** — YAML config load/validate. `internal/hashutil` — hashing.

## Invariants to preserve

- **Offline / no-CDN.** The civitai theme+components CSS and `htmx.min.js` are
  **vendored** and served via `go:embed` (`internal/web/assets/`). Do not
  reintroduce external CDN/script/style/font references.
- **Theme-aware.** UI honors `data-theme` (light/dark) — keep both paths styled.
- **Tailwind is a committed, purged static build.** `internal/web/assets/output.css`
  is a purged **Tailwind v3.4.17** build (content glob `./*.go`) — NOT regenerated
  automatically, so a NEW utility class in a `h.Class("…")` string is **unstyled until
  you rebuild**. Regenerate after adding/removing template classes:
  ```
  cd internal/web && nix-shell -p tailwindcss --run \
    "tailwindcss -c tailwind.config.js -i input.css -o assets/output.css --minify"
  ```
  (Inputs: `tailwind.config.js` — remaps the slate/indigo/… palette onto `--civitai-*`
  theme tokens — + `input.css`.) For **custom** CSS (not a Tailwind utility), add a
  `.cm-*` class to `internal/web/assets/app.css` (theme-aware via `--civitai-*`); it's
  served as-is and survives the purge (hence `.cm-blur`, `.cm-masonry`,
  `.cm-updated-pop`, `.cm-vstatus-pop`, `.cm-video-badge`, …).
- **NSFW mode `hide | blur | show`.** `hide` must **OMIT** the content
  server-side (not just CSS-hide it), `blur` renders blurred, `show` renders plain.
- **CSRF on every POST.** All state-changing endpoints carry/validate a CSRF token.
- **Loopback-gating.** Endpoints that take an arbitrary filesystem path
  (scan/browse/discover) are gated to loopback — do not expose them to non-local
  callers.
- **Race-safe streaming jobs.** Append to a job's progress AND snapshot it **both
  under the job mutex**. The client poller must target a **stable container**
  element — never `outerHTML`-replace the polling node itself (self-replace breaks
  the poll loop).
- **Hash cache** keyed by `(path, size, mtime)` makes re-scans fast — preserve the
  key; do not invalidate it gratuitously.
- **Remote match defaults ON.** Scan matching (`match_remote`) is on by default and
  **sends file SHA256s to civitai.com**. Keep that opt-out honored and keep the
  data-egress behavior obvious to the user.

## Working conventions that held up

- **Dispatch feature work to subagents that COMMIT IN SMALL, COMPILABLE
  INCREMENTS.** A bare `git commit` sweeps *all* staged changes into one commit and
  yields broken intermediate trees; commit in small steps so every interruption
  leaves the tree buildable.
- **Run the deterministic verify-agent gate** (fresh `go build`/`vet`/`test`) before
  trusting any "done" claim — read the gate's verdict, not the agent's prose.
  Remember to set `GOPRIVATE` for that gate (see above).
- **Feature subagents leave necessary bumps UNCOMMITTED** — more than once a test or
  schema-version bump passed in the dirty working tree but was never `git add`ed,
  leaving a **committed tree that FAILS**. After ANY subagent: `git status` must be
  clean AND re-run the real gate **on the committed tree** (the verify-agent gate
  catches this — trust it over the agent's "green").
- **Stale `<new-diagnostics>` after a subagent are almost always false.** The
  classic false-alarm signature: a `go.mod` "updates needed / go mod tidy" warning
  + an `undefined: X` cascade across many files + cross-branch/worktree symbol
  mismatches. That's the LSP indexing a transient/mixed tree after a checkout or
  worktree switch — **not** a real break. Re-run the real `go build`/`go test`
  (with GOPRIVATE) as the arbiter.
- **Run `/audit-pr` before merging** — but scale it to blast radius. Full adversarial
  `/audit-pr` for: web endpoints, outbound egress, untrusted input (zips, URLs-in-
  href), DB migrations, concurrency, security. For a **pure-UI / bug-fix / mirror of
  an already-audited pattern**, gate + HTTP-level live-verify + a focused self-review
  is sufficient (precedent: v0.1.45/0.1.50/0.1.51). Always live-verify regardless.
  `/audit-pr` surfaced a real bug on nearly every high-blast PR this session.
- **Fake-reader unit tests can encode the WRONG assumption about the real CivitAI
  API — they pass green while the real integration is broken.** For ANY new civitai
  API integration (search params, response decode), you MUST live-verify the request
  params AND the response decode against the REAL API (curl the endpoint / run the
  built binary against it), not only synthetic-body tests. This session had THREE
  such catches: `types` plural, app `id` ULID, `modelVersions[]` ordering — all green
  in tests, all broken against reality.
- **Verify htmx/interaction changes at the HTTP level — real browsers are
  unavailable here** (MCP Playwright is broken on this NixOS host AND system
  chromium is NOT installed, so `executablePath` doesn't work either). What works:
  a button's `hx-vals`/form IS the exact request it issues, so `curl` that request
  against the running dogfood binary and assert the returned fragment — this
  reproduces the click's server-side effect without a browser. For
  **state-mutating** actions (subscribe/unsubscribe, quarantine, downloads) do NOT
  exercise them against the user's real DB
  (`~/.config/civitai-manager/civitai-manager.db`) — that creates real
  subscriptions/downloads. Spin up a **throwaway temp DB** (start the binary once to
  run migrations, seed the needed rows via a tiny in-module `cmd/` seeder using the
  `store` package, then verify), and delete the seeder + temp DB afterward. The
  **dogfood serve pattern**: a `serve` instance runs against the real DB on a
  loopback port (e.g. `:8972`) for live verification + user dogfooding — rebuild and
  restart it after each merge. Honest caveat: HTTP-level reproduction verifies the
  server response, not the actual browser DOM/JS dispatch — say so when reporting,
  and never silently skip interaction verification. Two specifics: (a) the
  **download-enqueue** endpoint (`POST /models/{id}/download`) makes the worker
  **actually download the file** — verify it against a temp DB with a
  `--max-file-size` guard (or kill fast) so you don't pull multi-GB models; (b) on
  model/search pages the CSRF token is embedded in the button's **`hx-vals` JSON**
  (`csrf_token&#34;:…`), not a `<input name="csrf_token">` — extract it from there
  when curl-reproducing a POST.
- **Live-data verify tooling (grounds "what does reality return" without touching the
  app).** The **`civitai` CLI** at `/home/zach/go/bin/civitai` fetches real CivitAI
  data: `models get <id> --json`, `models search --query … --json`, `images search
  --meta --json`, `download <id> --type Workflows` (Workflow models are zips of
  `.json`). A **local ComfyUI (v0.27) at `http://127.0.0.1:8188`** (the default
  `comfy_url`) live-verifies the workflow run/convert path (`/object_info`, `/prompt`,
  `/history`, `/view`).
- **Dogfood binary swap is a 4-step SEQUENCE, not a compound:** `pkill -9 -f
  "dogfood/cm serve"` → confirm the port is free by a `curl` returning `000` (do NOT
  trust `pgrep`, which matches its own shell) → `cp` the new binary → start. A
  compound `kill; cp` hits "Text file busy" because the old process still holds the
  file.
- **zsh loop gotchas that cause flaky verify loops:** unquoted `for x in $var` does
  NOT word-split in zsh (use `while IFS= read -r x` from a file/`<<<`, or
  `${(f)var}`); `curl` inside a loop consumes the loop's stdin — pass `</dev/null`;
  `pgrep -f PATTERN` matches the very shell running the command.
- **Converter reality (ComfyUI UI→API):** a UI-graph node input slot's `type` can be
  a **string OR an array** (COMBO/enum inputs carry their option list as the type) —
  decode it as `json.RawMessage` (v0.1.51 array-type fix). The converter also handles
  subgraph expansion, Get/Set teleports, rgthree UI-only nodes, and object-form
  `widgets_values` (v0.1.46). CivitAI cloud (CustomComfy) rejects a **bare
  `comfy:nodepack` URN** at submit — it needs a `comfyNodepackSnapshot` step →
  `nodepacklayer` AIR (post-paid), so custom-node cloud runs are NOT yet supported
  (see COMFYUI-INTEGRATION-DESIGN.md).
- **Parallel subagents on this repo:** pass `isolation: "worktree"` so their edits
  can't collide in the shared working tree.
