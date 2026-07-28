# Security Audit — civitai-manager v0.1.64 (holistic / cross-cutting)

**Date:** 2026-07-27 · **Commit:** `d38b671` (main @ tag `v0.1.64`) · **Auditor:** read-only adversarial pass
**Build gate (grounding):** `GOPRIVATE=github.com/civitai/* go build ./... && go vet ./... && go test ./...` — **all green** (build exit 0, vet exit 0, every package `ok`). Claims below are traced against the built tree.

## Executive summary

The app is a single-binary, loopback-by-default, single-user local library manager with a server-rendered gomponents+htmx UI. This was a whole-system review of the cumulative attack surface (per-feature audits already happened as features shipped). **The core security controls are strong and consistently applied:** CSRF is verified in constant time before every state-changing side effect; the HuggingFace client has a genuinely well-built from-scratch SSRF dialer (dial-time IP block on the resolved address + host allowlist + token-drop on the CDN hop, AND-composed); path containment (`SafeModelDest` + atomic sha-verified writes) is layered and defense-in-depth; auto-download eligibility is tightly gated; ZIP import has zip-bomb caps and never executes contents; untrusted strings render through `g.Text` and the one `g.Raw` sink is bluemonday-sanitized; secrets are redacted and host-scoped.

**No confirmed 🔴 critical findings.** Three 🟡 should-fix items surfaced, all cross-cutting (exactly the class a per-PR lens misses): an **unbounded (depth-only) subgraph expansion** that allows exponential CPU/memory blow-up on running a hostile workflow; the **CivitAI download path delegates all SSRF/token-scope containment to the opaque private SDK** with no app-level URL guard (the HF path got a hardened dialer precisely because the SDK couldn't be trusted/imported — the CivitAI path has no equivalent); and a **non-loopback bind turns every state-changing/egress endpoint into an unauthenticated surface** because CSRF is not an auth boundary.

| Severity | Count |
|---|---|
| 🔴 Critical | 0 |
| 🟡 Should-fix | 3 |
| 🟢 Nit / defense-in-depth | 3 |

---

## 🟡 Should-fix findings

### 🟡-1 — Exponential subgraph expansion: CPU/memory DoS on running a hostile workflow — **CONFIRMED (code-traced)**

**Where:** `internal/comfy/convert_subgraph.go:138` (`sgExpander.expand`), reached from `ConvertUIToAPI` → `flattenSubgraphs` (`:86`). Depth bound `maxSubgraphDepth = maxResolveDepth = 64` (`convert.go:58`).

**What:** `expand()` bounds only recursion **DEPTH**, never the total number of emitted nodes. For an interior node whose type is itself a subgraph definition it recurses `e.expand(nestedDef, …, depth+1)` (`:181`) and appends every non-boundary interior node to `e.outNodes` (`:210`). A `definitions.subgraphs[]` entry that is self-referential (or a short mutual cycle) where each instance contains **≥2 child instances** fans out to ~2^depth ≈ 2^64 recursive calls and clone allocations before the depth guard trips at each leaf.

**Exploit:** import a crafted UI-format workflow (via `POST /workflows/import` JSON paste, `POST /workflows/import-png`, or `POST /workflows/discover/{id}/import` from a CivitAI Workflows model) whose `definitions.subgraphs` contains one self-referential subgraph with two self-typed interior nodes → click **Run**. `realRun` calls `comfy.ConvertUIToAPI` (`run_handlers.go:269`).

**Why the existing guards don't save it:**
- `ConvertUIToAPI`/`flattenSubgraphs`/`expand` take **no `context.Context`** and never check `ctx.Err()`, so the 30-minute `runJobBudget` (a context timeout) cannot interrupt the pure-CPU recursion.
- The per-run `recover()` in `startRun`/`startDownloadAndRun` (`run_handlers.go:171`) catches a *panic*, not a multi-minute CPU spin or an OOM (OOM kills the whole process).

**Impact:** the single run goroutine hangs indefinitely and allocates without bound → server process hang / OOM crash. Blast radius is the user's own instance (loopback, single-user), but a workflow shared on CivitAI and imported via Discover is a realistic hostile carrier — this is exactly threat-model (d) "untrusted imported ComfyUI graphs."

**Fix:** cap the **total emitted node count** in `sgExpander` (e.g. abort with a warning once `len(e.outNodes)` crosses a few thousand — real graphs are hundreds of nodes), and/or thread a `context.Context` into `ConvertUIToAPI` and check `ctx.Err()` periodically inside `expand`. The node-count cap is the deterministic fix and is preferable.

---

### 🟡-2 — CivitAI download URLs are used verbatim from API responses with no app-level scheme/host guard; SSRF + token-scope containment is delegated entirely to the opaque private SDK — **PLAUSIBLE (SDK not auditable here)**

**Where:** `internal/web/run_download.go:462/489` (`pickFileFromModelRaw`/`pickFile` return `f.DownloadURL` verbatim), `:565` (`downloadModelFile` → `s.downloader().DownloadFile(ctx, pd.URL)`); `internal/web/discover_workflow_import.go:170/265` (`archiveFiles` → `fetchBounded` → `dl.DownloadFile(ctx, file.DownloadURL)`). `s.downloader()` is `civitai.New(...)` = the private `github.com/civitai/cli` SDK (`internal/civitai/civitai.go:103`).

**What:** every CivitAI download URL comes straight from a CivitAI API response's `downloadUrl` field and is handed to the SDK client **without any app-level validation** — no `https`-only check, no civitai-host allowlist, no private-IP block. The only place a URL is parsed in this file is `comfyURL` for loopback detection (`:27`); the download URL itself is never inspected. The entire SSRF/token-leak containment therefore rests on the SDK's dialer.

**Why this is the crown-jewel concern:** the HuggingFace client was given a purpose-built hardened dialer (see `internal/hf/client.go`) *specifically because* "the civitai SDK's dialer … lives in an unexported package and cannot be imported here — so it is reconstructed." That asymmetry means the CivitAI download path — carrying an equally-untrusted API-supplied URL **and the CivitAI bearer token** — relies on an SDK guard this codebase can neither import nor verify. If the SDK does not (a) block private/link-local/RFC1918 dial targets and (b) scope the `Authorization` token to civitai hosts only, then a malicious or compromised CivitAI API response returning `downloadUrl: "http://192.168.50.x/…"` (or any off-host target) yields **SSRF into the homelab** and/or **leak of the CivitAI token** to that host. This is threat-model (b), the highest-value risk.

**Impact if the SDK guard is weak/absent:** blind (or semi-blind) SSRF to internal hosts + CivitAI token exfiltration. Requires a hostile/compromised CivitAI response or DNS control, and a user action (download / download-and-run / workflow import), all of which are in-model.

**Fix (defense-in-depth, independent of the SDK):** add an app-level check on every API-supplied download URL before handing it to the SDK — `https`-only, host must be an exact dotted-suffix of the civitai download domains — mirroring `hf.newRequest`/`hostAllowed`. Cheap, deterministic, and removes the dependence on an unauditable third-party dialer. Independently, confirm with the SDK maintainer that its dialer blocks private IPs and host-scopes the token.

---

### 🟡-3 — Non-loopback bind exposes all state-changing/egress endpoints unauthenticated; CSRF is not an auth boundary — **CONFIRMED (design)**

**Where:** `internal/web/server.go:326` (`extraPathsAllowed` = `config.IsLoopbackAddr(Addr)`), `internal/web/discover_handlers.go:38` (`gate`). Default bind is loopback (`config.go:27` `127.0.0.1:8787`) but `Addr` is user-configurable to a LAN/`0.0.0.0` address.

**What:** `gate()` disables only the **arbitrary-PATH** endpoints (discover / browse / scan-dir / scan / workflow-run family) on a non-loopback bind. The state-changing and egress endpoints that take **no path** are *not* gated and are protected only by CSRF:

- `POST /models/{id}/download` — egress + writes a file into the model root (verified `csrf=1 gate=0`).
- `POST /library/quarantine` — **destructive**: moves the user's model files into the trash dir (`csrf=1`, no gate).
- `POST /trash/{id}/restore`, `POST /subscribe`, `POST /models/{id}/subscribe|unsubscribe`, `POST /subscriptions/{id}/delete|flags` — state changes.

CSRF defends against a *cross-site* forgery from a page the attacker doesn't control, but the per-process token is embedded in **every served page** (in `hx-vals` / forms) and is **same-origin readable**. On a non-loopback bind there is no login, so any LAN peer who can load a page scrapes the token and can then drive quarantine (destructive file moves), downloads (egress + disk fill), and subscription changes. The token is an anti-CSRF nonce, not an authentication credential.

**Impact:** on a LAN/`0.0.0.0` bind the whole UI is effectively unauthenticated; the worst primitives are destructive file moves and attacker-directed downloads. (The arbitrary-path read primitive *is* correctly gated off — that part holds.)

**Fix:** for a non-loopback bind, either (a) refuse to start without an auth layer (bearer/basic-auth), or (b) extend the gate to the state-changing/egress endpoints too, or (c) at minimum emit a loud startup warning documenting that a non-loopback bind is unauthenticated. Deterministic option: bind-address-conditional auth middleware.

---

## 🟢 Nits / defense-in-depth

- **🟢-1 `AppsClient` uses a plain `http.Client` with no hardened dialer.** `internal/civitai/apps.go:140` — `&http.Client{Timeout: 20s}`, no private-IP block. The request URL is `baseURL + "/api/v1/apps"` with `baseURL` operator-configured (default civitai.com), so low risk, but a redirect off civitai.com is followed with no IP guard (Go's stdlib does strip `Authorization` on a cross-host redirect, so no token leak). Reuse a hardened transport for parity with the HF client.
- **🟢-2 Unescaped `repo` interpolation in HF API path.** `internal/hf/api.go:110/120` build `c.base + "/api/models/" + repo` with `repo` (from HF search results) not path-escaped, unlike `resolveURL` (`resolve.go:340`) which escapes every segment. The dial-time host allowlist makes host substitution impossible, so worst case is a malformed path (a miss); tidy up for consistency.
- **🟢-3 `handleQuarantine` has no `gate()` call.** Correct in isolation (it takes candidate IDs, not a path) — noted only because it is one of the destructive endpoints reachable on a non-loopback bind (see 🟡-3).

---

## Controls verified to HOLD (with enforcing lines)

- **CSRF everywhere, constant-time, before side effects.** Token is 32 bytes from `crypto/rand` (`server.go:371`); compared with `subtle.ConstantTimeCompare` (`server.go:389`). Every POST handler traced verifies CSRF; the `ParseForm → verifyCSRF → gate` order holds (e.g. `run_download.go:110-119`, `discover_workflow_import.go:51-62` — CSRF *before* the token-authed egress). Swept 17 POST handlers: all `csrf=1`.
- **HF SSRF dialer.** `internal/hf/client.go`: dial-time `Control` block on the **resolved** IP (`:123`, DNS-rebind resistant), `isBlockedIP` covers loopback/unspecified/link-local incl. 169.254.169.254/RFC1918/ULA/CGNAT/multicast/0.0.0.0-8 for v4 and v4-mapped v6 (`:193`), host allowlist as exact dotted-suffix (`:163`), https-only + host-allowlist + token-drop on **every** redirect hop (`:141`), token attached only to `huggingface.co` (`:184`). Host-allowlist AND post-DNS IP-block both apply (never OR).
- **Path containment + safe writes.** `SafeModelDest` (`download_target.go:72`) uses basename-only, normalizes `\`/`/`, rejects abs/`..` subdir, and double-checks with `filepath.Rel`. `WriteModelStreamVerified` (`:140`) writes to a same-dir temp, enforces the size cap independently of a lying Content-Length (`:183` cap+1 bound), sha-verifies **before** the atomic rename (`:202`), refuses to overwrite (`:150`), removes the temp on any error.
- **Auto-download trust gates.** `Match.AutoDownloadEligible` (`hf/resolve.go:61`) requires (curated OR recognized-org) AND non-gated AND a determinable subdir AND a captured sha256; the HF download path always pins that sha (`run_download.go:585`). CivitAI path has no hash to pin (documented, `run_download.go:364-378`) and trusts CivitAI's ranking + exact-basename — same trust as every other download in the app.
- **ZIP import.** Entry-count / per-entry / cumulative caps (`discover_workflow_import.go:25-36`, enforced `:194/:210/:215`), only `.json` entries parsed, contents never executed, `readZipEntry` uses `LimitReader(cap+1)` so a lying header can't exceed the cap (`:294`).
- **XSS / output encoding.** Untrusted model/file/repo/choice/comfy-error strings all render via `g.Text`. The only `g.Raw` sink is the model description, sanitized by bluemonday `UGCPolicy` (`sanitize.go:14`). External links are scheme-validated: apps `isSafeHTTPURL` (http/https + host, `discover_apps.go:341`) and HF `hfOpenLink` (`hf_fallback.go:160`, forces an `https://huggingface.co/` prefix). The `/view` image proxy constrains non-`image/*` to `application/octet-stream` + `X-Content-Type-Options: nosniff` and serves only exact members of the active run's output set (`run_handlers.go:540-578`). No external CDN/script/style references (offline invariant preserved).
- **NSFW `hide` omits server-side.** `model_card_pages.go:655` (`if nsfw && mode == NSFWHide { continue }`) and `model_community_pages.go:86` skip the content entirely; search/list requests set the SFW-only flag (`handlers.go:25`).
- **Secrets.** `RedactToken` keeps only last 4 chars; `Config.Redacted()/String()` redact Token/ComfyToken/HFToken (`config.go:617-644`). No token/URL/header logged — only the request **path** at Debug level (`server.go:505`). HF token host-scoped; config never serializes raw secrets.
- **Resource bounds.** Resolve/popular caches are TTL'd and size-capped (`resolveCacheMax`, `run_resolve.go`); HF JSON bodies capped at 8 MiB (`hf/http.go:16`), apps body at 4 MiB (`apps.go:126`); PNG upload uses `MaxBytesReader` + multipart temp cleanup (`workflow_handlers.go:120-132`); streaming jobs snapshot under the mutex and cancel on shutdown. (The one unbounded compute path is 🟡-1.)

---

## Per-endpoint gating table

Legend: **CSRF** = verifies token before side effect · **Gate** = loopback-gated arbitrary-path guard · **Egress** = makes outbound network calls · **FS-write** = writes/moves local files.

| Route | Method | CSRF | Gate | Egress | FS-write | Notes |
|---|---|---|---|---|---|---|
| `/{$}`, `/search`, `/models/{id}`, `/creators/{username}` | GET | — | — | ✓(read) | — | Read-only outbound proxy GETs (search/detail). |
| `/models/{id}/{title,version-status,card-images,community,subscribe-*}` | GET | — | — | ✓(read) | — | Fragments; community/card-images fetch CivitAI. |
| `/models/{id}/download` | POST | ✓ | ✗ | ✓ | ✓ | Enqueues real download → model root. Reachable on non-loopback (🟡-3). |
| `/models/{id}/subscribe`,`/unsubscribe`, `/subscribe`, `/subscriptions/{id}/{flags,delete}` | POST | ✓ | ✗ | — | — | DB state. Reachable on non-loopback (🟡-3). |
| `/settings/{nsfw,theme,match-remote}` | POST | ✓ | ✗ | — | — | DB settings. |
| `/library` | GET | — | — | — | — | Page. |
| `/library/scan`, `/library/discover` | POST | ✓ | ✓ | ✓(scan match) | — | Arbitrary-path walk → gated. |
| `/library/{scan,discover}/status` | GET | — | — | — | — | Poll (loopback surface). |
| `/library/{scan,discover}/stop` | POST | ✓ | ✓ | — | — | |
| `/library/browse` | POST | ✓ | ✓ | — | — | Containment: `BlockedForBrowse` + `BrowseAllowed` on symlink-resolved real path. |
| `/library/scan-dirs/{add,remove}` | POST | ✓ | ✓ | — | — | `validateScanDir`. |
| `/library/quarantine` | POST | ✓ | ✗ | — | ✓(move) | **Destructive** move to trash. IDs not paths. Reachable on non-loopback (🟡-3). |
| `/trash/{id}/restore` | POST | ✓ | ✗ | — | ✓(move) | Reversible restore. |
| `/library/workflow-scan[/status,/stop]` | POST/GET | ✓* | ✓* | — | — | *POSTs CSRF+gate; status GET ungated. |
| `/apps/discover` | GET | — | — | ✓(read) | — | Read-only catalog; play URLs scheme-validated in-page (not server-fetched). |
| `/workflows`, `/workflows/{id}`, `/workflows/discover` | GET | — | — | ✓(discover) | — | Pages/fragments. |
| `/workflows/discover/{modelId}/import` | POST | ✓ | ✓ | ✓ | — | Token-authed zip download; CSRF+gate **before** egress; zip-bomb caps. |
| `/workflows/import`, `/import-png` | POST | ✓ | ✓ | — | — | Parses untrusted graph/PNG (stored; converted only at run — 🟡-1). |
| `/workflows/{id}/{delete,attach,golden}` | POST | ✓ | ✗ | — | — | DB state. |
| `/workflows/{id}/run`, `/run-substitute`, `/run-with-options`, `/install-option-and-run`, `/download-and-run` | POST | ✓ | ✓ | ✓(runs+dl) | ✓(dl-run) | Reaches ComfyUI + civitai/HF + FS. Path guards in `SafeModelDest`; convert DoS 🟡-1. |
| `/workflows/{id}/run/{comfy-status,status}`, `/run/view`, `/run/resolve-model` | GET | — | ✓(view/resolve/status) | ✓(view/resolve) | — | `/view` constrained to active-run outputs; `/resolve-model` gated + TTL-cached. |
| `/workflows/run/stop` | POST | ✓ | ✓ | — | — | |
| `/workflows/{id}/cloud[/whatif,/run]`, `/cloud/{status,stop}` | POST/GET | ✓* | ✓* | ✓ | — | Sends graph+token to civitai.com (opt-in, default off). |

---

## Verdict

**Ship-safe on the default loopback bind.** The security architecture is coherent and the invariants (CSRF-before-side-effect, loopback-gating of arbitrary-path primitives, path containment, sha-verified auto-downloads, HF SSRF hardening, output escaping, secret redaction, NSFW server-side omission) are consistently enforced — verified against real code, with a green build/vet/test gate. No 🔴.

Before this can be considered safe on a **non-loopback** bind, address **🟡-3** (add auth or gate the state-changing/egress endpoints). Independently, **🟡-1** (bound subgraph node count) closes a self-inflictable-but-hostile-triggerable DoS, and **🟡-2** (app-level URL allowlist on CivitAI download URLs) removes the app's dependence on an unauditable third-party dialer for its crown-jewel SSRF/token-leak defense — both are cheap deterministic fixes and both are the kind of cross-cutting gap a per-PR review structurally cannot catch.

**Explicitly inferred vs verified:** 🟡-1 and 🟡-3 are CONFIRMED by code trace. 🟡-2 is PLAUSIBLE and hinges on the private `civitai/cli` SDK's dialer, which is out of this repo and was **not** auditable here — the finding is about the *absence of an app-level guard*, which is verified; the exploitability depends on the SDK's behavior, which is not.
