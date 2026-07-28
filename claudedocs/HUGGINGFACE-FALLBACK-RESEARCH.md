# HuggingFace Fallback Model Search — Research & Design Recommendation

**Status:** Research / design only. No production code proposed here — this is a
design others can implement. Date: 2026-07-27.

**Problem statement.** civitai-manager's "Download & run" / resolve-model flow maps
a workflow's missing model **filename** → a CivitAI model → a download URL → writes
the file into the local ComfyUI models dir → runs. It is **CivitAI-only**. Many
ComfyUI auxiliary models — Ultralytics/adetailer YOLO detectors, some VAEs,
upscalers, IPAdapter weights, CLIP/text-encoders — are **not on CivitAI**; they live
on **HuggingFace**. Concrete trigger: workflow 587 needs `bbox/face_yolov9c.pt`,
which is not on CivitAI. We want an HF fallback that kicks in *after* CivitAI
resolution misses.

### What was verified LIVE vs. from docs

- **LIVE (curl against the real public HF API, 2026-07-27):** the models search
  endpoint + response shape; that `search=` is repo-name-level and does NOT index
  filenames (`search=face_yolov9c` → `[]`); the `Bingsu/adetailer` siblings +
  recursive tree with LFS/sha256 metadata; the `face_yolov9c.pt` `resolve` HEAD (302
  → Xet CDN) and a real 1-byte range GET (`206`); gated-repo behavior
  (`black-forest-labs/FLUX.1-dev` → `gated:auto`, resolve HEAD `401` unauthenticated);
  existence of several canonical aux-model repos; the live `RateLimit`/
  `RateLimit-Policy` headers.
- **From docs:** the rate-limit *tiers* table (`huggingface.co/docs/hub/rate-limits`)
  and the general API doc pointer. Go-client landscape from web search (not code-audited).

All throwaway curl commands were run ad hoc; no script was left on disk.

---

## 1. Recommended filename → HF-file resolution METHOD

