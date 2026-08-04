# civitai-manager — session handoff

_Durable conventions and lessons live in the repo `CLAUDE.md` — **read it first**. This
doc is OPEN THREADS and the commands that tell you where things stand._

🔴 **The rule for editing this file: if a line will be false next week, delete it or turn
it into a command the reader runs.** A PR number, a commit sha, a "currently in flight"
list and a release version are all facts that expire — `gh pr list` and `git log` do not.
Prefer being *less* specific and *still true*. (This survived a session that rewrote it
three times for exactly that reason.)

## ⏭️ Kickoff (paste to start next session)

> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read
> `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first, then run the
> orientation block in the handoff to see where things actually stand — do not trust
> numbers written in the doc. **GOPRIVATE is NOT needed.** Standing OK to push+tag+release
> without asking — run the real gate (`go build ./... && go vet ./... && go test ./... &&
> go test -race ./internal/web/... && gofmt -l ./internal/ ./e2e/ ./*.go`, **plus
> `build`/`vet`/`test` again inside the NESTED `e2e/uxaudit` module**, which a root
> `go test ./...` never compiles, **plus `.github/deadcode.sh` under `nix-shell -p go_1_26`**),
> plus `/audit-pr` scaled to blast radius and **a delta re-audit after EVERY fix round**;
> then **push `main` BEFORE tagging**, verify the tarball (checksum + attestation **with a
> negative control**), refresh the `:8972` dogfood (kill by `pgrep -x cm` +
> `/proc/<pid>/exe`, wait until NO cm process remains — **a free port is not a released
> binary** — then verify the served build by pid + `--version`). If `go.mod` changes at all,
> re-run `nix build .` for `vendorHash`. **You CAN drive a real browser** — it has found a
> real bug in every visual branch across eight sessions. Loop: feedback → recon → clarifying
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

## Open threads (ranked)

1. **`handleWorkflowImportPNG` stores a `prompt` chunk as `format=api` without calling
   `DetectFormat`.** `internal/web/workflow_handlers.go`; the only validation is
   `looksLikeJSON` (first non-whitespace byte is `{` or `[`, `internal/comfy/pnginfo.go`).
   Drag in a PNG carrying a truncated or UI-shaped graph and you get a row the app calls
   api-format. The **consequence** is closed — the run gate now refuses unusable graphs and
   the readiness line says `unknown` — but the **cause** is open, and it is the one path
   that skips format detection.

2. **`cardInstallBlockedText` has never been axe-scanned, in any wording.** It renders
   inside a `<dialog>` opened with `showModal()`, and the walk never opens it — the
   `"Choose a model…"` trigger row is in every run-failure accessibility tree, the dialog's
   subtree in none. Reaching it means teaching the walk to open a dialog. Same class:
   content newly collapsed behind `<details>` is now scanned only in its **collapsed**
   state (it passed expanded before it was collapsed).

3. **`library-sources` is a permanently flaky axe capture** — it differs between two runs
   of the **same tree**. Cause is known and is *not* what was previously assumed: the
   harness's randomized boot dir renders as the selected-scan-directory label
   (`/tmp/uxaudit-walk-<random>/models`), not disk-usage figures. Useless as a visual
   baseline until the dir is stabilised or the path masked. **A capture that varies without
   a code change quietly erodes the baseline everything else is measured against.**

4. **Five `overflow-x-auto` regions still lack `tabindex`** (`cloud_pages.go`,
   `library_pages.go` ×3, `pages.go`, `layout.go`). One instance of this class was fixed
   (a *serious* `scrollable-region-focusable` on the `git clone` block); these are unflagged
   only because they do not overflow with the lab fixture's data.

5. **The bad-option surface has no in-app setup affordance.** It renders with zero missing
   models and zero `#comfy-setup` containers, so its copy correctly names no control — but
   a user blocked there still has no one-click path. Needs a **shared single-instance**
   control; a second `id="comfy-setup"` would be a duplicate-id bug.

6. **Work the tier-B `deadcode` ledger down.** Entries were **measured, not investigated**.
   Spot-checked ones are *superseded wrappers*, not dropped features. Cleanup debt, not a
   user-visible regression — and the ledger is asserted, so it cannot grow silently.

7. **The "why is this tag not shown" affordance was never built.** `StopwordTags`' comment
   described it as existing; it never did. `internal/web/workflow_facets.go` drops stopword
   tags silently with no on-screen explanation. (That function is now deleted; the gap is not.)

8. **Two unexamined stale worktrees**: `civitai-manager-breadcrumbs` (`feat/breadcrumbs`)
   and `civitai-manager-copy-reduction` (`fix/copy-reduction`). ⚠ `feat/comfy-model-cache`
   proved a branch can be **fully superseded** — every claim independently reimplemented on
   `main`, and porting it would have shipped two live bugs. Evaluate before porting *or*
   deleting; `claudedocs/BRANCH-EVAL-comfy-model-cache.md` is the template.

## Live diagnosis state (durable findings, not status)

### The operator has NO `config.yaml`, and that is the state the run-failure work targets

`comfy_model_path` is unset; their ComfyUI answers 200 on `127.0.0.1:8188`. That combination
is what made the panel's primary CTA dead for so long. There is now an in-panel setup flow
that infers the models root from ComfyUI's `/internal/folder_paths` (parent of the most
categories — a category lists several roots in `extra_model_paths.yaml` order, which is
**not** a preference order) and stores it as a **settings row**, not a config file.

🔴 **Do not "fix" this by writing them a `config.yaml`.** The unset state is the case under
test, and creating that file is a surprising side effect on their machine.

Fail direction: absent endpoint, timeout, a genuine tie, or a suggestion that fails
re-validation all degrade to the same type-it-yourself form; a non-local `comfy_url` gets no
form at all. Hostile-payload hardening requires ≥2 agreeing categories **and** that the
winning root already contains a reported category directory.

### The readiness line and the run-failure panel are coupled — do not edit one alone

They answer the same question on either side of the Generate click, and shipping them in
consecutive releases put **both on screen at once, 0px apart**. The rule now: *a run for this
workflow exists ⇒ the readiness line yields*, in **every** terminal state — after a
**successful** run the line is not merely redundant, it is **false**, because the panel's
CTAs install things.

Enforced by one predicate read by the fragment **and** every handler writing into
`#run-status`, each answering with an out-of-band clear. 🔴 **A new writer that forgets the
OOB clear reproduces the bug.** Guarded structurally (an AST check pinning who may call the
raw fragment, against an asserted two-entry ledger) *plus* a behavioural table for the case
the structural check type-checks straight past.

### The custom-node detector is authoritative, with two staleness windows

It reads `python_module` from a cached `/object_info` (deny-list on `custom_nodes.*`), with
the old hand-written table as the cold-cache fallback — deleting that table is **unsafe**,
since `PrimitiveNode`/`Note`/`Reroute` are frontend-only and absent from every
`/object_info`. Two accepted windows: installing a custom node does not invalidate the cache
until the next run, and a **library scan deletes the cache row**, so for API-format
workflows the old table's false positives return until the next local run.

### `web_scan_timeout`: live, with an upgrade hazard

🔴 `docs/configuration.md` shipped `web_scan_timeout: "2m"` in its annotated sample for ~89
releases while nothing read the key — so hand-copying the docs was the only way to hold an
explicit value, and those users now have an enforced 2m. **A deadline firing during the HASH
phase persists ZERO rows** (hashing is phase 1, `local_files` are written in phase 3).
A release note would be kind.

### The ComfyUI model cache repopulates on use; empty is a designed state

Written by the local-run and cloud-panel paths, invalidated after a library scan. Until it
populates, the ◎ ("in ComfyUI") chip state cannot render and chips correctly fall back to
✓/✗. Scan invalidation is a heuristic — a scan means *our* file set changed, not ComfyUI's.

## How to verify a release

```sh
gh release download "$(git describe --tags --abbrev=0)" -R ZacxDev/civitai-manager \
  -p '*_linux_amd64.tar.gz' -p 'checksums.txt'
sha256sum -c --ignore-missing checksums.txt      # must print OK
tar xzf ./*_linux_amd64.tar.gz && ./civitai-manager --version
gh attestation verify ./*_linux_amd64.tar.gz -R ZacxDev/civitai-manager
AUDITLOOP_CHROMIUM=/run/current-system/sw/bin/brave make ux-audit
```

🔴 **The axe walk must report 26 captures / 0 violations.** A different **capture count**
means the walk changed shape. Count from `*.axe.json` (**not** `*.a11y.json`, which has no
`violations` key at all), with an explicit `is None` check, and assert **zero payloads
lacking the key** — an empty violations list is falsy, and `x or y` on it silently skips
healthy captures and prints "0 captures", which reads as a regression.

`gh attestation verify` is **silent on success** in some shells. Do not read a quiet `rc=0`
as proof — append one byte to a copy and confirm it gives non-zero.

## Corrections, so they are not re-derived

- **`git tag --contains` in a worktree returns the same count as the base clone** — worktrees
  share the ref store.
- **The `e2e/uxaudit` CI blind spot is closed** — a dedicated job compiles the nested module.
  Only the *local* root-module exclusion still bites.
- **`pickFileFromModelRaw` never lived in `internal/comfy/download_target.go`** — the
  offer-don't-perform behaviour is `pickFileFromModel` + `renderSubstituteOffer` in
  `internal/web/run_download.go`; `TypeSubdir` is the part that really is in
  `download_target.go`.
- **The `deadcode` allowlist "dead UI" scare was wrong** — superseded wrappers, not dropped
  features.
- **`e2e/uxaudit/fakes.go` no longer claims `realRun` returns early on conversion warnings** —
  it does not, and the `input_order` requirement holds for the *opposite* reason. Settled by
  a measured probe, not by reasoning.
- 🔴 **A probe can be vacuous the way a test can.** Stripping `input_order` from ONE loader
  leaves a second loader independently holding `report.OK` false, so the result is identical
  under both hypotheses — and it was paired with a decision rule that would have deleted a
  correct 🔴 block. Strip **both** loaders. An experiment needs a positive control too.
- **A perf figure was revised three times** (~21–34 ms → ~6–8 ms scaled from a synthetic →
  **~3–5 ms** against the real payload). Only a SQLite read is avoided; the ~21 ms parse is
  still paid. Do not quote a derived ratio from that comment.
