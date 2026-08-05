# Handoff: post-0.1.108 — 2026-08-05

🔴 **Read `claudedocs/SESSION-HANDOFF.md` too — it is the DURABLE doc.** This file is the
*session-scoped* snapshot: what shipped, what is measured-and-open, what to do next.

⚠ **`scripts/resume-state.sh` prefers `handoff-*.md` over `SESSION-HANDOFF.md`**, so `/resume`
resolves THIS file first. **Delete it once its next-steps are done**, and delete
`handoff-post-0.1.107.md` — its work is finished (see below), it is now pure noise, and two
competing handoffs is worse than none.

## State now

- **`main` @ `dccb9ad`**, clean, in sync. **Zero open PRs.** One worktree (the base clone).
- **Released: `v0.1.108`**, verified **against the artifact, not the job**: `sha256sum -c` OK ·
  extracted binary reports `0.1.108 (commit d9ee4fc…)` · `gh attestation verify` rc=0 **with a
  negative control** (one byte appended → rc=1, 404 for the new digest). All 11 assets published.
  ⚠ `gh attestation verify` prints **nothing** on success in this version — the exit code
  unpiped plus the tampered-copy control is the whole evidence. Do not read the silence as failure.
- **Shipped in 0.1.108:** #79 api-node ordering · #80/#81 the dry-run probe + census · #82 the
  separator consolidation. **Merged AFTER the tag** (so they are 0.1.109 material): #84
  installed-aware nodepack panel · #85 note-URL resolution.
