# civitai-manager — session handoff

_Durable conventions and lessons live in the repo `CLAUDE.md` — **read it first**. This
doc is OPEN THREADS and the commands that tell you where things stand._

🔴 **The rule for editing this file: if a line will be false next week, delete it or turn
it into a command the reader runs.** A PR number, a commit sha, a "currently in flight"
list and a release version are all facts that expire — `gh pr list` and `git log` do not.
Prefer being *less* specific and *still true*. (This survived a session that rewrote it
three times for exactly that reason, and a later one that invalidated every thread in it.)

## ⏭️ Kickoff (paste to start next session)

> Continuing civitai-manager (Go single-binary CivitAI/ComfyUI library manager). Read
> `claudedocs/SESSION-HANDOFF.md`, repo `CLAUDE.md`, and my auto-memory first, then run the
> orientation block in the handoff to see where things actually stand — do not trust
> numbers written in the doc. **GOPRIVATE is NOT needed.** Standing OK to push+tag+release
> without asking — run the real gate (`go build ./... && go vet ./... && go test ./... &&
> go test -race ./internal/{library,store,queue,poller,web}/... && gofmt -l ./internal/
> ./e2e/ ./*.go`, **plus `build`/`vet`/`test` again inside the NESTED `e2e/uxaudit` module**,
> which a root `go test ./...` never compiles, **plus `.github/deadcode.sh` under
> `GOTOOLCHAIN=go1.26.0`**), plus `/audit-pr` scaled to blast radius and **a delta re-audit
> after EVERY fix round**; then **push `main` BEFORE tagging**, verify the tarball (checksum
> + attestation **with a negative control**), refresh the `:8972` dogfood (kill by
> `pgrep -x cm` + `/proc/<pid>/exe`, wait until NO cm process remains — **a free port is not
> a released binary** — then verify the served build by pid + `--version`). If `go.mod`
> changes at all, re-run `nix build .` for `vendorHash`. **You CAN drive a real browser** —
> it has found a real bug in every visual branch across nine sessions. Loop: feedback →
> recon → clarifying Qs + recommend → worktree-isolated subagent(s) → real gate → audit →
> delta re-audit → ship → verify tarball → refresh dogfood.

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
🔴 **Do not delete a worktree by diffing its tree against `main`** — a branch that is merely
*behind* main always differs, so that check refuses every real candidate. Ask the authority:
`gh pr view <n> --json state` = `MERGED`, plus a clean `git status --porcelain`.

## Open threads (ranked)

1. **A release is owed.** The unreleased-commit command above is non-empty and includes
   user-visible behaviour (PNG import classification, a new setup CTA on the run-failure
   surface, a corrected facet-bar note, scroll-region keyboard access). Follow `CLAUDE.md`'s
   release flow — **bump `flake.nix` first**, push `main` **before** tagging.

2. **Two pre-existing `-race` flakes in `internal/web`, unowned.**
   `TestScanStatusRaceSafe` and `TestInstallMissingAndRunMixedAlreadyPresentSaysSo`. Measured
   3/20 and 2/10 failures **under heavy host load**, and 20/20 passing **at idle** — so they
   are load-sensitive, not deterministic. Neither lives in a file any recent PR touched. The
   diagnosed cause of the first: four goroutines poll until `<-done`, then assert
   `polls != 0`, with nothing guaranteeing a poll completes first — **the non-vacuity check
   is itself the racy part.** 🔴 **Consequence for every gate you run: a single green
   `-race` pass on this package means "passed once on a quiet host", not "race-free".** Say
   it that way.

3. **`workflows.resources` has no backfill.** Rows written before the ordering fix keep
   whatever order they were written with, and untouched files are never rescanned
   (`workflowCacheHit` keys on size+mtime). For *this* operator it is moot — measured
   `format` = `ui|71`, i.e. **zero api-format workflows**, and the ui path was never
   randomised. Decide explicitly: backfill api-format rows, or say in the doc comment that
   the ordering contract binds new writes only.

4. **`autoLink` can now pick a different model on re-scan.** `PrimaryCheckpoint(api)` went
   from random to lowest-node-id, and `autoLink` prepends it and takes the first hit. This
   is a net improvement (deterministic beats random) but it *is* a behaviour change: a
   re-scanned multi-checkpoint api workflow can acquire a different `version_id` than it
   currently has. Only fires when a file's size/mtime changes, so it trickles in.

5. **Two guards the ux-audit work left unpinned** (from its own audit, both 🟡):
   the **release-time ownership re-check** in `e2e/uxaudit/workdir.go` works but deleting it
   leaves the suite green; and the **animation-freeze fix has zero committed guards** — no
   test references `freezeAnimationScript`, so deleting it keeps CI green and the capture
   flake returns silently weeks later. The opt-in repeatability instrument is the only thing
   exercising it and it never runs in CI.

