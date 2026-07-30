# HuggingFace source linking for referenced resources — proposal

A referenced-resource chip that carries a source link is a **claim about where the
bytes came from**. CivitAI-matched resources can make that claim because a hash
match against `GetModelVersionsByHashes` was persisted. HuggingFace-sourced
resources cannot, because `internal/hf` computes everything a link needs and then
throws all of it away.

This proposes what to persist, what to render, and — for files already on disk —
argues that most of the obvious retroactive design should **not be built**.

Related: `HUGGINGFACE-FALLBACK-RESEARCH.md` (the resolver this builds on),
`CUSTOM-NODE-RESOLUTION-DESIGN.md` (the honesty precedent this follows).

**Status: design. No production code, no migration file, no handler.**

---

## The recommendation, up front

1. **Build going-forward provenance recording.** It is exact, costs zero extra
   egress, and is ~1 migration + 1 store call + 1 chip branch.
2. **Do not build search-based retroactive attribution.** There is no global
   hash→file lookup on HuggingFace (proven below), so the only retroactive shape
   that scales is basename guessing — and the guessed link is precisely the claim
   this repo has repeatedly decided not to make.
3. **Curated-set retroactive attribution is cheap and proof-carrying (7 requests,
   28.3 KiB) but its addressable population is ~2 days wide and shrinking.** It is
   specified here as an optional Phase 2, and my call is to defer it until a user
   asks.

The reasoning is below; §5 is the load-bearing part.

---

## 0. Corrections to the brief's ground truth

The brief's reading of `internal/hf` is accurate. Four things elsewhere are not,
and two of them change the design.

**(a) The chip does NOT use `comfy.LocalMatch`.** `comfy.LocalMatch`
(`internal/comfy/resolve.go:38-41`) has exactly the right two fields, but its only
consumer is the **cloud AIR resolver** — `comfy.ResolveResources(apiGraph,
storeResourceLookup{…})` at `internal/web/cloud_handlers.go:168`, via
`resolveModelRef` (`internal/comfy/resolve.go:81-97`).

The chip surface is PR C1's own struct, in the sibling worktree
`.claude/worktrees/agent-ac954ac466656ccb0` on branch
`feat/pr-c1-workflow-ui-rework` @ `0bce6d7` (it is **not** on `main@62b6b48`, which
still renders resources as a plain `<ul>` at `internal/web/workflow_pages.go:854-863`):

- `type resourceInfo struct { Path string; ModelID int; VersionID int }` —
  `internal/web/workflow_resources.go` (C1)
- populated by `workflowResolver.localResource`, wired at
  `internal/web/workflow_handlers.go:310-327` (C1) from
  `store.LocalFileByBasename` (`internal/store/localfiles.go:232`)
- rendered by `workflowResourceChip` (C1), whose own doc comment already states
  the rule this proposal is here to satisfy:

  > *"not in the library, or resolved only by HuggingFace at download time → a
  > plain `<span>` with NO source link. `internal/hf` is a download-time fallback
  > resolver that persists NO provenance, so there is nothing to link to and we
  > must not imply otherwise."*

  **The change site is `resourceInfo` + `localResource` + the third branch of
  `workflowResourceChip`.** Nothing in `internal/comfy` moves.

**(b) "Nothing is persisted" understates it.** There is no HF column, correct —
but the web install path never writes to `local_files` **at all**. The only
`UpsertLocalFile` callers are `internal/queue/queue.go:260` (the CivitAI download
queue), `internal/library/scanner.go:230`, `internal/library/analyzer.go:210`,
`internal/library/quarantine.go:495`. An HF-installed file first appears in
`local_files` on the **next library scan**, with `model_id`/`version_id` NULL.

Consequence: "going forward" is not just new columns — it needs a write that does
not exist today. That is a direct argument for a **separate table** (§4).

**(c) `local_files` has 13 columns, not 8.** `0001_init.sql:63-72` is the base;
`0003_library.sql` adds `mtime`, `status`, `candidate_reason`, `kind`;
`0005_scan_root.sql:23` adds `scan_root`. Canonical list at
`internal/store/localfiles.go:13-14`. Immaterial to the design, but the upsert's
`ON CONFLICT (path) DO UPDATE` (`localfiles.go:71-87`) is not — see §4.

**(d) `?blobs=true` is not a thing.** It is not a documented parameter and not
needed: `lfs.oid` comes back unconditionally. `getTree` already sends
`recursive=true` (`internal/hf/api.go:120`). Details in §2.

**Confirmed:** `0015` is the next free migration number — on disk, on `main`, and
across every ref ever (`git log --all --diff-filter=A -- internal/store/migrations/`
yields exactly `0001`–`0014`).

---

## 1. Ground truth: the sha256 ↔ LFS oid identity is REAL

Everything downstream rests on this, so it was proven end-to-end rather than
assumed. Measured live 2026-07-29 (local) / 2026-07-30 UTC, anonymous, no token.

```
$ curl https://huggingface.co/Bingsu/adetailer/raw/main/face_yolov8n.pt
version https://git-lfs.github.com/spec/v1
oid sha256:70b640f8f60b1cf0dcc72f30caf3da9495eb2fb6509da48c53374ad6806e6a9c
size 6230011

$ curl 'https://huggingface.co/api/models/Bingsu/adetailer/tree/main?recursive=true'  # entry:
{"type":"file","oid":"e7223117b4a22a9806853068e5bdbb24c4b4d7aa","size":6230011,
 "lfs":{"oid":"70b640f8f60b1cf0dcc72f30caf3da9495eb2fb6509da48c53374ad6806e6a9c",
        "size":6230011,"pointerSize":132},
 "xetHash":"d1126adb762a2a935e6a9780395360ba6a223144ee012777342d4258180c3e93",
 "path":"face_yolov8n.pt"}

$ curl -L .../resolve/main/face_yolov8n.pt -o y8n.pt && sha256sum y8n.pt
70b640f8f60b1cf0dcc72f30caf3da9495eb2fb6509da48c53374ad6806e6a9c
```

