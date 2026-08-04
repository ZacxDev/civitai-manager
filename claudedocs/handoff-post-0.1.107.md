# Handoff: post-0.1.107 — 2026-08-04

🔴 **Read `claudedocs/SESSION-HANDOFF.md` too — it is the DURABLE doc** (ranked open threads,
live diagnosis state, the lessons this batch paid for). This file is the *session-scoped*
snapshot: what just shipped, what is mid-diagnosis, what to do next.

⚠ **`scripts/resume-state.sh` prefers `handoff-*.md` over `SESSION-HANDOFF.md`**, so `/resume`
will resolve THIS file first. That is intended for the next session or two. **Delete this file
once its next-steps are done** so `/resume` falls back to the durable doc rather than pinning
a stale snapshot.

## Goal

Close out the eight-PR audit batch, release it, and leave the repo with no open PRs and no
un-actioned audit findings. Done. What remains is one measured, unfixed bug and three
follow-ups.

## State now

- **Branch:** `main` @ `d7f79c7`, clean, in sync with origin. **Zero open PRs.** One worktree
  (the base clone) — all agent/feature worktrees removed.
- **Released:** `v0.1.107`, **verified against the artifact, not the job**:
  `sha256sum -c` OK · extracted binary reports `0.1.107 (commit d7f79c7…)` · `gh attestation
  verify` rc=0 **with a negative control** (one byte appended → rc=1, so the 0 means
  something). All 6 targets + `.deb`/`.rpm` published.
- **Shipped in 0.1.107** — #68 PNG import classifies its chunk · #69 scroll regions focusable ·
  #70 deadcode ledger 15→1 · #71 Unclassified note names both reasons · #72 stable ux-audit
  baseline + Fix dialog/`<details>` axe-scanned · #73 shared setup CTA on the bad-option
  surface · #74 a11y-prefixed test helpers · #75 `ExtractResources` deterministic order.
  Every one was adversarially audited **and** delta-re-audited after its fix round.