**One-line summary:** HF has **no filename search**, so resolve by (a) a small
**curated filename→repo map** for the common ComfyUI aux families, falling back to
(b) a **two-step search→file-match** (search repos by a cleaned query, then list each
candidate repo's files and require an **exact basename match**), and pin the result
with the file's **LFS `oid` (sha256)** exposed by the tree endpoint.

### 1a. The endpoints (all verified live)

**Search models (repo-level).**
```
GET https://huggingface.co/api/models?search=<q>&limit=<n>&sort=downloads&direction=-1&filter=<tag>
```
Returns a JSON array; each item has real fields:
`id` (e.g. `"Bingsu/adetailer"`), `likes`, `downloads`, `private`, `tags[]`,
`library_name`, `pipeline_tag`, `createdAt`, `modelId`. `sort`/`direction` let us
rank by popularity; `filter=<tag>` (e.g. `ultralytics`) narrows by library/tag.

> **Verified gotcha — search is repo-name/metadata level, NOT filename level.**
> `GET /api/models?search=face_yolov9c` returns `[]`. And
> `search=face_yolo&filter=ultralytics` returns unrelated repos (`iitolstykh/…`,
> `AdamCodd/yolo11n-face-age`) — **not** `Bingsu/adetailer`, because that repo's name
> is "adetailer", not the filename. So you cannot find the right repo by feeding it the
> bare filename; you must know (or guess) the repo, then confirm the file inside it.

**Get one model's info + file list (`siblings`).**
```
GET https://huggingface.co/api/models/{repo}
```
Real fields used: `gated` (`false` | `"auto"` | `"manual"`), `private`, `downloads`,
`likes`, `sha` (current commit), `siblings[]` where each entry is
`{"rfilename": "face_yolov9c.pt"}`. `siblings` gives filenames but **no size/hash**.

**List the file tree WITH hashes (this is the one that gives us a sha256).**
```
GET https://huggingface.co/api/models/{repo}/tree/{revision}?recursive=true
```
Each file entry (verified):
```json
{"type":"file","oid":"92c50bb8…","size":51648019,
 "lfs":{"oid":"d02fe493c31e1bbc6450f4dc6f1db86a02a59322ff1f6d318da0661d72ddd084","size":51648019,"pointerSize":133},
 "xetHash":"7118824c…","path":"face_yolov9c.pt"}
```
- `path` — the file path within the repo (match the workflow's basename here).
- **`lfs.oid` — the git-LFS SHA-256 of the file content.** This is our hash to pin
  against, and it is confirmed to equal the resolve response's `x-linked-etag`
  (`d02fe493…`). Non-LFS (small) files won't have an `lfs` block; only `oid` (a git
  blob sha1) — those we cannot sha256-pin, but the aux models we care about are all LFS.
- `xetHash` is HF's newer Xet chunk-addressed hash — different from sha256; **do not**
  use it as the file sha256. Pin on `lfs.oid`.

**Download (resolve) a file.**
```
GET https://huggingface.co/{repo}/resolve/{revision}/{path}
```
Verified: HEAD returns **302** to a signed CDN URL on **`us.aws.cdn.hf.co`** (the Xet
bridge — NOT the old `cdn-lfs.huggingface.co` the task text assumed), and a real range
GET returns `206` with bytes. Response headers expose `x-linked-size`,
`x-linked-etag` (= sha256), `x-repo-commit`. Use `{revision}` = the concrete commit
`sha` from the model-info call (pin to a commit, not `main`, for reproducibility).

### 1b. The resolution algorithm

1. **Normalize** the workflow ref to a bare basename (`bbox/face_yolov9c.pt` →
   `face_yolov9c.pt`). Keep any parent hint (`bbox/`, `ultralytics/`) — it maps to
   the destination subdir and can bias which family we search.
2. **Curated map first (high precision).** Look the basename up in a small static
   `filename → {repo, subdir}` (or `family-pattern → repo`) table (see §5). On a hit,
   go straight to step 4 for that repo. This is the primary path for known families.
3. **Search fallback (best-effort).** If not in the map, `GET /api/models?search=<cleaned>&sort=downloads&direction=-1&limit=~10` with an optional `filter` inferred from the
   extension/subdir (`.pt` + `bbox/`/`segm/` → `filter=ultralytics`). Cleaning:
   strip extension, split on `_`/`-`, drop pure version tokens. Take the top few
   candidates by downloads.
4. **File-match inside candidate repo(s).** For each candidate repo,
   `GET /api/models/{repo}/tree/{revision}?recursive=true` and require an **exact
   basename match** on `path` (case-sensitive; optionally case-insensitive as a lower-
   confidence tier). Reject the candidate if no exact match.
5. **Rank & pick.** Prefer: curated-map hit > exact filename match in a high-download/
   high-like repo from a recognized org. Capture `{repo, path, revision=sha, lfs.oid,
   size, gated}`.
6. **Gate the action** (see §2/§3): if `gated != false` or confidence is low, **do not
   auto-download** — surface an "Open on HuggingFace" link instead.

### 1c. The verified `face_yolov9c.pt` walkthrough

- `search=face_yolov9c` → **`[]`** (no filename search — must know the repo/family).
- `search=adetailer&sort=downloads` → top hit **`Bingsu/adetailer`**,
  `downloads=10,702,774`, `likes=752`, `gated=false`, `library_name=ultralytics`,
  `license=apache-2.0`.
- `GET /api/models/Bingsu/adetailer` → `siblings` includes `face_yolov9c.pt`;
  `sha=53cc19de382014514d9d4038601d261a7faa9b7b`; `gated=false`.
- `GET /api/models/Bingsu/adetailer/tree/main?recursive=true` → `face_yolov9c.pt`,
  `size=51,648,019`, **`lfs.oid=d02fe493c31e1bbc6450f4dc6f1db86a02a59322ff1f6d318da0661d72ddd084`**.
- `HEAD https://huggingface.co/Bingsu/adetailer/resolve/main/face_yolov9c.pt` →
  **`HTTP/2 302`** → `location: https://us.aws.cdn.hf.co/xet-bridge-us/…`;
  `x-linked-etag: "d02fe493…"` (matches `lfs.oid`); `x-repo-commit: 53cc19de…`.
  A range GET (`-r 0-0 -L`) returned **`HTTP 206`, 1 byte** — the CDN serves content
  unauthenticated. **No auth required.**

**Conclusion: `face_yolov9c.pt` is reliably resolvable on HF.**
Canonical URL (pin to commit for reproducibility):
`https://huggingface.co/Bingsu/adetailer/resolve/53cc19de382014514d9d4038601d261a7faa9b7b/face_yolov9c.pt`
sha256 to verify the downloaded bytes: `d02fe493c31e1bbc6450f4dc6f1db86a02a59322ff1f6d318da0661d72ddd084`.

---

## 2. Recommended integration design (fits the existing flow)

### 2a. Where it hooks in
- **After a CivitAI resolution miss**, not in parallel: the resolver first does its
  existing filename→CivitAI-model→URL attempt; only when that yields no match does it
  call a new `hfresolver`. Keep CivitAI as the primary source (hash-matchable there in
  some cases, richer metadata). HF is strictly a fallback.
- Reuse the existing **basename-equality matching** philosophy — HF's `siblings`/tree
  `path` is exactly a basename compare, so it slots into the same mental model.
- Reuse the existing **per-type subdir writer**: the destination subdir is chosen the
  same way it already is (from the workflow slot / model type), e.g. `ultralytics/bbox/`
  for a detector, `vae/`, `loras/`, etc. HF resolution only supplies `{url, basename,
  optional sha256}`; the writer path logic is unchanged.

### 2b. The SSRF-dialer change (the load-bearing bit)
The civitai SDK's dialer is HTTPS-only, blocks private/loopback/link-local/RFC1918 at
dial time, caps redirects, and attaches the auth token ONLY to `civitai.com` +
subdomains (exact dotted-suffix). The HF fallback must fit this posture **without
weakening the private-IP block**. Required changes, all additive:

1. **Host allowlist for HF egress.** Permit these hosts for the HF path:
   - `huggingface.co` (API + `/resolve` origin)
   - the **Xet/LFS CDN redirect targets** — verified live as
     **`us.aws.cdn.hf.co`**; be general and allow the CDN parent **`*.hf.co`**
     (covers `*.aws.cdn.hf.co`, regional variants) **and** the legacy
     `cdn-lfs.huggingface.co` / `cdn-lfs-us-1.huggingface.co` (older LFS hosts still
     appear for non-Xet repos).
   - Also expect `cas-server.xethub.hf.co` referenced in `Link:` reconstruction
     headers — only needed if you use the Xet client protocol; a plain `/resolve` GET
     that follows the 302 to `*.aws.cdn.hf.co` does NOT need it.
   - **Keep the private-IP block unconditional.** Allowlisting a *hostname* must not
     bypass the post-DNS IP check — the dialer must still reject if that host resolves
     to a private/loopback/link-local address (defense against DNS-rebinding to an
     allowlisted name). i.e. host-allowlist AND IP-block, not OR.
2. **HF-token scoping.** If an HF token is configured, attach it as
   `Authorization: Bearer <hf_token>` **ONLY** to HF hosts via the same exact
   dotted-suffix rule used for civitai (`huggingface.co` and `.hf.co` /
   `.huggingface.co` suffixes). **Never** send the HF token to the civitai host, and
   **never** send the civitai token to HF. On the 302 → CDN redirect, the token must
   be **dropped** (the signed CDN URL is self-authorizing; forwarding a Bearer token
   cross-host is exactly what the existing scoping prevents — keep that behavior).
3. **Redirect cap** stays as-is; the HF path is origin → one CDN redirect, well within
   the existing cap.

### 2c. Auto-download vs. link-only decision
Auto-download only when **all** hold:
- `gated == false` and `private == false` (verified: gated repos 401 the resolve HEAD
  unauthenticated and require accepting terms on the website — cannot be auto-fetched),
- an **exact basename match** was found in the repo,
- confidence is high: curated-map hit, OR search hit that is a recognized/high-
  download repo (see §3 thresholds),
- (recommended) we obtained an `lfs.oid` sha256 to verify the bytes post-download.

Otherwise **degrade to a link**: render "Not on CivitAI — found on HuggingFace:
`{repo}/{path}` — Open on HuggingFace" (scheme-validated external `https://huggingface.co/...`
link, matching the existing Apps click-to-play external-link pattern). Also link-only
when the repo is gated (with a hint: "requires accepting the model's terms on HF").