**Three independent sources agree.** The bytes' sha256 == the tree's `lfs.oid` ==
the LFS pointer's `oid sha256:`. And `hashutil.SumFile`
(`internal/hashutil/hashutil.go:13-24`) emits **lowercase hex** of the whole file,
same as `lfs.oid`, so `local_files.sha256` and `hf.Match.SHA256` are directly
comparable strings (`hashutil.Equal` is case-insensitive anyway,
`hashutil.go:29-36`).

Two traps in that same JSON:

- 🔴 **`oid` (top level) is the git BLOB sha1 of the 132-byte pointer file**, not
  the content. `internal/hf/api.go:82` names it correctly; never fall back to it.
- 🔴 **`xetHash` is a THIRD, different hash** (Xet content-defined chunking). It is
  not a sha256 of anything we hold. It appears on every LFS entry and is a
  plausible-looking 64-hex string — exactly the shape of a wrong-column bug. It is
  currently absent from `treeEntry` (`api.go:78-87`); **keep it absent.**

---

## 2. Ground truth: the HuggingFace API surface

All live, anonymous, 2026-07-29/30 UTC.

### 2.1 The tree endpoint returns LFS oids by default

`GET /api/models/{repo}/tree/{rev}` returns `lfs: {oid, size, pointerSize}` on
every LFS entry **with no extra parameter**. Verified byte-identical output with
and without `?blobs=true` — because `blobs` is not a parameter at all. Fetching
`https://huggingface.co/.well-known/openapi.json` (972 KB, 252 paths) and reading
`/api/models/{namespace}/{repo}/tree/{rev}/{path}` gives its **complete** query
parameter set:

| param | default |
|---|---|
| `expand` | `false` |
| `recursive` | `false` |
| `limit` | *(unset)* |
| `cursor` | *(unset)* |

`recursive=false` matters: `h94/IP-Adapter`'s root tree is **5 entries, 2 of them
directories** — every `ip-adapter*` weight lives under `models/` and
`sdxl_models/`. `getTree` already sends `recursive=true` (`api.go:120`), so this is
handled; do not "simplify" it away.

### 2.2 Pagination is unhandled (latent)

Setting `limit` produces a real cursor header:

```
$ curl -D- '.../tree/main?limit=2'
link: <https://huggingface.co/api/models/h94/IP-Adapter/tree/main?expand=false&limit=2&cursor=ZXlKbWFXeGxYMjVoYldVaU9pSnpaSGhzWDIxdlpHVnNjeUlzSW5SeVpXVmZiMmxrSWpvaVpUWmtaRGMzWkRKbE1EUXhNR1kzT0RJeE56ZG1NelJsWVRKa05qTXpNekZsWTJWaU5UVTNZeUo5OjI%3D>; rel="next"
```

`getTree` (`api.go:119-124`) decodes one response into a slice and **parses no
`Link` header**. **Could not verify the default page size** — the largest repo
probed (`unsloth/DeepSeek-R1-GGUF`, 131 entries) returned no `Link`, so the default
is >131 and no probe triggered it naturally. This is a pre-existing truncation risk
for a very large repo, not something this proposal introduces; it becomes
*load-bearing* if Phase 2 is built, because a truncated tree silently yields a
**missing** oid, which reads as "no answer" rather than an error. Flagged, not
fixed here.

### 2.3 A nonexistent repo returns **401**, not 404

```
$ curl -o/dev/null -w '%{http_code}' https://huggingface.co/api/models/zzznotanowner9f3/zzznotarepo9f3
401
$ curl -D- ... | grep x-error
x-error-message: Invalid username or password.
```

Consistent across `/api/models/{repo}`, `/tree/{rev}`, and `/paths-info/{rev}`.
`internal/hf/http.go:56-58` maps only **404** → `ErrNotFound`, so a missing repo
surfaces as a generic transport error rather than a clean miss. On the existing
resolver this is benign (`Resolve` returns the error and the caller degrades), but
any retroactive loop MUST treat 401-on-a-public-path as "not found", or one
mistyped repo id in the curated map poisons a whole batch as a hard failure.

### 2.4 Gated repos ARE readable

`black-forest-labs/FLUX.1-dev` (gated) returns **200** anonymously for both
`/api/models/{repo}` and `/tree/main`, with `gated` set in the info body. So
**metadata and oids are readable for gated repos even though the bytes are not.**
A content-verified link to a gated repo is therefore possible and correct — the
user already has the bytes; the link is where they came from.

### 2.5 `paths-info` — a targeted batch, per repo

```
$ curl -X POST -H 'Content-Type: application/json' \
    -d '{"paths":["face_yolov8n.pt","README.md"]}' \
    https://huggingface.co/api/models/Bingsu/adetailer/paths-info/main
[{"type":"file","oid":"e7223117…","size":6230011,
  "lfs":{"oid":"70b640f8…","size":6230011,"pointerSize":132},…,"path":"face_yolov8n.pt"},
 {"type":"file","oid":"d0882d3c…","size":3962,"path":"README.md"}]
```

Works anonymously. Accepts up to **2000 paths** per request (`maxItems: 2000` in
the OpenAPI body schema). ⚠ **A path that does not exist is silently OMITTED from
the response** — no error, no null entry. Any caller must match responses back by
`path`, never by index.

Not needed for the design below (a whole recursive tree is already tiny), but it is
the right tool if per-file targeting ever matters.

### 2.6 `GET /api/models/{repo}/lfs-files` is 401 anonymously

It exists in the spec but requires auth (presumably repo-write). Not usable.

### 2.7 Rate limits — measured and documented, and they agree

Live response headers on every API call:

```
ratelimit: "api";r=497;t=168
ratelimit-policy: "fixed window";"api";q=500;w=300
```

