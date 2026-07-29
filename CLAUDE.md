# CLAUDE.md — dev & agent conventions

Conventions for working ON this repo. End-user docs live in `README.md`; this
file is for contributors and agents. Module: `github.com/ZacxDev/civitai-manager`
(Go 1.25). Current release line: v0.1.x (latest **v0.1.51**).

## The `civitai/cli` dependency — GOPRIVATE is NO LONGER required

This module depends on `github.com/civitai/cli` (its `pkg/civitai` SDK — auth,
download, and read APIs, including the batch by-hash lookup
`GetModelVersionsByHashes`), pinned to a real version in `go.mod` (no `replace`
directive is active).

**That module is now PUBLIC** (verified 2026-07-28: `github.com/civitai/cli`
returns 200 and `proxy.golang.org/github.com/civitai/cli/@v/list` serves its
versions). A bare `go build` / `go test` resolves it through the module proxy
with normal `sum.golang.org` verification — and `go install
github.com/ZacxDev/civitai-manager@latest` therefore works for an outside
contributor, which is what the public README now assumes.

```sh
go build ./...
go test ./...
```

`export GOPRIVATE=github.com/civitai/*` is harmless and still appears in older
handoff docs, but it is no longer needed. **Historical note:** the SDK used to be
private, and a `sum.golang.org … 500` plus an `undefined:` cascade from
`pkg/civitai` symbols was an env/config problem rather than a real break. If you
now see that signature, treat it as a genuine failure to investigate, not
something GOPRIVATE will paper over.

## Release flow — bump flake → tag → GoReleaser → GitHub Release

1. **Bump `version` in `flake.nix`** and commit it. It is a hard-coded string
   feeding the same `-X main.version` ldflag GoReleaser uses; forget it and Nix
   users get a binary reporting the PREVIOUS release.
2. Tag a semver on `main`: `git tag vX.Y.Z && git push origin vX.Y.Z`.
3. `.github/workflows/release.yml` runs **GoReleaser** (`goreleaser-action@v7`,
   `release --clean`). Builds are **`CGO_ENABLED=0`** (pure-Go SQLite driver
   cross-compiles cleanly) across **6 targets** — `{linux, darwin, windows}` ×
   `{amd64, arm64}` — producing a GitHub Release with tarballs, **`.deb`/`.rpm`**
   (`nfpms:`, linux amd64+arm64), `checksums.txt`, and **build attestations**
   (`actions/attest@v4`, `subject-checksums: dist/checksums.txt`, keyless OIDC).

**Distribution gotchas that cost real bugs:**

- **`flake.nix`'s `vendorHash` is not derived from anything the Go build reads.**
  A `go get` silently invalidates it and `nix build
  github:ZacxDev/civitai-manager` starts failing for everyone with a hash
  mismatch — this shipped broken on the public path once already. After any
  `go.mod`/`go.sum` change: `nix build .`, copy the `got:` hash from the error,
  paste it in. **Never guess it.** The `nix flake check` CI job is the only thing
  that catches this.
- **The flake's `src` is a `lib.fileset`, not `./.`** — deliberately. `src = ./.`
  swept untracked `.claude/` agent worktrees into the build and broke it. If you
  add a new top-level directory the build needs, add it to the fileset.
- **`brews:` is dead** — hard-deprecated in GoReleaser v2.16, removed in v3.
  `homebrew_casks:` is the supported spelling and covers Linux too.
- **The Homebrew cask publish is LIVE.** `ZacxDev/homebrew-tap` is public with
  `main` + `Casks/`, and `HOMEBREW_TAP_GITHUB_TOKEN` is set on this repo
  (verified 2026-07-28), so a tagged release opens a cask PR against the tap.
  The `skip_upload` guard **stays** — it is a permanent fail-soft, not
  scaffolding for the pre-tap era. GoReleaser resolves `skip_upload` **before**
  `repository.token`, so an expired/revoked/absent secret skips the cask instead
  of failing the release. That matters because GoReleaser publishes the GitHub
  Release (`release.Pipe{}`) **before** the cask (`cask.Pipe{}`) — a late cask
  failure aborts the job with the artifacts already public. For the same reason
  `release.yml`'s attestation step carries
  `if: !cancelled() && hashFiles('dist/checksums.txt') != ''`: unguarded, a cask
  failure silently costs every artifact its provenance attestation.
