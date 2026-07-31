# Disk management — design proposal (v0 draft)

_Status (2026-07-31): **NOT BUILT.** This document covers the half of the `/disks`
work that was deliberately deferred: **bulk relocation of model files across
disks**, and a **default download location + naming pattern**. What DID ship on
the same branch is read-only: `/disks` renders per-filesystem capacity for the
configured directories and absorbs the quarantine table that used to live at
`/trash`. Nothing in this document is implemented; every code reference below is
to something that exists TODAY and would be touched, not to something new._

**Recommendation up front:** build the **download-location + naming half first**,
and build the relocation half as a **quarantine-shaped, manifest-first, one-batch-
at-a-time mover with a hard "no two-writer" interlock** — not as a general "move
these 400 files" job. Reasons throughout.

---

## 0. Why this is the highest-blast-radius operation in the codebase

Everything else the app does to a user's disk is either additive (download a new
file), reversible by construction (quarantine — a move plus a manifest), or
read-only (scan, hash, discover). Bulk relocation is the first operation that
**moves files the user already depends on, in bulk, across filesystems, while
three other subsystems believe they know where those files are**:

- `internal/store`'s `local_files` rows key on path (and the hash cache keys on
  `(path, size, mtime)` — an invariant CLAUDE.md calls out explicitly).
- `internal/library/scanner.go` may be mid-walk over the source tree.
- `internal/queue` + `internal/poller` may be mid-download INTO the destination
  tree.
- ComfyUI itself may have the file open (a model being loaded), and — on the
  local-run path — `internal/comfy` resolves models by path under
  `comfy_model_path`.

A partially-completed bulk move is therefore not "some files in the wrong place".
It is a library whose index disagrees with the disk, in a way the user cannot see
and the next scan will "fix" by re-hashing everything.

---

## 1. Half A — default download location + naming pattern

This is the smaller, lower-risk half and it should ship first, because **it
reduces the need for the relocation half**: most relocation demand comes from
"everything landed on the small SSD because that is where `model_root` points".

### 1.1 What exists today

`internal/civitai`'s `DestPath(modelRoot, modelType, creator, modelName,
versionName, fileName)` is the single function that decides where a downloaded
file lands, and `TypeSubdir` maps a CivitAI model type onto its folder
(`loras/`, `checkpoints/`, …). `handlers.go`'s `handleModelDownload` calls it;
so does the workflow "Download & run" path. There is exactly one root
(`model_root`) and the layout is fixed.

### 1.2 Proposal

**Two config keys, both optional, both with today's behaviour as the default:**

```yaml
# Multiple roots, tried in order. The FIRST one with enough free space wins.
download_roots:
  - path: /mnt/fast/models
    reserve: 50GB      # never fill below this
  - path: /mnt/bulk/models