### 2d. Reuse existing safety rails (unchanged)
Atomic write, size cap, path-containment into `<comfy_model_path>/<subdir>/<basename>`
all apply identically. Add: **verify the downloaded file's sha256 against `lfs.oid`**
when available (HF gives us the hash the workflow never did — use it). On mismatch,
discard. The `--max-file-size` guard already used for civitai downloads applies.

---

## 3. Precision / disambiguation / safety analysis

**Baseline confidence is LOWER than CivitAI**, because (a) HF search can't be fed the
filename, and (b) the workflow gives us no hash to pin the *intended* file — only a
name. Mitigations, in order of strength:

1. **Curated map = highest precision.** For the common aux families, a hand-verified
   `filename/family → canonical repo` table removes search guesswork entirely. This is
   why §5 matters: it converts "search and hope" into "known-good repo, confirm file".
2. **Exact basename match required.** Never accept a fuzzy/substring file match. The
   file must exist verbatim in the repo tree.
3. **Popularity + org as tiebreak.** Real duplicate observed: both **`Bingsu/adetailer`**
   (10.7M downloads, 752 likes) and **`JCTN/adetailer`** (372 downloads, 4 likes)
   contain `face_yolov9c.pt`. Rank by `downloads`/`likes` and prefer the canonical
   high-signal repo. Consider a floor (e.g. require downloads over some threshold, or a
   recognized org) before auto-download; below it → link-only.