6. **`e2e/uxaudit/walk.go`'s scope comment cites a now-fixed app bug.** It justifies keeping
   `expandStaticDetails` scoped to `#run-status` by describing the `ExtractResources`
   map-ordering flake as live and open. Once the ordering fix lands that becomes stale in a
   way that misleads in both directions — it will read as a reason not to widen the scope,
   and as evidence the app bug is still there.

7. **The ComfyUI model cache still has no manual "Refresh" control.** Migration `0019`'s
   header documents three population triggers; only two exist. Known residue, recorded here
   so it is not rediscovered as a bug.

8. **The facet bar's "no known use case matched" is only true for a warm model cache.**
   `model_cache` has exactly one writer (`cachedModelDetail`), reached from model-detail/card
   renders and the Discover import — **a library scan never triggers it**. The copy is now
   hedged ("nothing we have locally matches"), which makes it honest, but the underlying gap
   is real: for a scan-linked workflow the app has never fetched the tags it is reasoning
   about. Populating the cache on scan-link would close it.

## Live diagnosis state (durable findings, not status)

### The operator has NO `config.yaml`, and that is the state the run-failure work targets

`comfy_model_path` is unset; their ComfyUI answers 200 on `127.0.0.1:8188`. That combination
is what made the panel's primary CTA dead for so long. There is an in-panel setup flow that
infers the models root from ComfyUI's `/internal/folder_paths` (parent of the most
categories — a category lists several roots in `extra_model_paths.yaml` order, which is
**not** a preference order) and stores it as a **settings row**, not a config file.

🔴 **Do not "fix" this by writing them a `config.yaml`.** The unset state is the case under
test, and creating that file is a surprising side effect on their machine.

Fail direction: absent endpoint, timeout, a genuine tie, or a suggestion that fails
re-validation all degrade to the same type-it-yourself form; a non-local `comfy_url` gets no
form at all. Hostile-payload hardening requires ≥2 agreeing categories **and** that the
winning root already contains a reported category directory.

**The setup control is now shared across two surfaces** (the missing-models batch and the
bad-option surface), with exactly one `#comfy-setup` per panel. The ownership rule is
**"the batch section did not render one"** — 🔴 *not* "there are no missing models", which is
wrong in two reachable states (degenerate basenames, and a remote `comfy_url`, both of which
yield zero containers from the batch section). The slot renders as a **sibling before** the
options `<form>`, never inside it.

### The readiness line and the run-failure panel are coupled — do not edit one alone

They answer the same question on either side of the Generate click, and shipping them in
consecutive releases put **both on screen at once, 0px apart**. The rule: *a run for this
workflow exists ⇒ the readiness line yields*, in **every** terminal state — after a
**successful** run the line is not merely redundant, it is **false**, because the panel's
CTAs install things.

Enforced by one predicate read by the fragment **and** every handler writing into
`#run-status`, each answering with an out-of-band clear. 🔴 **A new writer that forgets the
OOB clear reproduces the bug.** Guarded structurally (an AST check pinning who may call the
raw fragment, against an asserted ledger) *plus* a behavioural table for the case the
structural check type-checks straight past.

### The custom-node detector is authoritative, with two staleness windows

It reads `python_module` from a cached `/object_info` (deny-list on `custom_nodes.*`), with
the old hand-written table as the cold-cache fallback — deleting that table is **unsafe**,
since `PrimitiveNode`/`Note`/`Reroute` are frontend-only and absent from every
`/object_info`. Two accepted windows: installing a custom node does not invalidate the cache
until the next run, and a **library scan deletes the cache row**, so for API-format workflows
the old table's false positives return until the next local run.

### `web_scan_timeout`: live, with an upgrade hazard

🔴 `docs/configuration.md` shipped `web_scan_timeout: "2m"` in its annotated sample for ~89
releases while nothing read the key — so hand-copying the docs was the only way to hold an
explicit value, and those users now have an enforced 2m. **A deadline firing during the HASH
phase persists ZERO rows** (hashing is phase 1, `local_files` are written in phase 3).
A release note would be kind.

### Two node-id orderings, deliberately different — do not "unify" them

The **ui** path preserves document order because it walks a `[]uiNode` **slice**. The **api**
path sorts by node id, because its graph is a JSON **object** and a map decode leaves no
document order to preserve. Both are deterministic. 🔴 **Any api-side node-id comparison must
go through `comfy.LessNodeKey` (`internal/comfy/client.go`)** — see the lesson below for why
hand-rolling it does not merely order differently, it fails to sort at all.
⚠ It was `lessNodeKey`, unexported, until PR #79; this instruction named a symbol
`internal/web` could not actually call. It is exported precisely so it can obey it —
the third open-coding (`lessNumericID`) existed because it could not.

## How to verify a release