- **24 orphan `worktree-agent-*` local branches**; all content is on `main` (the one "17 ahead"
  is #85's, squash-merged). Safe to prune, not pruned.

## What the operator's ComfyUI looks like now

- **ComfyUI 0.30.2**, ComfyUI-Manager **V3.41**, root `/home/zach/workspace/fast/comfyui/ComfyUI`.
  ⚠ CLAUDE.md still says v0.27 in the live-verify-tooling bullet — stale.
- **`ComfyUI_UltimateSDUpscale` was installed this session** (via Manager, authorized) and the
  operator restarted: `/object_info` went **2496 → 2499** types.
- **`4xNomosWebPhoto_RealPLKSR.pth` installed** into `models/upscale_models/`, sha256
  `a9db66c9…` matching OpenModelDB's published hash.
- 🔴 **ComfyUI went DOWN at least twice unprompted**, once mid-agent-run with no traceback and
  no shutdown line in its log. Nobody diagnosed it. It was down at end of session. **If a probe
  returns `000`, suspect this before suspecting the app.**

## Workflow 590 — 4 blockers → 2

`Moody Zimage Simple Workflow - V7.5`, ui-format, 96 nodes. Measured with the live post-restart
schema:

```
before: nodes=60 conv.warnings=1 cut=1 missing_nodes=1 missing_models=3  ConvWarned+GraphIncomplete
after:  nodes=61 conv.warnings=0 cut=0 missing_nodes=0 missing_models=3  ReportOK only
```

**The residual unknown is CLOSED**: node 663's 20 widget values had never met the real schema, so
installing the pack was exactly when new `BadOptions` could appear. Measured `bad_options=0`.

**Remaining, all model files:**

| file | wanted by | status |
|---|---|---|
| `1xSkinContrast-High-SuperUltraCompact.pth` | `UpscaleModelLoader` node 702 | OpenModelDB, **mediafire interstitial** — needs a browser, or substitute one of the 4 upscalers already installed |
| `ComfyUI\moody-porn-v12.6_00001_.safetensors` | `UNETLoader` node 761 | author's own local merge; model 620406 publishes 38 files, **none with this name**. Substitute `z_image_turbo_nvfp4` or `zImageTurbo_turbo` |

🔴 **The readiness line will still say "needs 1 node type" until a real run happens** — measured.
The run path re-fetches `/object_info` live; the readiness line reads only `comfy_model_cache`,
and the operator's row is still **2026-08-03**. That is the gap #84's follow-up (confirm-restart
→ auto-refresh) closes.

⚠ **Three DORMANT resources sit on bypassed nodes and are NOT validated by preflight** —
`ema_vae_fp16.safetensors` (SeedVR2 VAE), `seedvr2_ema_7b-Q4_K_M.gguf`, `zimage\zit_sda_v1.safetensors`.
The library has **none** of them. They are not "satisfied", they are *unchecked*; enabling those
branches makes them blockers. (Also: `SeedVR2LoadVAEModel`/`SeedVR2LoadDiTModel` are absent from
`/object_info`, so those branches need a node pack too.)

## The new instrument — use it before diagnosing anything about a workflow

`internal/web/run_dryrun_probe_test.go` — a dry run of the local run path against a **copy** of a
real DB, stopping immediately before submission. It refuses to open the canonical DB path.

```sh
cp ~/.config/civitai-manager/civitai-manager.db /tmp/probe.db
CM_PROBE_DB=/tmp/probe.db CM_PROBE_WORKFLOW=590 CM_PROBE_COMFY=http://127.0.0.1:8188 \
  go test ./internal/web/ -run TestDryRunProbe -v
```

It drives the **production** functions (`readinessSchema`, `ConvertUIToAPIResult`, `Preflight`,
`localHaveFile`, `evalRunGate`), reports every blocker **by name**, names which loader wants each
missing file, cross-checks the run path against the readiness line, and accounts for every node
(census) plus dormant resources.

🔴 **It is a LOWER BOUND on brokenness, never a proof of success.** ComfyUI's own `/prompt`
validation is stricter and runs after; nothing here sees VRAM, a throwing custom node, or a bad
weight. **Its positive control is workflow 587** (`Basic_V37`, 7 real generations) — without it,
"590 is refused" is indistinguishable from a probe that refuses everything.

## Open investigations — live diagnosis state

### The deadcode gate has a tier-B blind spot: "reachable only through reflection"

- **Symptom:** `comfy.Client.ManagerAlive` has **no non-test caller** (grep-verified: definition
  + comments + 8 test refs), yet the gate does not flag it. Tier B lists only
  `queue.Worker.ProcessOne`.
- **Measured:** `deadcode -whylive 'github.com/ZacxDev/civitai-manager/internal/comfy.Client.ManagerAlive'`
  → **`is reachable only through reflection`**. Note the naming convention: `Client.ManagerAlive`,
  **not** `(*Client).ManagerAlive` — the latter returns "not found in program" and reads as a
  missing symbol.
- **Why it matters:** CLAUDE.md documents the reflection escape hatch for **tier A** only, while
  calling tier B "the one that matters". This is a hole in the tier that matters.
- **Next probe:** decide whether the gate can exclude reflection-only reachability in tier B, or
  whether the ledger should carry known-uncalled-but-reflection-reachable entries.
- **Bonus:** #84's follow-up (poll `ManagerAlive` after a restart) would turn this specific
  function from debt into a feature.

### `internal/library` has no `PathBase` ledger — and that is exactly where the miss was

#82's structural ledger covers `internal/comfy` only. The live bug the audit found
(`workflow_scan.go:253`, a graph-derived ref taking `filepath.Base`) is in `internal/library`,
and **S8's mutation reds its three behavioural tests but NOT the ledger** — verified, and that is
the observable proof of the scope hole. A second ledger there is the obvious follow-up.

### The loopback-gate case table covers 5 of ~50 handlers

Measured during #85's audit: **50 `s.gate(w)` call sites across 23 files**;
`nonloopback_gate_web_test.go` enumerates **5**, none on the run surface. Deleting a gate outright
went undetected until #85 added a local test. **Pre-existing, not #85's debt.** A structural AST
ledger (handlers calling `s.gate`, cross-checked against registered routes) would close it —
same shape as `runStatusFragmentCallers`.

### Two unowned `-race` flakes in `internal/web` (carried forward, still unowned)

`TestScanStatusRaceSafe` (3/20 under load) and `TestInstallMissingAndRunMixedAlreadyPresentSaysSo`
(2/10). Not real data races — `-race` reports 0 DATA RACE blocks; they are assertion failures
under CPU starvation. **Every `-race` green this session was "passed once on a quiet host"** and
was reported that way. Fix the timing dependency rather than re-running until green.

### The model cache is never populated by a library scan (carried forward)

Unchanged from the 0.1.107 handoff. `model_cache` has one writer, `cachedModelDetail`, not reached
from `matcher.go`'s auto-link.

## Next steps (ranked)

1. **Get 590 running** — one browser download (mediafire) or an upscaler substitution, plus the
   UNET substitution. Everything else is done.
2. **Confirm-restart → auto-refresh the schema** (#84 follow-up). Poll `ManagerAlive` after
   `ManagerReboot`, then refetch `/object_info` and `cacheComfyObjectInfo`. Closes the stale
   readiness line the operator is sitting in *right now*, and retires the reflection-hidden
   function above.
3. **A manual "Refresh node schema" control** — one CSRF-protected, loopback-gated POST. Migration
   0019's header still names a trigger the code does not implement.
4. **HF full-text as a second discovery tier** + `UpscaleModelLoader.model_name → "Upscaler"` in
   `InferCivitaiType` (today it returns `""`, so the CivitAI branch of `resolveInstallPlan` is
   never entered for either upscale model, and the `"Upscaler"` whitelist entry is unreachable).
   Measured: full-text + exact-basename tree confirmation resolves **2 of 6** target files where
   the current repo-name search resolves 0.
5. **OpenModelDB resolver** — 671 models, **753/753 resources carry a sha256**. Needs a THIRD
   hardened client (`modelDownloader` is a two-way switch), so a genuinely new egress surface.
   ~30% auto-fetchable (github), 100% link-with-hash.
6. **`internal/library` ledger** and **the loopback-gate ledger** (above).
7. **Own the two `-race` flakes.**

## Process lessons this session paid for

- 🔴 **Every audit round found something, and the fix round kept creating the next finding.**
  #79 took **3 rounds**; #82 took **2**; #84 and #85 took **2 each**. In #79, rounds 2 and 3 each
  caught a **false claim written by the coordinator** — including one wrong in the direction that
  would train a maintainer to dismiss a **true red as a flake**. Budget for ≥2 rounds always.
- 🔴 **"Calibrated to its own constant" recurred THREE times in one PR (#85).** The author caught
  it for `noteMaxLinks` and did not carry the fix to the two neighbouring constants. The tell for
  the worst one: raising the cap to **1 GiB** left the test GREEN while its runtime went
  **0.00 s → 188 s**. Nobody reads runtime. **When you fix a bound-pinning defect, sweep every
  sibling bound in the same file.**
- 🔴 **A comment is a claim, and the two budget halves were documented BACKWARDS.** Measured:
  a RAISED budget is caught by the past-the-budget half, a LOWERED one by the just-inside half.
  Both comment blocks asserted the opposite. Fixed in `notes_test.go`.
- 🔴 **`rc=$?` after a pipe reports the LAST command's status, not the one you care about.**
  Cost a bogus "attestation verified" reading until re-run unpiped. Same family as the
  count-don't-read-exit-codes rule.
- 🔴 **A `perl`/`sed` mutation that matches nothing is indistinguishable from a passing test.**
  Hit twice: `256 << 10` vs the real `256 * 1024`, and a `(filepath.Base)` paren form. **Always
  `git diff --numstat` and confirm the mutation LANDED before reading the verdict.**
- **A mutation caught by the COMPILER proves nothing** — hit by the coordinator and by two agents.
  Confirm `go build ./...` exits 0 for every mutant.
- **`pgrep -f 'python.*main.py'` matched the shell running it**, reporting a ComfyUI process that
  did not exist. Use `pgrep -x`, or read `/proc/<pid>/exe`.
- **Backticks in `gh pr comment --body "…"` are shell-executed** — ate a symbol name from a
  posted comment. Use `--body-file`. (CLAUDE.md documents this for `git commit -m`; it is the
  same hazard on any quoted CLI argument.)
- **Stale `<new-diagnostics>` from agent worktrees are almost always false.** An `undefined: sync`
  report was a mid-edit snapshot; `go build`/`go vet` were clean. Re-run the real build as arbiter.
- **A squash-merged branch shows as "N commits ahead" forever** — that is not unmerged work.
  Check the content, not the commit count, before deciding a branch is safe to delete.

## How to verify

```sh
cd /home/zach/workspace/civit/civitai-manager
git status -sb && gh pr list && git describe --tags --abbrev=0 origin/main

go build ./... && go vet ./... && gofmt -l ./internal/ ./e2e/ ./*.go     # gofmt must print NOTHING
go test ./... 2>&1 | tee /tmp/t.log; grep -c -- '--- FAIL' /tmp/t.log    # COUNT, never an exit code
grep -c 'panic: test timed out' /tmp/t.log
go test -race ./internal/{library,store,queue,poller,web}/...            # see the flake caveat
(cd e2e/uxaudit && GOTOOLCHAIN=go1.26.0 go build ./... && GOTOOLCHAIN=go1.26.0 go vet ./... && GOTOOLCHAIN=go1.26.0 go test ./...)
GOTOOLCHAIN=go1.26.0 .github/deadcode.sh    # 🔴 REQUIRES that GOTOOLCHAIN or it refuses
```