4. **Recognized-org allowlist.** Treat a known set of orgs/authors as trusted for
   auto-download (`Bingsu`, `madebyollin`, `h94`, `comfyanonymous`,
   `stabilityai`, `black-forest-labs`, `city96`, `Comfy-Org`, `lllyasviel`, …).
   Matches from unrecognized authors → link-only regardless of exact-name match.
5. **sha256 pin when possible.** Use `lfs.oid` to verify bytes. This defends against
   the file changing under a mutable `main`; pin `{revision}` to the commit `sha`.

**Wrong-match / supply-chain risks (be explicit):**
- **Name collision / typosquat repo.** Anyone can create a repo containing a file with
  a well-known name (`face_yolov9c.pt`) but malicious weights. `.pt`/`.ckpt` are
  **pickle** formats → arbitrary code execution on load inside ComfyUI. This is the
  central risk. Mitigations: curated map / recognized-org gate for auto-download;
  link-only for everything else; surface the repo id + downloads so the user sees what
  they're getting; **do not silently auto-run** a freshly-downloaded pickle from an
  unrecognized source. Consider preferring `.safetensors` variants where a family
  offers them.
- **No hash to pin the *intent*.** We can verify bytes match the repo's advertised
  file (`lfs.oid`), but we can't verify the repo is the one the workflow author meant.
  That gap is inherent and is exactly why link-only is the safe default outside the
  curated/trusted set.
- **Mutable `main`.** A trusted repo could be updated later; pin to the commit `sha`
  captured at resolution time.

**Net recommendation:** auto-download is safe **only** for curated-map/recognized-org +
exact-match + non-gated + sha-verified. Everything else is a link. This keeps the
convenience for the 90% case (adetailer detectors, canonical VAEs/upscalers) while not
turning the resolver into an arbitrary-pickle auto-executor.

---

## 4. Auth, rate limits, ToS, licensing constraints

**Auth / gating (verified live):**
- Public, non-gated repos need **no token** — `face_yolov9c.pt` resolved and served
  bytes fully unauthenticated (`302` → CDN → `206`).
- **Gated repos cannot be auto-downloaded.** `black-forest-labs/FLUX.1-dev` reports
  `gated:"auto"` and its resolve HEAD returns **`401`** without a token; even with a
  token the user must have **accepted the model's terms** on the website first. Detect
  via the model-info `gated` field (`false` = open; `"auto"`/`"manual"` = gated) and
  degrade to link-only with a "requires accepting terms on HF" note.
- An **optional** user-supplied `HF_TOKEN` (a) raises rate limits and (b) enables
  download of gated repos the user has already accepted. It is opt-in; the default flow
  is anonymous. Scope it to HF hosts only (§2b).

**Rate limits (headers verified live; tiers from docs, "Sept '25"):**
- Live headers on the resolve HEAD: `RateLimit-Policy: "fixed window";"resolvers";q=3000;w=300`
  and `RateLimit: "resolvers";r=2999;t=257` — i.e. **3,000 resolver requests / 5-min
  window** for an anonymous IP, matching the docs table.
- Docs tiers (per 5-min window): Anonymous (per IP) — **API 500 / Resolvers 3,000 /
  Pages 100**; Free user (token) — 1,000 / 5,000 / 200; PRO — 2,500 / 12,000 / 400.
