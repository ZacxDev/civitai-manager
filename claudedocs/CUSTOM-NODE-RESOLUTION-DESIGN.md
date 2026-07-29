# Custom-node resolution & gated install — design

Closes the last dependency gap in "click Run and it just works": when a workflow
needs a ComfyUI **custom node** that is not installed, name it, attribute it to a
node pack, and offer a gated install.

Source ticket: clawgate task #74. Related: `COMFYUI-INTEGRATION-DESIGN.md`
(cloud/CustomComfy is a SEPARATE, larger piece — local-first is the valuable half
and does not wait on it).

**Status: design. Not yet implemented.**

---

## The decision that shaped this

The ticket's step 4 proposed `git clone` into `custom_nodes/` + `pip install` into
ComfyUI's venv, reusing the `comfyext` safety discipline. **We are not doing
that.** Recon found **ComfyUI-Manager V3.41 already installed** in the target
ComfyUI with its full HTTP API live on loopback. Delegating to it removes the
entire highest-risk half of the ticket:

| | clone + pip ourselves | delegate to Manager |
|---|---|---|
| Who executes third-party code | **us** | Manager (already trusted by the user) |
| venv resolution | **we must get it right** (this repo already ate one `$PWD`-anchoring outage) | not our problem |
| Writes into `custom_nodes/` | **ours** | Manager's |
| Security policy | **we invent one** | Manager's own `security_level`, already configured |
| Uninstall / restart | **we build both** | Manager already has both |

Trade-off, stated honestly: **one-click install becomes conditional on
ComfyUI-Manager being present.** Detection, attribution, and copy-the-command stay
unconditional, so a user without Manager still gets the ticket's third "Done when"
bullet (told exactly what to install by hand). Even delegated, this still ends in
third-party code running in the user's ComfyUI — lower blast radius, not low. Full
`/audit-pr`.

---

## Ground truth from recon (2026-07-29, live)

Measured against the real local ComfyUI 0.27.1 + Manager V3.41 at `127.0.0.1:8188`,
and the real workflow **wf581 "WAN 2.2 Smooth Workflow v6.0"**. These numbers are
why the naive happy path would have shipped broken.

### Manager's two indexes and how they join

- `GET /api/customnode/getmappings?mode=cache` — 1.4 MB, **41,670 node classes**.
  Shape: `{ <key>: [ [class names...], {title_aux} ] }`.
- `GET /api/customnode/getlist?mode=cache` — 4.5 MB.
  Shape: `{ channel, node_packs: { <pack_id>: {id, version, cnr_latest, files[],
  repository, reference, title, state, trust, install_type} } }`, **7358 packs**.

**The join is NOT simply key-to-key.** Of `getmappings`' 5573 keys, **2389 (43%)
are raw GitHub URLs rather than pack ids.** A pack-id-only join silently misses
those — it fails as a "we don't know this node" false negative, not as an error.

> Required: join by pack id **first**, then fall back to matching the URL key
> against each pack's `repository` and `files[]`. Verified: the URL key
> `https://github.com/GACLove/ComfyUI-VFI` resolves to pack `ComfyUI-VFI` only via
> the URL join.

### Attribution is partial in the real world

Missing classes for wf581 (raw UI-graph diff vs live `/object_info`) and what
attribution actually returns:

```
CR Float To Integer        -> ComfyUI_Comfyroll_CustomNodes   (nightly-only)
Pick From Batch (mtb)      -> comfy-mtb
RIFEInterpolation          -> https://github.com/GACLove/ComfyUI-VFI  (URL key)
MMAudioSampler             -> NOT FOUND
MMAudioModelLoader         -> NOT FOUND
Note Plus (mtb)            -> NOT FOUND
Label (rgthree)            -> NOT FOUND
```

**4 of 7 unresolved.** "We could not attribute this node type" is a **first-class
UI state**, not an error path. It must name the class and offer a search link.

### Not every pack is auto-installable

**1178 of 7358 packs (16%) are nightly-only** (`cnr_latest == "nightly"`). Those
route to Manager's git-url path, which at Manager's defaults
(`security_level='normal'`, `allow_git_url_install=False`) is **refused**.

**Comfyroll — the pack wf581 needs — is one of them.** So the blocked path is what
the headline verifier hits. It is the primary UX case, not an afterthought: it must
name the pack, the repo URL, the exact manual command, and the Manager setting that
would permit it.

### Noise that must never reach attribution

