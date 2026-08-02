# civitai-manager — session handoff

_Durable conventions and lessons live in the repo `CLAUDE.md` — **read it first**. This
doc is OPEN THREADS and the commands that tell you where things stand._

🔴 **This file has been rewritten three times in one session because it kept enumerating
facts that expire.** The rule for editing it: **if a line will be false next week, delete
it or turn it into a command the reader runs.** A PR number, a commit sha, a "currently in
flight" list and a release version are all facts that expire — `gh pr list` and `git log`
do not. Prefer being *less* specific and *still true*.

## ⏭️ Kickoff (paste to start next session)

> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read
> `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first, then run the
> orientation block in the handoff to see where things actually stand — do not trust
> numbers written in the doc. **GOPRIVATE is NOT needed.** Standing OK to push+tag+release
> without asking — run the real gate (`go build ./... && go vet ./... && go test ./... &&
> gofmt -l ./internal/ ./e2e/ ./*.go`, **plus `build`/`vet`/`test` again inside the NESTED
> `e2e/uxaudit` module**, which a root `go test ./...` never compiles), plus `/audit-pr`
> scaled to blast radius and **a delta re-audit after EVERY fix round**; then **push `main`
> BEFORE tagging**, verify the tarball, refresh the `:8972` dogfood (kill by `pgrep -x cm` +
> `/proc/<pid>/exe`, wait until NO cm process remains — **a free port is not a released
> binary** — then verify the served build by pid + `--version`). If `go.mod` changes at all,
> re-run `nix build .` for `vendorHash`. **You CAN drive a real browser** — it has found a
> real bug in every visual branch across seven sessions. Loop: feedback → recon → clarifying
> Qs + recommend → worktree-isolated subagent(s) → real gate → audit → delta re-audit → ship
> → verify tarball → refresh dogfood.

## Orientation — run these, don't read a number

```sh
gh pr list                                          # what is open RIGHT NOW
git fetch origin && git log --oneline "$(git describe --tags --abbrev=0 origin/main)"..origin/main
                                                    # unreleased commits (empty = queue clear)
git describe --tags --abbrev=0 origin/main          # latest release
grep -n 'version = ' flake.nix                      # what the NEXT nix build will report
ls internal/store/migrations/ | tail -1             # latest migration (check in-flight branches too)
git worktree list                                   # stale agent worktrees under .claude/worktrees/
grep -vc '^\s*#\|^\s*$' .github/deadcode-allow.txt  # size of the tier-B debt ledger
```

⚠ **Agents are often in flight while you read this.** `gh pr list` can be empty and work can
still be happening in a worktree — cross-check `git worktree list` for non-stale entries.

**Housekeeping that is safe whenever you see it:** stale merged worktrees under
`.claude/worktrees/` accumulate (about a dozen at the time of writing) — prune them.
`~/.config/civitai-manager/` holds several ~23 MB `.pre-*` DB backups; the pre-0017 and
pre-0018 ones are long superseded.

## Open threads (ranked)

1. **Proposal C — preflight on the workflow page, before the Run click.** "Needs 1 node
   pack + 3 models" shown *before* the user commits to a run. A and B already shipped
   (`preflightFailureResult` reports missing nodes AND models in one pass). C was blocked on
   a caching policy so the **4.66 MB** `/object_info` is not refetched per render — which is
   what migration `0019` + `internal/web/comfy_model_cache.go` exist for. **The
   node-origin work has now landed, so C is unblocked**; it touches the same workflow page,
   so check nothing else is editing it first.
2. **Orphan route `GET /workflows/run/resolve-model`** — registered in `server.go`,
   loopback-gated, makes outbound egress, and **nothing links to it**. ~8 tests exercise it,
   reading as coverage of the missing-model resolve feature; production renders
   `resolveModelFragmentWithReason` instead. Delete it, or document it as a debug endpoint.
   🚧 **In progress** — a sibling agent was working this at the time of writing. Confirm
   against `gh pr list` / `git log` before starting; its fate depends on that PR.
3. **3 dead `.cm-*` CSS rules**: `.cm-crumb-name`, `.cm-vstatus-date`, `.cm-gen-sep`. Plus
   **`Server.attributeFn`** — a test seam nothing sets, in production or in any test.
   🚧 **In progress** — same sibling agent as (2), same caveat.
4. **Two systematic guards** that would have caught most of this class mechanically: a
   **route-emitter check** (diff registered `mux.Handle*` paths against emitted `hx-*`/`href`
   literals, with an allowlist; ~60 lines) and a **reverse class-coverage test** (~30 lines)
   for CSS rules nothing emits. The `deadcode` CI gate covers Go functions only — these are
   the routing and CSS equivalents, and `class_coverage_web_test.go` is explicitly
   one-directional (see `CLAUDE.md`).
5. **Work the tier-B `deadcode` ledger down.** The entries were **measured, not
   investigated** — none has been checked for whether it should be deleted or wired up.
   Spot-checks found **superseded wrappers, not dropped features** (e.g. `runModesPanel` is
   a 2-line delegate while production calls `runModesPanelSelected`, and the live page
   renders both panels) — cleanup debt, *not* a user-visible regression. `internal/web`
   carries about half the list. The ledger is **asserted**: the gate fails when it grows
   *and* when it shrinks, so a stale entry is a win to take, not a line to keep.
6. **The "why is this tag not shown" affordance was never built.** `StopwordTags`' comment
   described it as existing; it does not. `internal/web/workflow_facets.go` drops stopword
   tags silently with no on-screen explanation. (The function that carried the false comment
   is now deleted; the gap it described is not.)

## Live diagnosis state (durable findings, not status)

### The custom-node detector is authoritative — with two known staleness windows
`comfy.NodeOrigins` (`internal/comfy/node_origin.go`) classifies on the registering
`python_module` from `/object_info`, as a **deny-list on `custom_nodes.*`** — measured to
yield exactly **790** built-ins, where the obvious allow-list (`comfy_extras.*` + `nodes`)
yields 566 and silently reclassifies all 224 bundled `comfy_api_nodes` types as custom. It
replaced a ~50-entry hand-written table that called a real built-in custom in **44 of 70**
workflows. Full reasoning, including why `coreNodeClasses` is **kept** as the cold-cache
fallback (`PrimitiveNode`/`Note`/`Reroute` are frontend-only LiteGraph nodes absent from
every `/object_info`, so deleting it would call them custom), is in `CLAUDE.md`.

Two staleness windows, **both accepted, neither a bug**:
- Installing a custom node does not invalidate the `0019` cache until the next local run.
- A library **scan deletes the cache row** (`internal/web/scan_handlers.go`), so for
  **API-format** workflows — where the cache is the only origin source — the old table's
  false positives return until the next local run. UI-format workflows re-fetch in-request
  and are barely affected, which is why this is accepted rather than fixed. **Do not
  redesign invalidation as a drive-by**; the coupling reasoning is in
  `comfy_model_cache.go`'s `invalidateComfyModelCache` comment.

⚠ **Caution about that PR's performance claim — it was revised three times by successively
better measurement** (~21–34 ms → ~6–8 ms on a scaled synthetic → **~3–5 ms** against the
real 4.66 MB payload), because each earlier figure measured something adjacent to the thing
that changed. Only the SQLite **read** is avoided; the parse is still paid either way. The
correctness half is the load-bearing one and is untouched by the smaller number. **Quote
the measured ranges from `CLAUDE.md`, never a figure derived from them.**

### `web_scan_timeout`: live, with an upgrade hazard
Inert for **89 releases** (v0.1.13 → v0.1.101). Now enforced, default **6h** — the value
`scanJobBudget` already imposed, so unset behaviour is unchanged.
🔴 **`docs/configuration.md` shipped `web_scan_timeout: "2m"` in its annotated sample config
for that whole period**, and no code path writes the key — so hand-copying the docs was the
ONLY way to hold an explicit value. Anyone who copied it now has an enforced 2m. **And a
deadline firing during the HASH phase persists ZERO rows** — hashing is phase 1,
`local_files` are written in phase 3; measured, a 150 ms deadline against a 12 × 200 MB
fixture saved nothing. For a large library that is a total loss, every time.
`docs/configuration.md` carries the upgrade warning; **a release note would be kind.**

### The ComfyUI model cache repopulates on use; empty is a designed state
`cacheComfyObjectInfo` is wired in `run_handlers.go` and `cloud_handlers.go`, with
invalidation after a library scan. Proven end-to-end on the real DB: a **4,661,987-byte**
row with an RFC3339 `updated_at` appeared during dogfooding. Until it populates, the ◎
("in ComfyUI") chip state cannot render and chips correctly fall back to ✓/✗.

### `feat/comfy-model-cache` is retired — do not re-evaluate it
All seven of its claims landed on `main` independently, and where the two differ main's
version fixed a bug the branch still contains (a descendant-selector popover that opens
*every* chip at once, and a Safe-mode button that **fails OPEN** to the full maturity
range). Its tip `46a10ed` is recoverable via
`git push origin 46a10ed:refs/heads/feat/comfy-model-cache` if ever wanted.

## How to verify a release

```sh
V=$(git describe --tags --abbrev=0 origin/main); V=${V#v}
gh release download "v$V" -R ZacxDev/civitai-manager \
  -p "civitai-manager_${V}_linux_amd64.tar.gz" -p checksums.txt
sha256sum -c --ignore-missing checksums.txt          # must print OK
tar xzf "civitai-manager_${V}_linux_amd64.tar.gz" && ./civitai-manager --version
gh attestation verify "civitai-manager_${V}_linux_amd64.tar.gz" -R ZacxDev/civitai-manager
AUDITLOOP_CHROMIUM=/run/current-system/sw/bin/brave make ux-audit
```

🔴 **The axe walk must report 24 captures / 0 violations.** A different **capture count**
means the walk changed shape; **0 captures** means it regressed to the API-format fixture no
real workflow uses — the failure that let it certify a dead surface for four releases. Count
the PNGs (`ls e2e/uxaudit/artifacts/*.png | wc -l`) rather than trusting the exit code.

`gh attestation verify` is **silent on success** in some shells. Do not read a quiet `rc=0`
as proof without a negative control — append one byte to a copy and confirm `rc=1`.

## Corrections, so they are not re-derived

- **`git tag --contains` in a worktree returns the same count as the base clone.** Worktrees
  share the ref store; an earlier claim that it differs was wrong.
- **The `e2e/uxaudit` CI blind spot is closed** — a dedicated `uxaudit` job compiles the
  nested module. Only the *local* root-module exclusion still bites, so run that second
  invocation yourself.
- **`pickFileFromModelRaw` never lived in `internal/comfy/download_target.go`.** The
  offer-don't-perform behaviour is `pickFileFromModel` + `renderSubstituteOffer` in
  `internal/web/run_download.go`; `TypeSubdir` is the part that really is in
  `download_target.go`.
- **"Nothing tests the `canQueue` agreement" was wrong.** Three of four drift directions
  were already caught; only the button-falls-back-to-single-run direction survived.
- **The `deadcode` allowlist "dead UI" scare was wrong** — superseded wrappers, not dropped
  features.
- **`e2e/uxaudit/fakes.go` no longer claims `realRun` returns early on conversion
  warnings.** That mechanism was deleted when the never-submit gate moved *after*
  `comfy.Preflight`; the `input_order` requirement it justified still holds, but for the
  opposite reason — measured, not reasoned. See the comment itself for the numbers, and
  note that the obvious single-entry probe is **under-powered** and would have argued for
  deleting a requirement that is real.