- The **API** bucket (search + model-info + tree) is the tighter one (500 anon).
  Since resolution can make 2–3 API calls per missing model, **cache** model-info/tree
  responses and prefer the curated map to avoid search calls. On `429`, HF returns
  `RateLimit`/`RateLimit-Policy` headers with seconds-to-reset — honor them (back off,
  don't hammer).

**ToS / programmatic access:** HF explicitly documents and *encourages* programmatic
access to these endpoints (they ship official Python/JS clients and publish the rate
limits for exactly this use). Downloading public files via `/resolve` is the same
mechanism `transformers`, `vLLM`, LM Studio, ollama, etc. use. No ToS obstacle to a
well-behaved, rate-limit-respecting fallback. Send a descriptive `User-Agent`.

**Per-model licensing:** licenses vary per repo (`cardData.license` / the
`license:*` tag — e.g. `Bingsu/adetailer` is `apache-2.0`; some mirrors are `agpl-3.0`).
Auto-installing a file for the user's own local ComfyUI use is low-risk, but we should
**surface the license** (it's in the model-info response) in the download/confirm UI so
the user sees it. Don't strip/relabel it.

---

## 5. Curated known-repo map (worthwhile — recommended)

**Yes, bootstrap with a curated map.** The live tests prove pure search is unreliable
for filenames (repo name ≠ filename), so a small static table is the difference between
"reliably resolves the common cases" and "best-effort guess". Start with these
(existence/gating spot-checked live where noted). Verify each `lfs.oid`/exact path at
implementation time.

| Family / filename pattern | Canonical repo | Notes (live-checked) |
|---|---|---|
| adetailer/ultralytics detectors: `face_yolov8*.pt`, `face_yolov9c.pt`, `hand_yolov8*.pt`, `hand_yolov9c.pt`, `person_yolov8*-seg.pt`, `deepfashion2_yolov8s-seg.pt` | **`Bingsu/adetailer`** | gated:false, dl 10.7M, apache-2.0. `face_yolov9c.pt` verified end-to-end. |
| SDXL VAE fp16 fix (`sdxl_vae.safetensors` / `sdxl-vae-fp16-fix`) | **`madebyollin/sdxl-vae-fp16-fix`** | exists, gated:false, 12 files. |
| IP-Adapter weights (`ip-adapter*.bin/.safetensors`) | **`h94/IP-Adapter`** (SD/SDXL), `h94/IP-Adapter-FaceID` | exists, gated:false, 30 files. |
| ControlNet v1.1 fp16 (`control_v11*.safetensors`) | **`comfyanonymous/ControlNet-v1-1_fp16_safetensors`** | exists, gated:false, 31 files. |
| CLIP / text encoders (`clip_l.safetensors`, `t5xxl_*.safetensors`) | `comfyanonymous/flux_text_encoders`, `Comfy-Org/*` | verify at impl time. |
| Upscalers (`RealESRGAN_x4plus.pth`, `4x-UltraSharp`, ESRGAN) | `ai-forever/Real-ESRGAN`, `uwg/upscaler`, `Kim2091/*` | community-mirrored; verify + pin. |
| SD1.5/SDXL VAEs (`vae-ft-mse-840000*.safetensors`) | `stabilityai/sd-vae-ft-mse`, `stabilityai/sdxl-vae` | stabilityai official. |

Keep the map small, versioned in-repo, and treat map entries as the **trusted/auto-
download** set. Expand it as real workflow misses surface (data-driven, like the
`face_yolov9c.pt` trigger).

---

## 6. Go integration recommendation

**Recommendation: raw `net/http` against the REST API, reusing the civitai SDK's
hardened transport/dialer — do NOT adopt a third-party Go HF client.**

Rationale:
- The existing Go HF clients (`gomlx/go-huggingface` hub subpackage,
  `seasonjs/hf-hub`, `hupe1980/go-huggingface` — the last is inference-focused, not
  downloads) each bring **their own HTTP client and dialing**, which would **bypass the
  SSRF-hardened dialer**. That is a non-starter given the project's egress posture.
- The surface we need is tiny and stable: three GETs (`/api/models?search=`,
  `/api/models/{repo}`, `/api/models/{repo}/tree/{rev}?recursive=true`) + one
  `/resolve/` download. Hand-rolling these against the existing hardened
  `http.Client` is less code than adapting a client's transport, and keeps ALL egress
  on the audited dialer.
- JSON shapes are small and known (documented with real field names in §1a) — decode
  into purpose-built structs.

So: add an `internal/hf` (or `internal/huggingface`) package mirroring the shape of
`internal/civitai` — a thin wrapper (`SearchModels`, `GetModel`, `GetTree`,
`ResolveURL`) over the shared hardened client, plus the curated map. This matches the
existing "thin wrapper over an SDK/API + path helpers" architecture.

---

## 7. Egress / privacy note

Sending an untrusted workflow **filename** to `huggingface.co` is **consistent with the
existing CivitAI egress** — the scan flow already sends file SHA256s to civitai.com,
and the resolver already sends cleaned filenames as CivitAI search queries. HF egress
is server-side (like the CivitAI calls); the offline/no-CDN invariant applies to the
*UI assets*, not to intentional server-side model resolution.

New privacy surface to disclose to the user (one line, same spirit as the
`match_remote` opt-out): "When a model isn't found on CivitAI, its filename is sent to
huggingface.co to look for a fallback download." Recommend making the HF fallback a
**setting** (default on or off is a product call — given it's a new external egress,
defaulting **on but clearly disclosed**, mirroring `match_remote`, is defensible) and
honoring an opt-out. If an `HF_TOKEN` is configured, note it is sent **only** to HF
hosts.

---

## 8. Open questions & recommended scope

**MVP (first implementation):**
1. `internal/hf` thin client (search / model-info / tree / resolve-URL) on the shared
   hardened http.Client.
2. SSRF-dialer additive change: allow `huggingface.co` + `*.hf.co` +
   `cdn-lfs*.huggingface.co`; HF-token scoping to HF hosts; **keep private-IP block
   unconditional**. This is the security-sensitive change → full `/audit-pr`.
3. Curated map (§5) as the trusted auto-download set; exact-basename confirm in the
   repo tree; sha256 verify via `lfs.oid`; pin to commit `sha`.
4. Hook after CivitAI miss; **auto-download only for curated/recognized-org + non-gated
   + exact-match + sha-verified; everything else → "Open on HuggingFace" link.**
5. Gated detection (`gated != false` → link-only). Optional `HF_TOKEN` setting.
6. Privacy disclosure + opt-out setting.

**Defer (later):**
- General search-fallback auto-download for non-curated repos (start link-only; graduate
  to auto only behind the recognized-org gate once trust heuristics are proven).
- Xet-protocol chunked downloads (`cas-server.xethub.hf.co`) — unnecessary; the plain
  `/resolve` 302→CDN path serves whole files fine.
- Datasets/Spaces resolution (only models needed here).

**Open questions:**
- Default-on vs default-off for the HF fallback egress (product call).
- How aggressively to trust `downloads`/`likes` thresholds for auto-download vs. a
  hard recognized-org allowlist only.
- Whether to prefer `.safetensors` over `.pt`/`.ckpt` variants when a family offers
  both (pickle-safety) — recommend yes where a same-family safetensors exists.
- Handling repos whose file lives in a subdir path (e.g. `models/foo.safetensors`) —
  the tree `path` carries the subdir; match on basename but download the full `path`.

---

### Appendix — reproduction commands (throwaway; not committed)
```
# search is repo-name-level, not filename-level:
curl -s 'https://huggingface.co/api/models?search=face_yolov9c&limit=10'      # -> []
curl -s 'https://huggingface.co/api/models?search=adetailer&sort=downloads&direction=-1&limit=10'
# file list + sha256 (lfs.oid):
curl -s 'https://huggingface.co/api/models/Bingsu/adetailer'                  # siblings + gated + sha
curl -s 'https://huggingface.co/api/models/Bingsu/adetailer/tree/main?recursive=true'
# download URL is real, unauthenticated, 302 -> Xet CDN:
curl -sI 'https://huggingface.co/Bingsu/adetailer/resolve/main/face_yolov9c.pt'   # HTTP/2 302 -> us.aws.cdn.hf.co
curl -sL -r 0-0 -o /dev/null -w '%{http_code}\n' \
  'https://huggingface.co/Bingsu/adetailer/resolve/main/face_yolov9c.pt'          # 206
# gated repo blocks anon download:
curl -sI 'https://huggingface.co/black-forest-labs/FLUX.1-dev/resolve/main/flux1-dev.safetensors'  # HTTP/2 401
```