The raw wf581 diff also contains **9 UUID keys** (ComfyUI *subgraph definitions*)
and rgthree **UI-only** nodes (`Label (rgthree)`, `Fast Groups Bypasser (rgthree)`).
Conversion already expands subgraphs and drops UI-only nodes.

> Required: attribution runs on the **converted API graph**, via the existing
> `PreflightReport.MissingNodes` — **never** on the raw UI graph. A test must pin
> that subgraph UUIDs and rgthree UI-only classes do not appear.

---

## Shape

### 1. Detect — already built

`comfy.Preflight` (`internal/comfy/preflight.go`) already computes
`PreflightReport.MissingNodes`: every `class_type` in the converted graph absent
from live `/object_info`. `run_pages.go` already renders it as a flat
`missingList("Missing custom nodes", …)`. **No change to detection.** The gap is
attribution and an action.

### 2. Attribute — new, unconditional

New `internal/comfy/nodepack.go` (pure, testable, no I/O):

```go
// Pack is a node pack that provides one or more missing class_types.
type Pack struct {
    ID          string   // Manager pack id ("" when only a URL key resolved)
    Title       string   // human label (title_aux, else id)
    Repository  string   // canonical repo URL, shown to the user BEFORE any install
    Version     string   // cnr_latest
    Installable bool     // CNR-released AND policy-permitted
    Reason      string   // why not, when !Installable
    Classes     []string // the missing classes this pack provides
}

// NodePackIndex is the joined class -> pack index.
type NodePackIndex struct { /* … */ }

func BuildIndex(mappings, getlist json.RawMessage) (*NodePackIndex, error)
func (ix *NodePackIndex) Attribute(missing []string) (packs []Pack, unattributed []string)
```

`Attribute` returns **both** the resolved packs and the classes it could not place.
Callers must render both.

**Index sources — MERGED, not ranked.** Live-measured against wf581's 7 real
missing classes:

| class | static map | Registry |
|---|---|---|
| `CR Float To Integer` | ✅ Comfyroll | ❌ |
| `RIFEInterpolation` | ✅ ComfyUI-VFI *(URL key)* | ❌ |
| `Pick From Batch (mtb)` | ✅ comfy-mtb | ✅ comfy-mtb |
| `MMAudioSampler` | ❌ | ✅ comfyui-mmaudio |
| `MMAudioModelLoader` | ❌ | ✅ comfyui-mmaudio |
| `MMAudioFeatureUtilsLoader` | ❌ | ✅ comfyui-mmaudio |
| `Note Plus (mtb)` | ❌ | ❌ |

**Static map alone 3/7 · Registry alone 4/7 · union 6/7.** They are
**complementary**, so "Registry first, map second" as a strict ranking is wrong —
query both and merge, recording which source backed each attribution so the UI can
label confidence.

1. **Manager's loopback endpoints** when Manager is present (no egress at all):
   `getmappings` + `getlist`, joined two-way (pack id, then URL).
2. **Comfy Registry** (first-party): `GET https://api.comfy.org/comfy-nodes/{class}/node`
   → `{id, name, …}`. Verified live. ~334k node classes across ~4.9k packs, ~8× the
   static map. Not-found returns a `message` body, not an error status shape.
3. **`extension-node-map.json`** as the no-Manager static source. Canonical URL is
   the **`recent` channel path**, which both the V3 and V4 channel configs point at:
   `https://raw.githubusercontent.com/Comfy-Org/ComfyUI-Manager/main/node_db/new/extension-node-map.json`
   ⚠ `node_db/legacy/` and `node_db/forked/` return **`{}` with HTTP 200** — an
   empty-but-successful answer that reads as "no packs found". Never use those paths.
   ⚠ The `manager-v4` branch's in-repo copies are ~8 months stale; V4 fetches from
   `main` at runtime. Never read the V4 in-repo file.

Egress (2) and (3) go through a **new SSRF-hardened client modelled directly on
`internal/hf/client.go`** (https-only incl. every redirect hop, dial-time
private/loopback/link-local/ULA/CGNAT block, host allowlist — `api.comfy.org` +
`raw.githubusercontent.com` only, bounded body). Cached in SQLite with a TTL — new
migration **`0013_nodepack_cache.sql`**, mirroring `0007_model_cache` /
`0010_community_cache` (`raw` BLOB + `fetched_at`; serve-fresh → fetch → fall back
to stale on failure).

This is new outbound egress to two new hosts. It is read-only and must be disclosed
the way `match_remote` is.