`https://huggingface.co/docs/hub/rate-limits` (tiers as of Sept '25):

| plan | API | Resolvers | Pages |
|---|---|---|---|
| **Anonymous (per IP)** | **500** | 3,000 | 100 |
| Free user (token) | 1,000 | 5,000 | 200 |
| PRO | 2,500 | 12,000 | 400 |

All over **5-minute fixed windows**; over-limit is **429**. `hf_token`
(`internal/config/config.go:189`, env `HF_TOKEN` at `config.go:62`) is optional and
**anonymous access is sufficient for every endpoint this design uses** — a token
only doubles the API budget to 1,000/5min.

---

## 3. Ground truth: there is NO global hash→file lookup on HuggingFace

This is the decisive finding, and it is negative. Three independent confirmations:

**(a) The OpenAPI spec has zero hash parameters.** Across all **252** documented
paths, a scan of every parameter name on every method for
`/(sha256|hash|oid|digest|checksum|blob)/i` returns **0 hits**. The only `sha`
parameter anywhere is a *path* segment on `DELETE
/api/models/{ns}/{repo}/lfs-files/{sha}` — a destructive, owner-only operation.

**(b) Search by hash returns empty, not an error** — the dangerous failure mode:

```
$ curl 'https://huggingface.co/api/models?search=70b640f8f60b1cf0dcc72f30caf3da9495eb2fb6509da48c53374ad6806e6a9c&limit=5'
[]
```

**(c) Upstream asked for it and were told no.** `huggingface/huggingface_hub`
issue **#3069, "Model File Lookup by SHA256 Hash"**, opened **2025-05-09**, still
**open** with 5 comments. Maintainer **Wauplin**, 2025-05-20:

> *"Creating a server-side index + an endpoint to serve its API seems like a big
> investment server-side for an unclear result."*

His suggested workaround is exactly the per-repo shape below: *"I list the files of
this model in the repo and check their sha256."*

Two later comments are worth reading before deciding this is a small gap.
`suianon`, 2025-08-28, describes **our exact use case**:

> *"A use case harder to work around is identifying unknown models for the sake of
> reproducibility. In the stable diffusion/comfyui scene, inference workflows meant
> to be reproducible break a lot, and unidentifiable models is a big reason why
> it's often hard to fix. When a script generates a workflow configuration, the
> only thing it can know about the model is its hash."*

Bumped again 2025-12-23 and **2026-07-17** — twelve days ago. Nothing shipped.

> **Conclusion.** CivitAI's `GetModelVersionsByHashes` has **no HuggingFace
> counterpart, by deliberate upstream decision.** Any retroactive design must be
> per-repo (you must already suspect *which* repo) — which means the repo comes
> from a **basename guess**, and the hash can only ever *confirm or refute* that
> guess. It can never *find* the repo. That constraint drives §5 entirely.

---

## 4. Going forward — the easy half

### 4.1 What we know at download time, and how solid it is

`resolveInstallPlan` (`internal/web/run_download.go:398`) builds the HF branch at
`run_download.go:428-441`:

```go
if m := s.resolveHF(ctx, filename); m != nil && s.hfInstallEligible(m) {
    return installPlan{FileName: m.FileName, RemoteFileName: m.FileName,
        URL: m.URL, DestPath: dest, ExpectedSHA256: m.SHA256, SourceHF: true}, resolveInstallOK
}
```

`hfInstallEligible` (`internal/web/hf_fallback.go:71-79`) requires
`m.AutoDownloadEligible()`, which requires `m.SHA256 != ""`
(`internal/hf/resolve.go:64-66`).

> **Therefore `pd.ExpectedSHA256` is ALWAYS non-empty on the HF install path**, and
> `comfy.WriteModelStreamVerified` (`internal/comfy/download_target.go:140`, called
> at `run_download.go:889`) hashes the stream and compares **before the atomic
> rename** — mismatch yields `ErrHashMismatch` and the temp file is removed
> (`download_target.go:16`, `:128`).

So at the moment a rename succeeds we hold, verified: the repo, the path in the
repo, the pinned commit sha, and a digest that provably matches the bytes now on
disk. **This is the strongest provenance available anywhere in this codebase** —
stronger than the CivitAI linkage, which is a hash *lookup* rather than a
transfer-time verification.

No signature change to `WriteModelStreamVerified` is needed (it returns bytes, not
the digest — but the digest is already known to the caller and already verified).

### 4.2 Migration 0015 — a separate table, keyed by content hash

Two candidate shapes:

| | columns on `local_files` | separate `hf_provenance` table |
|---|---|---|
| Row exists at install time? | **no** — nothing writes `local_files` on the web install path (§0b); the row appears only after the next scan | **yes** — written the instant the rename succeeds |
| Survives a rename/move? | no (PK is `path`) | **yes** (keyed by sha256) |
| Clobber risk | `UpsertLocalFile`'s `ON CONFLICT (path) DO UPDATE` (`localfiles.go:71-87`) rewrites the row; provenance would need preserve-not-clobber handling like `scan_root` (`:86-87`) | none — different table, different lifecycle |
| Duplicate copies of the same file | one row per path | **one row covers all copies** |
| Cost | none | one extra query on the chip path |

**Recommend the separate table, keyed by sha256.** The decisive reason is
semantic, not mechanical: the claim a source link makes is *about the bytes*.
Keying it by the bytes' hash makes it survive a rename, cover every copy, and — the
practical one — be writable **before** the file has ever been scanned.

```sql
-- 0015_hf_provenance: where a local file's BYTES came from on HuggingFace.
--
-- Keyed by CONTENT HASH, not path. The claim a source link makes is about bytes, so
-- it must survive a rename, a move, and a second copy of the same file — and it must
-- be writable at download time, BEFORE any library scan has indexed the file into
-- local_files (nothing on the web install path writes local_files; see
-- internal/queue/queue.go:260 for the only download-time writer, which is CivitAI).
--
-- confidence is deliberately a CLOSED set of two values, both of which are PROOFS:
--   'recorded' — we fetched these bytes from this URL and verified the digest.
--   'verified' — this file's sha256 equals an LFS oid published at this repo@revision.
-- There is NO 'guessed'. A basename match that no hash agrees with is not recorded at
-- all, so an unprovable claim is UNREPRESENTABLE rather than merely unrendered.
--
-- No url column: it is derivable from (repo, revision, path) via hf.resolveURL
-- (internal/hf/resolve.go:340) and would be duplicated state that can drift.
--
-- Holds only public metadata — a public repo id, a path within it, a commit sha.
-- No token, no secret, no user data.
CREATE TABLE hf_provenance (
    sha256      TEXT NOT NULL,  -- lowercase hex; the file's content sha256 == its LFS oid
    repo        TEXT NOT NULL,  -- e.g. "Bingsu/adetailer"
    path        TEXT NOT NULL,  -- path WITHIN the repo (may contain subdirs)
    revision    TEXT NOT NULL,  -- the pinned commit sha the URL resolves at
    confidence  TEXT NOT NULL CHECK (confidence IN ('recorded','verified')),
    recorded_at TEXT NOT NULL,  -- RFC3339 UTC
    PRIMARY KEY (sha256, repo, path)
);
```

Notes on the shape, each deliberate:

- **No separate index on `sha256`.** SQLite backs the PK with an index whose
  leftmost column is `sha256`, so lookup-by-hash is already covered. Adding one
  would be dead weight.
- **PK is `(sha256, repo, path)`, not `sha256`.** Mirrors are real: the same bytes
  are legitimately published in several repos, and *each* is a true statement. A
  bare `sha256` PK would silently overwrite one true attribution with another.
  Rendering picks one deterministically (§6.3).
- **`CHECK` on `confidence`** is the structural half of the honesty rule. Prose in
  a comment can be ignored by a future writer; the constraint cannot.
- **No `gated`, `license`, `downloads`, `likes`.** All are live upstream state that
  drifts; all are already fetched on demand by the existing resolver. YAGNI.

### 4.3 Where the write goes

One call, in `downloadModelFile` (`internal/web/run_download.go:864`), **after**
`WriteModelStreamVerified` returns nil at `run_download.go:889` — never before,
so a failed or hash-mismatched download records nothing.

It needs `pd` to carry the HF triple. `installPlan` already carries `SourceHF` and
`ExpectedSHA256` (`run_download.go:432-439`); add `HFRepo`, `HFPath`,
`HFRevision`, populated from the same `*hf.Match` at the same site, and thread them
onto `pendingDownload` exactly as `SourceHF` is threaded today.

Failure to record must be **non-fatal** — logged, never surfaced as a download
failure. The file is on disk and works; a missing link is cosmetic.

### 4.4 Link-only matches record NOTHING

When a match is link-only (gated, no determinable subdir, or no LFS oid —
`hf.Match.AutoDownloadEligible`, `resolve.go:61-69`) the user is shown "Open on
HuggingFace ↗" (`internal/web/hf_fallback.go:83-84`) and **no bytes are
transferred**. There is no local file, therefore no sha256, therefore nothing to
attribute and no chip to attach it to.

Recording an *intent* here would be the exact error this proposal exists to
prevent: the user may have clicked through and downloaded manually, or clicked
through and downloaded something else, or not clicked at all. **Record nothing.**
The existing link on the Fix popover is already the correct and complete answer for
that case.

### 4.5 Which URL the chip links to — pinned revision, always

`hf.Match.Revision` is the concrete commit sha from `info.SHA`
(`internal/hf/resolve.go:237-240`), and `resolveURL` (`resolve.go:340-349`) builds
`/{repo}/resolve/{revision}/{path}`.

**Link to the pinned revision.** It is the only URL that stays true: it resolves to
*the bytes we are making a claim about*, permanently, even after the repo's `main`
moves or the file is replaced. A link to `/{repo}` (the `PageURL()` degrade) or to
`resolve/main/…` decays into a claim about *different bytes* — which is a
confidently-wrong answer arriving silently and later, the worst variety.

Practical wrinkle: a `/resolve/` URL is a **download**, not a page. A chip whose
click starts a multi-GB download is user-hostile. Resolve it this way:

- **Chip href** → `https://huggingface.co/{repo}/blob/{revision}/{path}` — the
  human *file view* at the pinned revision (same permanence, renders a page).
- **Hover/title** → repo, path, and the short revision, so the claim is legible
  without a click.
- `PageURL()` (`resolve.go:73-75`) stays what it is: the degrade for the
  Fix-popover link-only path, which is a different surface.

⚠ **`/blob/{revision}/{path}` as a page URL is asserted from HF's URL conventions,
not probed in this research.** Verify before implementing (§9, V4).

Every such href must pass `isSafeHTTPURL` (as `workflowSourceLinks` does at
`internal/web/workflow_pages.go:711`) and carry `target=_blank rel=noopener`. Both
`repo` and `path` originate from the HF API — escape every segment via the existing
`resolveURL` escaping discipline (`resolve.go:340-349`).

---

## 5. Retroactive attribution — the hard half, and the case against most of it

### 5.1 The only viable mechanism

From §3: no global hash lookup exists. So retroactive attribution can only be:

```
local basename → (guess a repo) → fetch that repo's tree → compare local sha256 to lfs.oid
                                                              ├─ equal → PROOF
                                                              └─ absent → NO ANSWER
```

The hash **confirms or refutes**; it never **finds**. This is not a limitation of
our implementation — it is the shape upstream chose (§3c).

### 5.2 The curated set is nearly free — measured

The `curatedMap` (`internal/hf/resolve.go:110-154`) has 7 repos. Fetching all 7
recursive trees, anonymously, 2026-07-30 UTC:

| repo | files | with LFS oid | bytes | time |
|---|---|---|---|---|
| `Bingsu/adetailer` | 14 | 12 | 3,799 | 0.13s |
| `madebyollin/sdxl-vae-fp16-fix` | 12 | 7 | 2,761 | 0.12s |
| `stabilityai/sd-vae-ft-mse` | 5 | 2 | 927 | 0.12s |
| `h94/IP-Adapter` | 30 | 25 | 9,046 | 0.12s |
| `comfyanonymous/ControlNet-v1-1_fp16_safetensors` | 31 | 29 | 9,859 | 0.12s |
| `comfyanonymous/flux_text_encoders` | 6 | 4 | 1,446 | 0.12s |
| `ai-forever/Real-ESRGAN` | 5 | 3 | 1,098 | 0.12s |
| **total** | **103** | **82** | **28,936 (28.3 KiB)** | **~0.9s** |

**7 requests out of a 500-per-5-minutes anonymous budget buys a complete,
proof-grade oid index for the entire trusted set.** It caches trivially in the
existing `raw BLOB + fetched_at` pattern (`0007_model_cache.sql:19-24`,
`0010_community_cache.sql:6-12`, `0013_nodepack_cache.sql:23-27`).

### 5.3 The false-negative worry is smaller than expected

All 21 non-LFS files across those 7 repos are `.gitattributes`, `README.md`,
`config.json`, or `fig1.png`. **Zero weight files lack an oid.** The reason is
structural, not luck — HF's default `.gitattributes` LFS-tracks the extensions we
care about:

```
$ curl https://huggingface.co/Bingsu/adetailer/raw/main/.gitattributes
*.bin  filter=lfs …    *.ckpt filter=lfs …    *.onnx filter=lfs …
*.pt   filter=lfs …    *.pth  filter=lfs …    *.safetensors filter=lfs …
```

Note `fig1.png` (469 KB) is **not** LFS — so this is a tracking-pattern rule, not a
size rule. Don't reason about it as "small files lack oids".

Remaining true false-negative cases, honestly:

1. **Xet-only repos.** Newer repos may store content via Xet; `lfs` was present on
   every entry probed here, but a Xet-native repo with no `lfs` block would yield
   `SHA256 == ""` and silently produce no answer. **Could not verify** — no such
   repo was found among the recognized orgs. Treat "no oid" as "no answer", never
   as "no match".
2. **A locally converted/quantized/pruned file.** Different bytes, different hash,
   correctly no answer. This is the mechanism working.
3. **A locally renamed file.** The basename guess fails first, so the hash is never
   consulted. Ironically the hash *could* have identified it — but only within a
   repo we already guessed, which the rename prevents.
4. **Truncated tree** for a >default-page-size repo (§2.2) — a missing oid reads as
   "no answer".

### 5.4 The uncurated case is expensive AND produces only guesses

For a basename outside the curated map, `searchResolve` (`resolve.go:273-310`)
runs 1 search + up to `searchTreeProbes` (5) repos × 2 requests
(`getModelInfo` + `getTree`) = **up to 11 requests per file**. At 500/5min
anonymous that is ~45 files per window — a genuinely long background job over a
real library.

And what does it buy? A candidate repo chosen by `betterMatch`
(`resolve.go:329-334`): recognized-org first, then **highest downloads**. That is
an explicitly arbitrary tiebreak among possibly-many true matches. If the hash then
agrees → proof, and the 11 requests were worth it. If it does not → a guess, which
§6 says we will not display.

**So the expensive half of retroactive attribution produces, in its failure case,
exactly the artifact we have decided not to render.**

### 5.5 Basename matching is not merely uncertain — it is often wrong

Measured live in **one** recognized-org repo, `h94/IP-Adapter`:

```
model.safetensors      x2  models/image_encoder/model.safetensors      lfs oid 6ca9667da1ca9e0b…
                           sdxl_models/image_encoder/model.safetensors  lfs oid 657723e09f46a7c3…
pytorch_model.bin      x2  models/image_encoder/…                      lfs oid 3d3ec1e66737f77a…
                           sdxl_models/image_encoder/…                 lfs oid 2999562fbc02f9dc…
config.json            x2  (no oid on either)
```

`matchInRepo` (`resolve.go:245-266`) returns the **first** tree entry whose
basename matches. For `model.safetensors` in this repo that is a coin flip between
**two genuinely different files** — and this is inside a *single* repo, before
considering that `model.safetensors` / `config.json` /
`diffusion_pytorch_model.safetensors` exist across a large fraction of the Hub.

A hash check resolves this exactly and instantly. A basename match cannot resolve
it at all. This is the strongest single argument for "hash or nothing".

### 5.6 The population retroactive attribution can serve is ~2 days wide

The decisive practical fact:

```
$ git log --diff-filter=A --format='%h %ad %s' --date=short -- internal/hf/
10152ef 2026-07-27 hf: SSRF-hardened HuggingFace client + curated filename resolver
$ git log --diff-filter=A -- internal/web/hf_fallback.go
eb69c64 2026-07-27 web: HuggingFace fallback in resolve/download flow
$ git tag --contains eb69c64 | head -1
v0.1.62
```

**The HuggingFace fallback shipped 2026-07-27 — two days ago** (today is
2026-07-29), in v0.1.62; `main` is now at v0.1.80.

So the set of files that this app installed from HuggingFace *without* provenance
is at most two days of one user's dogfooding. Everything after Phase 1 ships is
covered exactly. The only other population is files the user downloaded **by hand**
that happen to live in those 7 curated repos — a real but small and unmeasurable
set.

**Could not verify** the actual hit rate: measuring it would require reading
`~/.config/civitai-manager/civitai-manager.db`, which this research was instructed
not to touch. That inability is itself an argument — building a feature whose value
cannot be measured before shipping is how speculative work gets justified.

### 5.7 Verdict: don't build it (yet)

| | build | skip |
|---|---|---|
| Going-forward recording | **yes** | — |
| Curated-set oid index (Phase 2) | cheap (7 req), proof-only, no new UI | population ~2 days wide; unmeasurable value |
| Search-based retroactive (Phase 3) | — | **no.** 11 req/file to produce a guess we refuse to render |

**My recommendation: ship Phase 1 only. Specify Phase 2, build it only if a user
asks. Never build Phase 3 as scoped.**

If Phase 2 is later wanted, build it as an **opportunistic branch of the existing
library scan**, not as a feature: the scan already computes every file's sha256
(`internal/library/scanner.go`) and already egresses those hashes to CivitAI under
`match_remote`. For any file the CivitAI batch does not match, consult a cached
curated-oid index and record `confidence='verified'` on a hit. That is ~7 extra
requests per scan, no new job, no new UI, no new poller, no new opt-out — and it
only ever writes proofs. Anything more elaborate than that is not worth it.

---

## 6. The honesty model — proven vs guessed

This is the part that matters most, and the repo already has the answer written
down three times.

### 6.1 The precedents

- **`install-and-run must NEVER substitute a file silently`** (`CLAUDE.md`): the
  fallback must be **offered, not performed**; a second click must echo the exact
  remote basename.
- **The `.*` catch-all guard** (`CUSTOM-NODE-RESOLUTION-DESIGN.md`): one index
  pattern matches every class name, so applied naively *every* unresolvable node
  gets confidently attributed to one pack — *"worse than saying 'unattributed'"*.
  The fix is empirical (probe with a nonsense control), not a blacklist.
- **PR C1's own chip comment**: *"persists NO provenance, so there is nothing to
  link to and we must not imply otherwise."*

All three say the same thing: **an unknown must be allowed to render as unknown.**

### 6.2 Two tiers, both proofs

| tier | what is actually known | UI wording | link |
|---|---|---|---|
| **`recorded`** | We fetched these bytes from this URL and the digest verified before the rename (§4.1) | *"Downloaded from HuggingFace"* | yes → `/{repo}/blob/{revision}/{path}` |
| **`verified`** | This file's sha256 equals an LFS oid published at `repo@revision` | *"Identical file on HuggingFace"* | yes → same |
| ~~`guessed`~~ | A basename matched a file in some repo | — | **none** |

The wording distinction is not decoration. `recorded` supports *"you got this
here."* `verified` supports only *"these exact bytes are published here"* — the
user may have obtained them from a mirror, from CivitAI, or from a friend's USB
stick. Both statements are true; they are **different statements**, and the UI
should not merge them into one. Given the tiers are already distinct rows in the
schema, distinguishing them in copy costs nothing.

### 6.3 Should a guessed link be shown at all? No.

Arguments for showing it (stated fairly): a plausible link is more useful than
nothing; users can judge for themselves; the resolver already picks a best
candidate for the install flow.

They lose, for four reasons:

1. **§5.5 is empirical, not theoretical.** `model.safetensors` resolves to two
   different files inside one repo. For generic basenames a guessed link is not
   "probably right" — it is closer to a coin flip, and the user cannot tell which
   flip they got.
2. **A wrong source link is worse than no link** in exactly this domain. The chip's
   whole purpose is workflow reproducibility (`suianon`'s comment in §3c is about
   this precise failure). A link to the *wrong* weights sends someone to download
   bytes that will silently produce different output. Missing information stops the
   user; wrong information routes them into a subtle failure.
3. **The install-and-run precedent already settled this** for a strictly *safer*
   case: there, a substitution was gated behind an explicit two-click confirmation
   naming both files, *and* a further round-trip if the remote basename changed
   between clicks. A chip has no confirmation step, no second click, no place to
   name the uncertainty. If a substitution needed two gated clicks, an unverifiable
   attribution cannot be a bare `<a>`.
4. **The schema makes it unrepresentable.** With `CHECK (confidence IN
   ('recorded','verified'))` there is no way for a future change to leak a guess
   into the chip without a deliberate migration — deterministic, not prose.

**What a guessed match may do instead:** nothing on the chip. If someone later
wants an affordance for "I have no idea where this came from", it must be a
**search action**, labelled as a search — *"Search HuggingFace for
`face_yolov8n.pt` ↗"* → `https://huggingface.co/models?search=…`. That is an honest
offer of *where to look*, not a claim about *where it came from*. It is out of
scope here and should not ship alongside Phase 1, where it would visually
compete with the real links and dilute them.

### 6.4 How the chip renders it

Extend C1's `resourceInfo` (do not reshape it):

```go
type resourceInfo struct {
    Path      string
    ModelID   int
    VersionID int
    HF        *hfProvenance // nil unless a PROVEN provenance row exists
}

type hfProvenance struct {
    Repo, Path, Revision string
    Confidence           string // "recorded" | "verified"
}
```

`localResource` (`internal/web/workflow_handlers.go:310-327`, C1) gains one
store call: it already has the `*store.LocalFile`, so
`store.HFProvenanceBySHA256(lf.SHA256)` is a primary-key lookup. No egress on the
render path — the chip stays offline-renderable, matching `resourceInfo`'s existing
doc comment (*"derived entirely from the store (never a civitai fetch)"*).

`workflowResourceChip` gains a branch **after** the existing `info.linked()` check,
so a CivitAI linkage keeps priority (it points at an in-app page; HF points off-site):

```
if info.linked()        → <a href="/models/{id}?modelVersionId={v}">  (unchanged)
else if info.HF != nil  → <a href="https://huggingface.co/{repo}/blob/{rev}/{path}"
                              target=_blank rel=noopener>             (new)
else                    → <span>                                      (unchanged)
```

Visual distinction: a distinct `data-src="hf"` attribute driving a `.cm-res-chip`
variant in `internal/web/assets/app.css` — **not** a new Tailwind utility, per the
committed-purged-build rule in `CLAUDE.md`. The `↗` glyph C1 uses for a linked chip
should differ for an off-site link, and `h.Title` should carry
`"{repo} @ {rev[:7]} — downloaded from HuggingFace"` or `"— identical file on
HuggingFace"`. Both light and dark paths styled.

If a resource has **multiple** provenance rows (mirrors, §4.2), render exactly one,
chosen deterministically: `recorded` before `verified`, then lowest `recorded_at`,
then lexical `repo`. Never render several — a chip showing three sources reads as
uncertainty, which is the opposite of what these rows mean.

---

## 7. Egress and opt-out

### 7.1 Phase 1 adds NO egress at all

Provenance recording happens on a download the user has already initiated, with
data already in hand. The chip read is a local SQLite primary-key lookup. **Zero
new outbound requests.** No new knob, no new disclosure — there is nothing to
disclose.

### 7.2 Phase 2, if ever built, rides `hf_fallback`

The precedent the brief names is `resolve_node_packs`
(`internal/config/config.go:206`, default via `ResolveNodePacksEnabled`
`config.go:302-304`), enforced by never constructing the client
(`internal/web/nodepack_attribute.go:117-125`).

**HuggingFace already has the identical knob**, which the brief did not mention:

```go
// internal/config/config.go:195
HFFallback *bool `yaml:"hf_fallback"`
// internal/config/config.go:295
func (c *Config) HFFallbackEnabled() bool { return c.HFFallback == nil || *c.HFFallback }
```

enforced the same way at `internal/web/hf_fallback.go:32-40`:

```go
func (s *Server) hfClientOrNil() hfClient {
    if s.hfClientFn != nil { return s.hfClientFn() }
    if !s.cfg.HFFallback { return nil }
    return hf.NewClient(s.cfg.HFToken)
}
```

`hfClientOrNil` is in fact the model `nodepack_attribute.go:108-112` cites for
itself. **Phase 2 must go through `hfClientOrNil` and rides `hf_fallback` — no new
setting.** A user who turned off HF fallback has said "do not talk to
huggingface.co"; a background attribution sweep is emphatically covered by that.

One honest caveat if Phase 2 is built: `hf_fallback` currently gates
**user-initiated, one-at-a-time** egress. A library-wide sweep is a different
*character* of request even at the same host, and it would be reasonable to want it
separately controllable. Since Phase 2 as recommended piggybacks on an explicit,
user-initiated **scan** — which already egresses every file's sha256 to
civitai.com under `match_remote` — it stays user-initiated and does not need its
own knob. Any design that runs it on a timer or on page load **does** need one.
See §11, Q3.

Also worth noting for a future settings surface: neither `hf_fallback` nor
`resolve_node_packs` has a UI control today (only `match_remote` does, via the
checkbox at `internal/web/library_pages.go:384-398`). They are YAML-only and
disclosed in prose. That is a pre-existing gap, not this proposal's to close.

### 7.3 Client hardening — reuse, do not rebuild

Any Phase 2 fetch uses the **existing** `internal/hf.Client` unchanged: https-only
including every redirect hop (`client.go:141-157`), dial-time
private/loopback/link-local/ULA/CGNAT/multicast block on the **resolved** IP
(`client.go:123-137`, `:193-214`), exact dotted-suffix host allowlist
(`client.go:163-179`), redirect cap of 10 (`client.go:40`), 8 MiB bounded body
(`internal/hf/http.go:16`), token attached only to the HF origin and dropped on the
CDN hop (`client.go:153-155`, `:184-187`).

The only change the tree path might need is `Link`-header pagination (§2.2), which
is a decode concern inside `getTree`, not a transport concern. **No new client.**

---

## 8. Phased implementation

Small, compilable increments — every step leaves `go build ./... && go vet ./... &&
go test ./...` green and `gofmt -l ./internal/ ./cmd/` empty.

### Phase 1 — going forward (recommended, build this)

1. **Migration + store, no callers.** `0015_hf_provenance.sql` exactly as §4.2, plus
   `internal/store/hf_provenance.go`: `UpsertHFProvenance(p HFProvenance) error`,
   `HFProvenanceBySHA256(sha string) ([]HFProvenance, error)`, and a `HFProvenance`
   type in `types.go`. Unit tests against a temp DB. **Ships alone, inert.**
2. **Thread the triple to the download.** Add `HFRepo`/`HFPath`/`HFRevision` to
   `installPlan` (`run_download.go:432-439`) and `pendingDownload`, populated at
   `run_download.go:428-441`. Nothing reads them yet. **Compiles, no behaviour
   change.**
3. **Record on success.** One `UpsertHFProvenance` after
   `WriteModelStreamVerified` returns nil (`run_download.go:889`), guarded on
   `pd.SourceHF && pd.ExpectedSHA256 != "" && pd.HFRepo != ""`. Errors logged, never
   fatal. **This is the whole backend.**
4. **Read on the chip.** In C1's `localResource`
   (`workflow_handlers.go:310-327`), fetch provenance by `lf.SHA256` and populate
   `resourceInfo.HF` with the deterministic pick (§6.4). Chip still renders as
   today. **Compiles, no visual change** — a useful checkpoint.
5. **Render.** The new branch in `workflowResourceChip` + the `.cm-res-chip[data-src=hf]`
   rule in `app.css`, light and dark.
6. **Docs.** A line in `CLAUDE.md` under `internal/store` and `internal/hf`
   recording the sha256==LFS-oid identity, the `xetHash`/blob-`oid` traps, and the
   no-guessed-links rule.

Steps 1–3 are independently mergeable and useful even if 4–6 slip: provenance
starts accumulating the moment step 3 ships, so the retroactive gap stops growing
before any UI exists. Sequence them that way.

### Phase 2 — curated-set oid index (specified, NOT recommended now)

7. `0016_hf_oid_cache.sql` mirroring `0013_nodepack_cache.sql` (`source TEXT PRIMARY
   KEY, raw BLOB NOT NULL, fetched_at TEXT NOT NULL`), one row per curated repo,
   serve-fresh → fetch → fall back to stale on failure.
8. A pure `hf.OIDIndex` (`map[sha256] → {repo, path, revision}`) built from cached
   tree JSON. No I/O, fully unit-testable.
9. An opportunistic branch in the library scan: for each scanned file with a sha256
   and no CivitAI match, consult the index; on a hit write
   `confidence='verified'`. Gated on `hf_fallback` via `hfClientOrNil`.

### Phase 3 — search-based retroactive

**Do not build.** See §5.4, §5.5.

---

## 9. Test plan

**Store (`internal/store`, temp DB)**

- T1 `UpsertHFProvenance` round-trips; re-upsert of the same `(sha256, repo, path)`
  updates rather than duplicating.
- T2 Two rows for one sha256 with different repos both persist (mirror case).
- T3 `confidence='guessed'` is **rejected by the CHECK constraint**. This test is
  the enforcement of §6.3 — it must fail loudly if someone relaxes the constraint.
- T4 `HFProvenanceBySHA256` on an unknown hash returns empty, not an error.
- T5 Migration applies cleanly on a DB already at 0014 and is idempotent on rerun.

**Download path (`internal/web`, fake HF client + temp dir + temp DB)**

- T6 A successful HF install writes exactly one row with `confidence='recorded'`,
  and its sha256 equals the digest of the bytes on disk.
- T7 A **hash-mismatched** download (`ErrHashMismatch`) writes **no** row and leaves
  no file. The critical negative test.
- T8 A CivitAI download (`SourceHF == false`) writes no row.
- T9 A link-only match (gated / no subdir / no oid) writes no row (§4.4).
- T10 A store error during recording does **not** fail the download.

**Chip rendering (`internal/web`, gomponent render assertions)**

- T11 Local file + provenance, no CivitAI linkage → an `<a>` to
  `huggingface.co/{repo}/blob/{revision}/{path}` with `target=_blank rel=noopener`.
- T12 CivitAI linkage **and** provenance → the CivitAI in-app link wins; exactly
  one link is emitted.
- T13 Local file, **no** provenance → a `<span>`, no `href`, no `huggingface.co`
  anywhere in the fragment. The regression test for "no guessed links".
- T14 A repo/path containing `../`, a space, `%`, and a `"` renders escaped; the
  href passes `isSafeHTTPURL`; no attribute injection.
- T15 Multiple provenance rows → exactly one chip link, deterministically chosen.
- T16 `recorded` and `verified` produce **different** title/label text.
- T17 An ambiguous basename (`LocalFileByBasename` returns nil, `localfiles.go:254-256`)
  → no path, no link — C1's existing behaviour is preserved.

**Live verification (mandatory — this repo's fake-reader lesson)**

- V1 Run the dogfood binary against a **temp DB**, install a real small HF file
  (`face_yolov8n.pt`, 6.2 MB, well under any cap), confirm the row lands with the
  correct sha256, and `curl` the workflow detail page to assert the chip's `href`.
  Do **not** use `~/.config/civitai-manager/civitai-manager.db`.
- V2 `sha256sum` the installed file and confirm it equals both the stored value and
  the live `lfs.oid` — re-proving §1 through our own code path rather than curl.
- V3 With `hf_fallback: false`, assert (Phase 2 only) the client factory is never
  invoked — the same test seam `nodePackResolverFn` uses
  (`internal/web/server.go:185-191`).
- V4 **Confirm `https://huggingface.co/{repo}/blob/{revision}/{path}` returns a
  page** for a pinned sha, not a redirect to `main` and not a download (§4.5). This
  was asserted, not probed.
- V5 Re-run the §5.2 curated-index probe before Phase 2 to confirm the numbers still
  hold, and re-check whether any recognized-org repo has become Xet-only with no
  `lfs` block (§5.3, case 1).

**Gate**: `go build ./... && go vet ./... && go test ./... && gofmt -l ./internal/ ./cmd/`,
plus `-race` on `web`/`store`. `/audit-pr` scaled to blast radius — Phase 1 touches
a DB migration and a download path, so full adversarial audit; Phase 2 adds egress
and would need it regardless.

---

## 10. Invariants this must not break

- **Offline / no-CDN.** The chip renders from SQLite only; the added `href` is a
  user-clicked external link, not a fetched subresource. No new asset.
- **Theme-aware.** Both `data-theme` paths styled for the new chip variant.
- **Tailwind is a committed purged build.** New styling goes in `app.css` as a
  `.cm-*` rule, not a new utility class.
- **Migrations are append-only and ordered.** `0015` only; no edit to `0001`–`0014`.
- **CSRF on every POST.** Phase 1 adds no endpoint. Phase 2's scan hook rides the
  existing scan POST.
- **Loopback-gating.** Unchanged — no new path-taking endpoint.
- **Hash cache keyed by `(path, size, mtime)`.** Untouched; provenance is a separate
  table and must not invalidate it.
- **Remote match defaults ON and is disclosed.** Unchanged. Phase 2 would piggyback
  on that same scan without widening what it already discloses to a *new* host —
  which is exactly why it needs the `hf_fallback` gate.
- **`internal/hf` stays a pure resolver.** Provenance is written by the web layer
  that owns the download, not by the resolver.

---

## 11. Open questions — need the user's decision

- **Q1 — Ship Phase 2 at all?** My call is no, on §5.6 (two-day population,
  unmeasurable value). But you can measure what I could not: how many rows in your
  real `local_files` have `sha256 <> ''` and `model_id IS NULL` *and* a basename
  matching one of the 82 curated oids. If that number is meaningfully non-zero,
  Phase 2 earns its 7 requests. **One query decides this; I was scoped out of
  running it.**

- **Q2 — Is "identical file on HuggingFace" (`verified`) too weak a claim to show
  as a link at all?** It proves the bytes are published there; it does not prove
  the user got them there. I argue it is worth showing with distinct wording (§6.2).
  The stricter position — *only* `recorded`, i.e. only what we downloaded ourselves
  — is defensible and would delete Phase 2 entirely along with half the schema.
  This is the single biggest fork in the doc.

- **Q3 — If Phase 2 ships, does it need its own opt-out?** I say no while it is
  piggybacked on a user-initiated scan (§7.2). If you would rather it run on a timer
  or on page load, it becomes background bulk egress and needs its own knob and its
  own disclosure — say so before it is built, not after.

- **Q4 — Should the "Search HuggingFace" affordance (§6.3) exist for unattributed
  chips?** I recommend not shipping it with Phase 1 — it would visually compete with
  the real links. But it is the honest answer to "I have no idea where this came
  from", and you may value that more than I do.

- **Q5 — `hf_fallback` and `resolve_node_packs` have no UI control.** Both are
  YAML-only with prose disclosure; only `match_remote` has a checkbox. Out of scope
  here, but if a settings surface is coming, these two belong on it.

- **Q6 — Fix `getTree` pagination now or later?** (§2.2) Pre-existing, latent, and
  only load-bearing if Phase 2 ships. Small fix; wrong moment to bundle it into a
  provenance PR unless you want it.

---

## 12. Could not verify

Stated plainly rather than papered over:

1. **The real-world hit rate of retroactive attribution.** Requires reading the
   user's live DB, which was out of scope. Q1 above is the query that settles it.
2. **The tree endpoint's default page size.** No probed repo exceeded 131 entries
   without a `Link` header. Pagination demonstrably exists (`limit=2` produced a
   cursor); the threshold is unknown.
3. **Whether `/{repo}/blob/{revision}/{path}` renders a page for a pinned commit
   sha.** Asserted from HF's URL conventions; V4 must confirm before implementing.
4. **Xet-only repos with no `lfs` block.** Every entry probed carried `lfs`
   alongside `xetHash`, but no Xet-native repo was located to confirm the negative
   case exists or how it decodes.
5. **Browser rendering of the new chip.** No browser is available on this host
   (MCP Playwright broken, no system chromium). HTTP-level `curl` assertions verify
   the emitted markup, not the painted DOM — say so when reporting.
6. **Whether PR C1's chip surface will land in the shape read here.** It was read
   from `feat/pr-c1-workflow-ui-rework @ 0bce6d7` in a sibling worktree while in
   flight; `resourceInfo` and `localResource` could still change before merge.