- **The cask's Gatekeeper bypass is deliberate — do not "clean it up".** The
  postflight `xattr -dr com.apple.quarantine` is load-bearing, and it was
  verified from Homebrew's source rather than assumed:
  `Cask::Download#extract_primary_container` calls
  `Quarantine.propagate(from:, to: staged_path)`, which globs `to/**/*` and
  stamps every non-symlink — **no artifact-type branching**, so a `binary`-stanza
  cask's Mach-O is quarantined and `$(brew --prefix)/bin/<name>` symlinks to it.
  `--no-quarantine` was deprecated in Homebrew 5.0 and **deleted in 5.2**, so the
  user cannot opt out. A quarantined non-notarized Mach-O run from Terminal is
  **terminated** by syspolicyd, not warned about (reported against a cask
  `binary` artifact on macOS 26.4, microsoft/vscode#309147). **Ad-hoc signing is
  not a substitute**: `rcodesign print-signature-info` on our own dist/ output
  shows darwin/arm64 ALREADY carries `ADHOC | LINKER_SIGNED` (Go's linker signs
  darwin/arm64 — `NeedCodeSign` in `cmd/link/internal/ld/lib.go`), darwin/amd64
  carries nothing, and neither satisfies Gatekeeper. The only real fix is
  Developer ID + notarization + a **stapled** `.pkg`/`.dmg` (a ticket cannot be
  stapled to a bare Mach-O). Two hazards if you edit the hook: the cask DSL's
  `system_command` is `run!` (**raising**) — pass `must_succeed: false`; and
  `Cask::DSL::Base#method_missing` **raises**, so `opoo`/`ohai` inside a
  postflight abort the install.
- **`docs/install.sh` is piped into strangers' shells.** POSIX sh, shellcheck-
  gated in CI, checksum verification is mandatory, no surprise `sudo`, https
  only. See `docs/README.md` for the rules before touching it.
  **busybox is a first-class target:** Alpine/musl ships busybox `wget` and NO
  curl, and busybox `wget` does not implement GNU's `--https-only` — it prints
  usage and fails, which surfaced as a bogus `download failed: <url>`. Probe for
  a wget flag before using it; do not assume GNU wget.

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
  - **`baseModels` is multi-value ONLY as a REPEATED param** (`q.Add`):
    `baseModels=Illustrious&baseModels=Pony` returns their UNION (verified with 10
    repeated values in one request). **A comma-joined `baseModels=A,B` returns
    ZERO items** — it is parsed as one literal base-model name — so it fails as a
    silently-empty page, not an error. An **unknown** `baseModels` value likewise
    returns `200` with `items: []`.
  - **`tag` is SINGLE-VALUE and fails silently in two ways.** Repeated `tag=a&tag=b`
    → **HTTP 400 ZodError** ("expected string, received array"). Comma-joined
    `tag=a,b` → **200 with results byte-identical to sending no tag at all**. An
    **unknown tag behaves the same way**: the filter is silently DROPPED and the
    **UNFILTERED** feed comes back. That is the dangerous one — a bad tag does not
    return "no results", it returns everything, so the UI renders a filter that is
    lying. **Only ever forward whitelisted tags**, and cover a multi-tag concept
    with **one request per tag, merged + deduped by model id**.
  - `tag` matching is **case-insensitive** (`inpaint` == `Inpaint` == `INPAINT`),
    but **synonyms are NOT unified**: `detailer`, `adetailer`, `facedetailer` and
    `face detailer` each return a DIFFERENT result set.
  - Workflow tags are dominated by noise — over a live 600-model `types=Workflows`
    harvest, `tool` appeared on 379, `comfyui` 179, `workflow` 163, `base model`
    76. Treat those as stopwords; real use-case signal lives underneath
    (`inpaint`, `upscaler`, `detailer`, `controlnet`, `i2v`, …). The curated
    ecosystem/use-case vocabulary derived from that harvest is the single source
    of truth in **`internal/civitai/taxonomy.go`** — used by BOTH
    `/workflows/discover` and the local library workflow list, so adding a family
    is a one-line table edit and the two surfaces can never drift.
  - Cloudflare **403s (`error code: 1010`) a bare Python `urllib` User-Agent** when
    probing the API by hand. `curl` works; from Python set
    `User-Agent: curl/8.5.0`. Do not read a 1010 as "the endpoint is gated".
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
- **NSFW mode is TWO-STATE in production: `blur ⇄ show`.** The toggle cycles only
  those two (`layout.go` `nsfwToggle`), and `normalizeNSFWMode` **migrates any
  stored `hide` → `blur`**. Every caller passes the normalized mode, so the
  server-side `NSFWHide` omit branches are **UNREACHABLE in production** — they
  are an inert, preserved capability that is still worth keeping unit-testable.
  Do NOT write or trust a test asserting "hide omits" as live behaviour, and do
  not claim three modes in user-facing docs (two independent agents and a live
  grep caught that claim in 2026-07).
  `blur` is a **browser-side CSS filter** — the unblurred bytes still go over the
  wire — so it is a shoulder-surfing guard, not an access control. Anything that
  must not be served has to be omitted server-side.
  **Open decision:** either restore a real `hide` (re-add it to the cycle and stop
  normalizing it away) or drop the "hide must OMIT" invariant. Today the code and
  this file disagree.
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
- **Install-and-run must NEVER substitute a file silently.** CivitAI renames files
  across versions, so a workflow's expected filename routinely matches **zero**
  files inside the model that bears its name — `pickFileFromModelRaw`
  (`internal/comfy/download_target.go`) then falls back to the primary version's
  primary file. That fallback must be **offered, not performed**: the first click
  returns an offer naming BOTH files, and only a second click carrying
  `confirm_substitute=1` **and** `confirm_file=<remote basename>` proceeds — the
  echoed basename means a primary-version promotion between the two clicks
  re-offers instead of installing something the user never approved. Once
  confirmed, every progress line must read `<remote> as <expected>`. An exact
  filename match stays ONE click. The model's type is cross-checked against the
  **destination folder** via `TypeSubdir`, **not** the raw type string —
  LORA/LoCon/LyCORIS all route to `loras/` and must stay equivalent (a stricter
  raw-string check shipped once and refused legitimate LoCon installs).
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
  (GOPRIVATE is no longer needed — see the dependency note above.)
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
  as the arbiter.
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
- **Multi-mode template detection keys on `toggleRestriction`, not on bypass shape**
  (`internal/comfy/modes.go`). Template packs ship several pipelines in ONE graph
  with all but one bypassed. The mode set is derived from an **ACTIVE rgthree `Fast
  Groups Bypasser`/`Fast Groups Muter` whose `properties.toggleRestriction` is
  `"max one"` or `"always one"`** — i.e. the author's own declaration of
  exclusivity — and group membership uses LiteGraph's `containsCentre` geometry.
  **Group titles are labels only; never key on them.** This was chosen over the
  obvious "uniformly bypassed groups" heuristic because, measured across 13 real
  graphs, that heuristic misfires on **every** workflow in pack 1386234 (15–31
  optional-feature groups each). A sub-group nested inside a mode group but driven
  by a *different* toggler keeps its stored mode. Handles both bypassed (mode 4)
  and muted (mode 2).
- **Parallel subagents on this repo:** pass `isolation: "worktree"` so their edits
  can't collide in the shared working tree.