**Ambiguity is real:** one class can be claimed by many packs (`PreViewVideo` by 16,
`LoadVideo` by 12). Attribution must therefore be **one class → N candidate packs**,
not a single answer, and the UI must not silently pick one.

### The attribution ladder (live-measured: 7/7 on wf581)

Apply in order, recording which rung produced each hit so the UI can label
confidence:

1. **Exact enumerated class match** in `getmappings` (highest confidence).
2. **Registry lookup** — `GET /comfy-nodes/{class}/node`, URL-escaped path segment
   (`CR Float To Integer` → `CR%20Float%20To%20Integer`, `+` → `%2B`, case-sensitive).
   No batch endpoint exists, so it is **N requests**; cap concurrency 4–8 and cache.
   Not-found is `{"error":"","message":"No node found…"}`.
3. **`nodename_pattern` regex** (lowest confidence). 38 entries in the live
   `getmappings` response carry a regex *instead of* an enumerated class list. This
   rung is what resolves `Note Plus (mtb)` → `comfy-mtb` (via `\(mtb\)$`) — the class
   that rungs 1 and 2 both miss. **Only the live endpoint applies these; the raw
   `extension-node-map.json` file does not carry them.**

> 🔴 **Catch-all guard — mandatory.** The index contains at least one pattern of `.*`
> (`https://github.com/DemonGatanjieu/Anomalous_Model_Browser`) which matches **every
> class name**, verified against a nonsense control (`ZZZ_unrelated_class_9f3` → matched).
> Applied naively, every unresolvable node in every workflow gets confidently
> attributed to that one pack — worse than saying "unattributed".
> **Guard empirically, not by blacklisting `.*`:** probe each compiled pattern with a
> synthetic nonsense class name and discard any pattern that matches it. That stays
> correct when the next catch-all appears. A test must pin this.