# Layout template. Default reproduces DestPath exactly.
download_layout: "{type}/{creator}/{model}/{version}/{file}"
```

**Placement rule — deliberately dumb.** Walk `download_roots` in order; pick the
first whose `diskusage.Stat` reports `Free - reserve >= fileSize * 1.05`. If none
qualifies, **fail the download with a named error** rather than falling back to
the fullest disk. Do NOT implement "balance across disks", "prefer the disk that
already has this model's other files", or any other scoring — those are
unpredictable to the user and each one is a support burden. Ordered-with-reserve
is explainable in one sentence.

**Naming template — a closed vocabulary, validated at config load.** Tokens:
`{type} {creator} {model} {version} {file} {baseModel} {modelId} {versionId}`.
Anything else is a config error at startup, not a silent literal. Three rules
that are not optional:

1. **Every token is sanitised the way `DestPath` already sanitises** (path
   separators, `..`, control chars, reserved Windows names, length caps). A model
   name is untrusted remote input; a template that interpolates it raw is a path
   traversal.
2. **`{file}` must be present**, and the result must still end in the remote
   basename. Losing the filename breaks the workflow "install-and-run" matcher,
   which compares an expected basename against what a model actually ships — see
   the "Install-and-run must NEVER substitute a file silently" invariant.
3. **The rendered path is containment-checked against its root** after
   rendering, not before. Template validation is not a substitute.

**Migration:** none. Changing the template must NOT move existing files — that
is Half B, and conflating them is how a config edit becomes a 400 GB move. The
template applies to NEW downloads only, and the UI must say so.

### 1.3 Test shape

- Table over templates × hostile model names (`../../etc`, `CON`, a 300-char
  name, a name with a newline) asserting containment and sanitisation.
- Placement: fake `diskusage.Stat` results (a seam this needs that
  `internal/diskusage` does not currently expose — add one) covering "first root
  fits", "first root is below reserve", "none fits → named error".
- A guard that the DEFAULT template renders byte-identically to today's
  `DestPath` for a set of real fixtures, so shipping this changes nothing for a
  user who does not opt in.

---

## 2. Half B — bulk relocation

### 2.1 The shape to copy: quarantine

`internal/library/quarantine.go` already solves 80% of this problem and is the
only prior art in the repo for "move a set of files and be able to undo it". It:

- creates a **batch header row first** (`store.CreateQuarantineBatch`),
- records **each file before moving it** (`store.AddQuarantinedFile`, holding
  `OriginalPath`, `TrashPath`, `SHA256`, `SizeBytes`),
- writes a **manifest** afterwards,
- and exposes a **`Restore`** that walks the rows back.

Relocation should be the SAME object with a different destination, not a new
subsystem. Concretely: a `relocation_batches` / `relocated_files` pair mirroring
the quarantine tables, and a `Relocate(ctx, ids, destRoot, apply bool)` that
mirrors `Quarantine`'s dry-run-then-confirm signature.

**Why not literally reuse the quarantine tables:** restore semantics differ. A
quarantine restore returns a file to where it was because the quarantine was a
soft delete; a relocation "undo" is itself a second full move with its own
failure modes, and the two must not share an undo queue. Separate tables,
identical shape.

### 2.2 The five hazards, and what to do about each

#### (a) Cross-filesystem rename is not atomic — and that is the whole point

`os.Rename` across filesystems fails with `EXDEV`. Since the entire feature is
"move to ANOTHER disk", **the rename path is the exception and the
copy-verify-delete path is the norm.** Do not write `os.Rename` with a fallback;
write copy-verify-delete and use `os.Rename` only as a same-device fast path
detected up front.

The sequence per file, and every step is load-bearing:

1. `stat` source; record `size`, `mtime`, and the **already-known SHA256** from
   `local_files` (never re-hash here — the hash cache exists precisely so this is
   free).
2. Copy to `dest.partial` **on the destination filesystem**.
3. `fsync` the file, then `fsync` the destination DIRECTORY. Without the
   directory fsync the rename in step 4 can be lost on a crash while the data
   survives — a file that exists nowhere.
4. Rename `dest.partial` → `dest` (same filesystem, atomic).
5. **Re-hash the destination and compare to the recorded SHA256.** This is the
   only thing that distinguishes "moved" from "silently truncated by a full
   disk". A mismatch means: delete `dest`, leave the source untouched, mark the
   row failed, stop the batch.
6. Only now delete the source.
7. Update the `local_files` row's path **in the same transaction** as marking the
   relocation row done.

A `.partial` suffix on the destination is what makes an interrupted copy
identifiable as garbage rather than as a real model file the next scan will
index.

#### (b) Partial failure and resume

The batch is a list of per-file rows with an explicit state
(`pending | copied | verified | source_deleted | done | failed`). That state
machine IS the resume protocol:

- **`copied` on restart** → the destination may be partial; delete it, re-run the
  file from `pending`.
- **`verified` on restart** → the destination is good and the source still
  exists; skip to step 6.
- **`source_deleted` on restart** → both sides are consistent; skip to step 7.
- **`failed`** → never retried automatically. A failed file is a question for the
  user, not a retry loop.

**Stop on first failure. Do not continue the batch.** A relocation that lands 200
of 400 files and reports "200 succeeded, 200 failed" leaves the user with a split
library and no obvious next action. Stopping leaves a prefix that is fully
consistent and a source tree that is fully intact from the failure point on.

**A batch must be resumable across a process restart**, because the operation is
long enough that the user will close the app. That means the state lives in
SQLite, not in a job struct — this is the key structural difference from the
existing scan/discover streaming jobs, whose state is in memory and dies with the
process. Reusing the streaming-job pattern here would be the single biggest
design mistake available.

#### (c) Collision with an in-flight scan or download

There is currently no global "the library is being mutated" interlock; scan,
discover, quarantine and the download worker all assume nobody else is moving
files. Relocation breaks that assumption hard.

**Proposal: one advisory lock row in SQLite, `library_mutation_lock`, holding an
owner string and a heartbeat timestamp.** Relocation takes it exclusively.
Scan/discover/quarantine/the download worker take it in a shared mode, or at
minimum check it and refuse to start with a clear message. The heartbeat is what
makes a crashed holder recoverable without a manual unlock.

This is deliberately a coarse, whole-library lock rather than per-path. Per-path
locking is more permissive and much easier to get subtly wrong, and the operation
is rare enough that "you cannot scan while a relocation is running" is an
acceptable UX.

**The download worker is the awkward one** — it is a background loop, not a
user-initiated action, and blocking it for the duration of a 400 GB move is not
obviously right. The narrower rule: the worker may keep downloading, but
relocation must **exclude any path that is the destination of a queued or active
queue item**, computed at batch-plan time and **re-checked immediately before
each file**. The re-check is not paranoia: the plan/confirm gap is
user-controlled and unbounded.

#### (d) Symlinks

A model tree assembled by hand — which is the norm for anyone with two disks — is
full of symlinks, and this is where a naive mover destroys data.

- **Never follow a symlink when moving.** `os.Lstat`, not `os.Stat`. Copying
  through a symlink turns a 2 KB link into a 6 GB duplicate and then deletes the
  link, silently doubling disk usage — the opposite of what the user asked for.
- A symlink whose target is INSIDE the moving set becomes dangling. Detect this
  at plan time and either rewrite it or refuse.
- A symlink whose target is OUTSIDE the set is fine to move as a link, but its
  relative target must be rewritten if it was relative.
- **The v0 answer should be: refuse.** If the selected set contains any symlink,
  report it and move nothing. Rewriting link topology correctly is a project of
  its own and is not what "free up space on my SSD" needs.
- Hardlinks have the same shape (moving one of N links to an inode across
  filesystems silently un-shares the storage). Detect `nlink > 1` and refuse in
  v0 as well. Both checks are unix-only; on Windows, skip them and say so.

#### (e) Interaction with the quarantine manifest and undo model

A quarantine batch's rows hold `OriginalPath` — the place `Restore` will put the
file back. **If a relocation moves a file that a quarantine batch remembers, the
restore target becomes stale**, and `Restore` will either fail or (worse)
recreate the file at a path the library no longer uses, producing a duplicate the
next scan flags as a deletion candidate.

Three options, in increasing order of effort:

1. **Refuse to relocate anything that appears in a non-restored quarantine
   batch.** Trivially correct, and the sets barely overlap in practice
   (quarantined files live in the trash dir).
2. Rewrite the affected `quarantined_files.OriginalPath` rows inside the same
   transaction as the relocation row. Correct, but silently rewrites the meaning
   of an undo the user set up earlier.
3. Version the manifest and make `Restore` fall back to a hash lookup.

**Recommend (1) for v0**, and note it in the UI. The trash dir is itself a
plausible relocation target ("move my trash to the big disk"), and that specific
case should be a separate, explicit action that also updates
`quarantine_batches.TrashDir` — not a general file move.

### 2.3 UI shape

Mirror quarantine exactly, because the user has already learned it:

- Selection happens on the **Library → Model files** tab (the surface that
  already has per-file checkboxes and a quarantine action), not on `/disks`.
- `/disks` gets a **"Move files here"** affordance per row that deep-links into
  that selection scoped to the destination. `/disks` shows capacity and batch
  history; it does not become a file browser.
- **Dry run first, always.** The plan names the destination root, the total bytes,
  the free space before and after, the count of files, and every refusal
  (symlink, hardlink, quarantined, queued). Only a second, explicit confirm
  starts it — the same offer-don't-perform discipline the install-and-run
  substitution flow uses.
- Progress is a poll endpoint over the SQLite batch state (not an in-memory job),
  so closing the tab, or the whole app, loses nothing.
- The batch is **stoppable**, and stop means "finish the current file, then
  stop" — never "abort mid-copy", which is how a `.partial` becomes orphaned.

### 2.4 What NOT to build

- **No "auto-balance my disks".** Unpredictable, and a background process that
  moves a user's files without being asked is a support nightmare.
- **No move-on-config-change.** Editing `download_layout` must never trigger a
  relocation.
- **No cross-machine / network destinations.** `diskusage.Stat` answers for a
  local mount; an NFS/SMB target changes the failure modes (silent write errors,
  no meaningful fsync guarantees) enough to be a separate project.
- **No parallel copies.** One file at a time. Parallelism buys little on
  spinning media, complicates the state machine, and makes the failure story much
  worse.

---

## 3. Suggested sequencing

| Slice | Content | Risk |
|---|---|---|
| **D1** | `download_roots` (ordered + reserve) — placement only, default layout unchanged | Low |
| **D2** | `download_layout` template with the closed token vocabulary + containment tests | Low |
| **D3** | `library_mutation_lock` + making scan/discover/quarantine/worker respect it. **Ships alone, before any mover exists.** | Medium |
| **D4** | Relocation tables + dry-run planner + all the refusals (symlink, hardlink, quarantined, queued). **Plans only — no mover.** | Low |
| **D5** | The mover: copy-verify-delete, the state machine, resume | High |
| **D6** | UI: `/disks` affordance, progress, stop, batch history | Medium |

D3 and D4 shipping before D5 is the point of the ordering: the interlock and the
refusals are exactly the parts that are cheap to build, cheap to test, and
catastrophic to retrofit. A D5 built first would be a mover with no interlock,
which is the version that eats someone's library.

## 4. Open questions

- Should `download_roots` supersede `model_root`, or layer on top of it? Layering
  keeps every existing config working but means two concepts for one thing.
- Does the relocation destination reuse `download_layout`, or preserve the
  source's relative path under the new root? Preserving is less surprising;
  reusing is what someone who just changed their template will expect. They
  conflict, and the answer should be an explicit choice in the dry-run UI rather
  than a silent default.
- Windows: `nlink`/symlink detection differs and the refusals above are unix
  shaped. The honest v0 answer may be to gate relocation to unix and say so,
  rather than ship a mover whose safety checks are silently inert on a platform
  the release builds for.