- **devrc:** PR #326 merged (`/resume` now resolves `SESSION-HANDOFF.md`, and DRIFT no longer
  prints a false green). Follow-up tracked as
  [innovation-upstream/devrc#330](https://github.com/innovation-upstream/devrc/issues/330).
- **Dogfood on `:8972` is DOWN** — no `cm` process, nothing listening, `curl` → 000. The old
  `0.1.106` binary is still on disk under another session's scratchpad
  (`…/766dd9a1-…/scratchpad/dogfood/cm`) but is not running. **Nothing to kill** — the earlier
  "don't touch another session's process" concern is dissolved.

## Open investigations — live diagnosis state

### ✅ CLOSED (PR #79) — `lessNumericID` was a THIRD live open-coding of the intransitive comparator

🔴 **Fixed. Do not re-derive this.** `lessNumericID` is **deleted**; `internal/comfy`'s
`lessNodeKey` is now **exported as `comfy.LessNodeKey`** and `structuredAPINodes` calls it —
one rule, one place, no fourth copy. Guarded by
`TestStructuredAPINodesOrderIsDeterministicForMixedIDs` (500 calls, mixed fixture, all-numeric
control, positive control on the counter itself) and
`TestStructuredAPINodesTruncatesTheDeterministicPrefix` (the truncation half). Both
mutation-verified with `go build` confirmed green for every mutant.

⚠ **The "5 distinct orders" below is this doc's number for the #75 auditor's own fixture.** The
committed guard measures **3** on ITS fixture, stable across 8 runs — the count is a property
of the id set, not run-to-run variance. Quote whichever fixture you actually ran.

The diagnosis below is kept as the worked record; the two stale claims in it — that
`lessNodeKey` is unexported, and that the `AllImages` doc comment is uncorrected (item 5 in
Next steps) — were both resolved by #79.

- **Symptom + exact repro (as diagnosed):** `internal/web/workflow_graph.go:1061` defined `lessNumericID`,
  byte-for-byte the naive comparator (`numeric if both parse, else lexical`). It is fed a
  randomised map range at `internal/web/workflow_graph.go:878` inside `structuredAPINodes`.
  Reproduce by calling `structuredAPINodes` repeatedly on an api graph with **mixed** node ids
  (e.g. `{"1","4","9","12","12:3","12:8"}`) and hashing the rendered node order.
- **Observed (with values):** **5 distinct rendered node orders across 500 calls** on a mixed-id
  graph; **all-numeric control = 1**. (Measured by the PR #75 auditor with a throwaway probe.)
  The mechanism is settled: `sort.Slice` on an intransitive comparator returns an **arbitrary
  permutation of its input**, and the input is a randomised map range — so the sort does not
  order differently, it does not sort at all. Witness: `{"9","10","5abc"}` → `9 < 10` numeric,
  `"10" < "5abc"` lexical, `"5abc" < "9"` lexical = a cycle.
- **Ruled out:** *"it's only cosmetic"* — no. Beyond render order it randomises **which** nodes
  survive the `gMaxNodes = 600` truncation, so a large graph shows a different subset per
  process. *"mixed ids are theoretical"* — no: `internal/comfy/convert_subgraph.go` mints
  interior ids as `"<instance>:<interior>"`, and `internal/comfy/convert_test.go:632` pins a
  converted api graph keyed `{"4","17","100:1"}`.
- **Leading hypothesis (CONFIRMED, and what #79 did):** identical defect and identical fix to
  #75 — route the comparison through the canonical comparator. It was unexported at the time;
  the decision taken was to **export it** (`comfy.LessNodeKey`) rather than add a wrapper,
  since `internal/web` already imports `internal/comfy` in 83 files and the dependency
  direction is comfy ← web.
- **Blast radius today:** reachable only on **api-format** workflows, and the operator's DB is
  `ui|71` — **zero** api-format rows. So it is latent for this user and live for anyone who
  imports an api graph.
- ~~**Next probe (verbatim)**~~ — **spent; both greps now return zero hits.** Kept only to show
  what the probe was:
  ```sh
  git grep -n 'lessNumericID\|func lessNodeKey' -- internal/
  # then reproduce, in a scratch _test.go inside internal/web:
  #   build a mixed-id api graph, call structuredAPINodes 500x, count distinct orders,
  #   with an all-numeric control that must report 1.
  ```

### Two pre-existing `-race` flakes in `internal/web`, unowned

- **Symptom + exact repro:** `go test -race ./internal/web/` intermittently fails
  `TestScanStatusRaceSafe` and `TestInstallMissingAndRunMixedAlreadyPresentSaysSo`.
- **Observed (with values):** `TestScanStatusRaceSafe` — **3/20** isolated failures at base
  `52cb872` and **3/20** on a branch touching no scan file, both under heavy concurrent load;
  **20/20 passing at idle** on the merged tree. `TestInstallMissingAndRunMixedAlreadyPresentSaysSo`
  — **2/10** under load. Neither reproduced in any idle run this session.
- **Ruled out:** attribution to any PR in this batch — measured with base controls on both
  sides; neither test's file was touched. Not a real data race either: `-race` reported **0
  DATA RACE** blocks in every run, the failures are assertion failures.
- **Leading hypothesis (for the first, diagnosed):** four goroutines poll until `<-done`, then
  assert `polls != 0`, with **nothing guaranteeing a poll completes first** — so the
  non-vacuity check is itself the racy part. Under CPU starvation the settle never happens.
- **Next probe (verbatim):**
  ```sh
  cd /home/zach/workspace/civit/civitai-manager
  for i in $(seq 1 20); do go test -race ./internal/web/ -run '^TestScanStatusRaceSafe$' -count=1 >/dev/null 2>&1 || echo FAIL; done | wc -l
  # then read the test and make the poll-completion deterministic (token/barrier), not timed.
  ```
- 🔴 **Consequence for every gate:** a single green `-race` on this package means *"passed once
  on a quiet host"*, not *"race-free"*. Say it that way in PR bodies.

### The model cache is never populated by a library scan

- **Symptom:** the facet bar can say a workflow is "linked but nothing we have locally matches
  a known use case" when the app has **never fetched that model's tags**.
- **Observed:** `model_cache` has exactly **one** writer — `cachedModelDetail` — reached only
  from model-detail/card renders and the Discover-workflows import. `internal/library/matcher.go:103`
  auto-links during a scan and does **not** trigger it.
- **Status:** the copy is hedged (shipped in #71) so it is no longer false. The underlying gap
  is real but not user-visible as a wrong statement.
- **Next probe:** decide whether scan-link should warm the cache; if yes the write belongs
  beside the auto-link, not in the render path.

## Next steps (ranked)

1. ~~**Fix `lessNumericID`**~~ — ✅ **DONE, PR #79.** See the CLOSED section above. The fixture
   lesson survives and generalises: an all-one-kind id set is internally a total order and
   **cannot** observe an intransitive comparator, so a mixed fixture plus a same-kind control
   is the only shape that measures anything.
2. **Restart the dogfood from v0.1.107** — now trivial, nothing to kill:
   build → copy to a scratchpad dir → `serve` on `:8972` against the real DB → verify by pid +
   `/proc/<pid>/exe` + `--version`. (A free port is not a released binary; wait on the
   *process*, not the port.)
3. **Add a `web_scan_timeout` release note.** `docs/configuration.md` shipped
   `web_scan_timeout: "2m"` in its annotated sample for ~89 releases while nothing read the key.
   Anyone who hand-copied the docs now has an enforced 2m deadline, and **a deadline firing
   during the hash phase persists ZERO rows** (hashing is phase 1; `local_files` are written in
   phase 3). This is the one upgrade hazard in 0.1.107.
4. **Own the two `-race` flakes** — fix the timing dependency rather than re-running until green.
5. **One of the two stale comments the #75 auditor found is still open.** ✅ The `AllImages`
   doc comment (`internal/comfy/client.go`) was corrected in #79 — it described the ordering by
   the old intransitive rule ~20 lines above the corrected doc. ⬜ Still open:
   `internal/comfy/missing.go:148` uses plain `sort.Strings` on node ids. It is a **total
   order**, so deterministic and not a bug — but it is a *different rule*, emitting
   `"1","10","2"` where the api listing emits `"1","2","10"`. Two surfaces of one graph
   disagree on node order; decide whether that is worth unifying.
6. **devrc#330** — three sibling paths in `resume-state.sh` still print the clean DRIFT line
   having reconciled nothing.

## Gotchas / decisions / dead-ends

- 🔴 **A clean `git merge` is not a clean merge.** Two test files declared the same
  package-level helper (`attr`); `go build ./...` passed (clash is in `_test.go`), `git merge`
  was clean, `gh` said `MERGEABLE`/`CLEAN`, and the **merged tree would not compile**. Only
  `go vet ./internal/web/` on the merged tree caught it. **Put `go vet` in every merged-tree
  gate**, and grep for an existing helper before adding a package-level one.
- 🔴 **A merged-tree gate is stale the moment a fix round lands.** Ours was cited for hours
  after four fix rounds had rewritten every branch — and the colliding file did not exist when
  it ran. Re-run against the heads you are actually merging.
- 🔴 **Do not delete a worktree by diffing its tree against `main`** — a branch merely *behind*
  main always differs, so that check refuses every real candidate. Use
  `gh pr view <n> --json state` = `MERGED` plus a clean `git status --porcelain`.
- 🔴 **Commit before you mutate.** `git checkout -- <file>` to revert a mutation reverted an
  uncommitted fix in the same file this session; the commit message then claimed a fix that
  was not in the commit. Caught on `git status`, amended.
- **A mutation caught by the compiler proves nothing** — several attempts died on unused
  imports/variables and read as "red" while exercising no assertion. Confirm `go build` still
  exits 0 before believing a red, and list discarded attempts rather than counting them.
- **`/audit-pr` scaled by blast radius was right, but my triage was not.** #71 was classified
  "light band, pure copy" and nearly merged unreviewed; its audit found a guard that could not
  see the bug it existed to prevent. When in doubt, audit.
- **Decision:** `ExtractResources` **sorts** rather than recovering document order. Recovering
  it means streaming with `json.Decoder`; no consumer wants "the order the exporter happened to
  write" over a stable one. Documented in the function.
- **Decision:** the deadcode ledger's last entry (`queue.Worker.ProcessOne`) **stays**.
  🔴 The gate's negative control weakens as the ledger shrinks and is **gone at zero** — an
  empty expected set makes a broken analysis indistinguishable from a clean repo. Build a
  replacement control before resolving it.

## How to verify

```sh
cd /home/zach/workspace/civit/civitai-manager
# state
git status -sb && gh pr list && git describe --tags --abbrev=0 origin/main

# the full gate (what "green" means here)
go build ./... && go vet ./... && gofmt -l ./internal/ ./e2e/ ./*.go
go test ./... 2>&1 | tee /tmp/t.log; grep -c -- '--- FAIL' /tmp/t.log   # count, never read the exit code
go test -race ./internal/{library,store,queue,poller,web}/...           # see the flake caveat above
(cd e2e/uxaudit && GOTOOLCHAIN=go1.26.0 go build ./... && GOTOOLCHAIN=go1.26.0 go vet ./... && GOTOOLCHAIN=go1.26.0 go test ./...)
GOTOOLCHAIN=go1.26.0 .github/deadcode.sh

# the release actually shipped what it claims
gh release download v0.1.107 -R ZacxDev/civitai-manager -p '*_linux_amd64.tar.gz' -p 'checksums.txt' -D /tmp/rv
(cd /tmp/rv && sha256sum -c --ignore-missing checksums.txt && tar xzf ./*_linux_amd64.tar.gz && ./civitai-manager --version)
gh attestation verify /tmp/rv/*_linux_amd64.tar.gz -R ZacxDev/civitai-manager   # then TAMPER a copy and confirm rc!=0
```