```sh
gh release download "$(git describe --tags --abbrev=0)" -R ZacxDev/civitai-manager \
  -p '*_linux_amd64.tar.gz' -p 'checksums.txt'
sha256sum -c --ignore-missing checksums.txt      # must print OK
tar xzf ./*_linux_amd64.tar.gz && ./civitai-manager --version
gh attestation verify ./*_linux_amd64.tar.gz -R ZacxDev/civitai-manager
AUDITLOOP_CHROMIUM=/run/current-system/sw/bin/brave make ux-audit
```

🔴 **Do not restate the walk's capture count here — it has now gone stale twice.** Derive it:
`len(Views())` in `e2e/uxaudit/walk.go` × `len(Viewports)` in `capture.go`, ratcheted by
`minViews` in `walk_selectors_test.go`. Count violations from **`*.axe.json`** (**not**
`*.a11y.json`, which has no `violations` key at all), with an explicit `is None` check, and
assert **zero payloads lacking the key** — an empty violations list is falsy, and `x or y`
on it silently skips healthy captures and prints "0 captures", which reads as a regression.
**A rising violation count can BE the improvement** — always quote the page count beside it.

`gh attestation verify` is **silent on success** in some shells. Do not read a quiet `rc=0`
as proof — append one byte to a copy and confirm it gives non-zero.

## Lessons this batch paid for (the expensive ones)

🔴 **A clean `git merge` is not a clean merge, and `go build` cannot see the difference.**
Two branches added a package-level test helper with the same name and different signatures.
`go build ./...` passed (the clash is in `_test.go`), `git merge` was clean, and
`gh pr view --json mergeable,mergeStateStatus` said `MERGEABLE`/`CLEAN` — because the two
files never touch the same lines. Only **`go vet ./internal/web/`** or `go test` on the
**merged** tree surfaced it. **Put `go vet` in every merged-tree gate**, and before adding a
package-level test helper, grep for an existing one — the duplicate here re-implemented a
long-standing `a11yAttr`/`a11yHasAttr` pair whose doc comment already described the exact
use case.

🔴 **A merged-tree gate run ONCE is stale the moment a fix round lands.** The six-way
integration gate in this session was cited for hours after four fix rounds had rewritten
every branch — and the colliding file did not exist when it ran. Re-run it against the
**current heads** you are actually about to merge.

🔴 **Deletion-mutants are the easy half, and this is where nearly every surviving mutant
lived.** Across this batch the survivors were **operand-order**, **branch-order**, and
**over-determined-fixture** mutants — not deletions. Three separate PRs carried comments
saying "ORDER IS LOAD-BEARING" over code nothing pinned. Enumerate mutants from the
expression's semantic failure modes, not from "delete the thing I was already thinking of".

🔴 **A guard can assert STATE and still be vacuous, if the state is a PARALLEL
representation.** One PR replaced a prose-substring assertion (correctly — it was vacuous)
with assertions on two `data-*` attributes computed on a path *independent* of the rendered
sentence. Reinstating the original bug verbatim then left the whole suite green. **Assert
the thing the user reads, alongside the state — a shadow state cannot contradict the claim.**

🔴 **A positive control must traverse the SUBJECT's code path.** A hermeticity tripwire built
an env and never passed it to `subprocess.run`; its positive control passed because it
exec'd the stub *directly*. It validated the stub, not the tripwire, and the "zero network
calls" result was structural.

🔴 **A fixture that cannot express the defect is green forever.** The ordering fix's own
tests used an all-numeric fixture and an all-non-numeric one — both *safe* cases, each
internally a total order. The bug lived only in the **mixed** set. Proven by mutation:
restoring the broken comparator turned the new mixed-id test red while the original stayed
green.

🔴 **"One rule, one place" — check whether this package already solved it.** A comparator was
hand-rolled ~200 lines from `lessNodeKey`, which was correct, documented, and guarded by a
mutation-verified strict-weak-ordering test **naming the identical failing witness**. The
naive version (`numeric if both parse, else lexical`) is intransitive on `{"9","10","5abc"}`,
and `sort.Slice` on an intransitive comparator returns an **arbitrary permutation of its
input** — so it did not order differently, it did not sort at all.

🔴 **An asserted debt ledger's negative control WEAKENS as the ledger shrinks, and is GONE at
zero.** An empty expected set makes a silently-broken analysis indistinguishable from a clean
repo. Before resolving the last `deadcode-allow.txt` entry, build a replacement control.

⚠ **A mutation caught by the COMPILER proves nothing** — it reads as red in a filtered log
while exercising no assertion. Several attempts this batch died on unused imports or unused
variables. **Confirm `go build` still exits 0 before believing a red**, and list discarded
attempts rather than counting them.

⚠ **An empty result cannot distinguish two mechanisms.** A comparison "proving" a fabrication
was pre-existing was nearly inverted because the base run silently returned *before* the code
under test — printing nothing, which reads as "no defect" but means "never ran".
