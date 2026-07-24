# CLAUDE.md — dev & agent conventions

Conventions for working ON this repo. End-user docs live in `README.md`; this
file is for contributors and agents. Module: `github.com/ZacxDev/civitai-manager`
(Go 1.25). Current release line: v0.1.x.

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
  untrusted model metadata (bluemonday).
- **`internal/store`** — SQLite via **`modernc.org/sqlite`** (pure Go, **no
  cgo**). Schema is embedded, **ordered** migrations (`migrations/*.sql`, via
  `go:embed`, applied in filename order). Subscriptions, queue, events,
  local-files, quarantine, model-cache, settings.
- **`internal/civitai`** — thin wrapper over the `pkg/civitai` SDK + path helpers.
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
- **Stale `<new-diagnostics>` after a subagent are almost always false.** The
  classic false-alarm signature: a `go.mod` "updates needed / go mod tidy" warning
  + an `undefined: X` cascade across many files + cross-branch/worktree symbol
  mismatches. That's the LSP indexing a transient/mixed tree after a checkout or
  worktree switch — **not** a real break. Re-run the real `go build`/`go test`
  (with GOPRIVATE) as the arbiter.
- **Run `/audit-pr` before merging** web-endpoint, concurrency, quarantine/
  filesystem, DB-migration, or security PRs. It surfaced a real bug on nearly every
  such PR this session.
- **Browser-verify client-side htmx bugs.** MCP Playwright is broken on this NixOS
  host — fall back to system chromium via `executablePath`. Never silently skip the
  browser check for an interaction bug.
- **Parallel subagents on this repo:** pass `isolation: "worktree"` so their edits
  can't collide in the shared working tree.