Rung 3 results must be visibly weaker in the UI than rungs 1–2 ("likely provided
by…"), never presented as certain.

### 3. Offer — new UI

Replace the flat list at `run_pages.go:320-322` with `missingNodesPanel`, following
the established `missingModelsPanel` / `incompatibleOptionsSection` precedents (a
terminal, poller-free fragment; every untrusted string escaped via `g.Text`).

Per pack: title, **repo URL rendered as visible text** (a scheme-validated external
link at most — never a bare untrusted `href`), the classes it provides, and one of:
- **Install** button (CSRF, gated) when `Installable`;
- **why not + the exact manual command** when not.

Plus an "Unattributed node types" section listing the classes with a search link.
Always show a copy-able manual command — that is the no-Manager and blocked answer.

### 4. Install — gated, delegated

`POST /workflows/{id}/nodepacks/install` (CSRF + loopback-gated, like every other
path-touching endpoint). Per-pack explicit confirmation showing the repo URL. No
transitive installs, no "install all" default.

**Manager is not one API — it is three targets. Probe, never assume.**

| Manager line | install endpoint |
|---|---|
| **V3** (installed here: V3.41) | `POST /manager/queue/install` → `POST /manager/queue/start` |
| **V4** default (`comfyui_manager/glob/`) | `POST /v2/manager/queue/task` |
| **V4** `--enable-manager-legacy-ui` | `POST /v2/manager/queue/batch` |

Neither V4 module registers both paths, and V4's `4.2.1` consolidated the
per-operation routes specifically breaking third-party callers. So the client
**probes for a live endpoint and degrades to attribution-only** when none matches —
it must never hard-code one line.

**Presence detection** (in order, all three required to claim "installable"):
1. ComfyUI server feature flag `extension.manager.supports_csrf_post` — the
   sanctioned capability negotiation. Absent → assume V3-era semantics.
2. A live endpoint probe. ⚠ Manager's **CLI-only mode** loads the package with the
   **internal web API disabled** — so "the directory exists" and even "the module
   loaded" are false positives. Only a live endpoint response counts.
3. `getlist` says the pack is CNR-released (`cnr_latest != "nightly"`).

⚠ `GET /customnode/getlist` **KeyErrors without the `mode` query param** (upstream
issue #2740) — always send `mode=cache`.

Then poll status until idle, **offer** (never perform automatically)
`POST /manager/reboot`, and state plainly that ComfyUI must restart before the
workflow can run.

### Reading the install result — NO websocket required

On **V3** (installed here), `nodepack_result` is **destroyed immediately after the
`cm-queue-status` websocket broadcast and is never exposed over REST**
(`manager_server.py:709-720`). `GET /manager/queue/status` returns counts only — and
`is_processing == false` is *already true before you start*, so a naive poll exits
instantly and reports success for work that never ran.

**We still do not open a websocket.** The portable success signal, confirmed in
source (`manager_server.py:939-942`) and probed live, is the installed-set diff:

```
GET /api/customnode/installed                 -> live disk scan
GET /api/customnode/installed?mode=imported   -> startup_time_installed_node_packs
```

`mode=imported` returns a snapshot frozen at ComfyUI startup; the default is a live
disk scan. **Non-empty `disk − imported` ⇒ the pack landed and a restart is pending**
— exactly the state our UX reports anyway. Works identically on V3 and V4. (Verified
baseline: both 31 packs, empty diff, i.e. nothing pending — the correct steady state.)

On **V4** additionally use `GET /api/v2/manager/queue/history?ui_id=<id>` for real
per-item status (`status.status_str` ∈ `success|failed|skipped|error|skip`).

### Wire details that will bite

- **Content-Type must be `application/json`.** A `_reject_simple_form_content_type`
  guard 400s `application/x-www-form-urlencoded`, `multipart/form-data`, and
  **`text/plain`** — which is what Go's `http.Post` sends by default. For no-body
  POSTs (`queue/start`, `reboot`) send `{}` with the JSON header.
- **There is no CSRF token.** `supports_csrf_post` means only "state mutation must be
  POST and simple-form Content-Types are refused". No header, no auth.
- **Always send `mode=cache`** — `getlist` uses bracket access (`query["mode"]`) and
  KeyErrors → 500 without it (upstream issue #2740). Also send `skip_update=true`.
- **`getlist` is GONE in V4 default mode** (legacy-UI only), so V4-default attribution
  leans on `getmappings` + Registry.
- Prefix every route with `/api`.
- V4 tombstone: `GET /v2/customnode/fetch_updates` → **410**, still documented as 200.

### 🔴 Reboot kills a running generation

`POST /manager/reboot` does **`os.execv`** with **no queue inspection whatsoever**.
It does not refuse while a generation is running — it destroys it. It returns
nothing; the process is replaced mid-request, so the client sees a connection reset
(treat transport failure as success, then re-probe `/api/manager/version` until it
answers).

> **Required:** before offering reboot, check `GET /api/queue` ourselves and refuse
> when `queue_running` / `queue_pending` are non-empty. Never reboot without explicit
> user action. This is the single most destructive call in the feature.

**Upstream stability, stated honestly:** these endpoints are de-facto internal to
Manager's own frontend. There is no third-party integration contract; the V3→V4
transition has no migration doc on `main`; `manager-v4/openapi.yaml` omits the very
install endpoints its changelog tells third parties to migrate to; and when asked
directly for an API, the maintainer pointed at `comfy-cli`. **Therefore: every
Manager call is best-effort, every failure degrades to attribution + manual
command, and no Manager response is load-bearing for correctness.**

---

## Invariants this must not break

- **Offline / no-CDN** for assets — unchanged; the fallback index is a data fetch,
  not a script/style/font.
- **CSRF on every POST**; **loopback-gating** on the install endpoint.
- **Race-safe streaming jobs** if install progress streams: append + snapshot under
  the job mutex, poller targets a stable container.
- **Never touch node packs civitai-manager did not install** — with delegation this
  is free: we never write to `custom_nodes/` at all.
- **Never automatic.** Explicit per-pack confirmation, showing exactly what will run.

## Done when

- A workflow needing an uninstalled node type shows **which classes are missing and
  which pack provides each** (and says so plainly when it cannot attribute one),
  instead of a generic preflight failure.
- The user can install the installable packs from the UI in a gated flow, and after
  one ComfyUI restart the same workflow runs.
- Declining — or Manager being absent, or the pack being policy-blocked — leaves
  ComfyUI untouched and still tells the user exactly what to install by hand.

## Verifier

End-to-end against the real local ComfyUI at `127.0.0.1:8188` using **wf581**:
confirm the report names Comfyroll/mtb/VFI correctly, confirm the nightly-only
block is explained rather than failing opaquely, install an installable pack
through the UI, restart ComfyUI, confirm the run progresses past that node.
Plus the real gate (build/vet/test + `-race` on web/comfy/comfyext/store) and a
full `/audit-pr` — this drives installs into the user's ComfyUI and adds new
outbound egress, the highest blast-radius class in this repo.
