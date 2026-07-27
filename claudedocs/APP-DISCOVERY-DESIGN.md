# CivitAI App discovery + click-to-play — feasibility + design proposal (v0 draft)

_Status: **A1 SHIPPED (v0.1.49: browse + click-to-play); A2 + A3 still open.**
Originally grounded 2026-07-26 by (a) probing the live civitai.com API with `curl`
(anon AND authenticated as the user's own personal API key), (b) reading the prod
repo at `/home/zach/workspace/civit/civitai` (routers, REST endpoints, DTO schemas,
page routes), and (c) reading the private `github.com/civitai/cli` SDK's App-Blocks
client. Cross-references the shipped `internal/web/discover_workflows.go` page (the
pattern this mirrors) and `claudedocs/WORKFLOW-DISCOVERY-DESIGN.md`._

_**Two research assumptions reality overturned during A1 (corrected below):**_
_(i) The catalog is **NOT dark for THIS user** — the user's own published apps ARE
visible via their API key, so the POPULATED render path **was live-verified**, not
just the empty path._
_(ii) The app-listing item **`id` is a ULID string** (e.g. `apl_01K…`), NOT an int
(`creator.id` IS an int) — this cost a live-caught decode bug in A1._

Scope, as decided by the user:
- **"Apps"** = CivitAI Apps (internally: **App Blocks** — sandboxed web apps that run
  inside civitai — plus **off-site** external-link apps).
- **"Click to play" = DEEP-LINK OUT.** Discover apps in-manager, click → open the app
  on civitai.com (or its external URL) in the user's own browser. NOT embedding /
  iframing, NOT orchestrated running.
- **Discovery scope** = ALL published apps (a browse/search surface).

---

## 1. Feasibility verdict

**A stable PUBLIC REST catalog API exists and is purpose-built for exactly this — but
it is DARK (default-closed) and returns ZERO apps to a normal user TODAY.** It
auto-opens, with no civitai code change, the moment CivitAI flips the marketplace
launch flag.

| Question | Verdict | Basis |
|---|---|---|
| Is there a stable public API to LIST/browse published apps? | **YES — it exists and is explicitly documented as "the REST front… to back a CLI app-discovery command."** | `GET /api/v1/apps` — `src/pages/api/v1/apps/index.ts:10-47`. Returns the standard `{items, metadata:{nextCursor,nextPage}}` envelope. |
| Can we get a per-app detail + play URL? | **YES** | `GET /api/v1/apps/{slug}` — `src/pages/api/v1/apps/[slug].ts:8-31`; the `ListingDetail` / `ListingCard` DTOs carry a play URL (`kindData.liveUrl` for onsite, `kindData.externalUrl` for offsite) — `app-listing-read.schema.ts:153-243`. |
| Does it return anything to a NORMAL user right now? | **NO — empty.** | Live probe, anon AND authed as `zachlowdenzx` (personal API key): `GET /api/v1/apps?limit=5` → `HTTP 200 {"items":[],"metadata":{}}`; `kind=offsite` → same empty. `GET /api/v1/apps/test` → `HTTP 404 {"error":"App not found"}`. |
| Why empty? | **Default-closed launch gate**, not a bug. | `resolveStoreVisibilityScope({user})` returns `none` for anyone who is not a moderator or in the `app-dev-testers` cohort, and the endpoint hard-returns an empty page on `none` — `index.ts:77-82`, scope logic `app-blocks-flag.ts:655-682`. |
| Is it tRPC-only / internal? | **NO — it's a first-class `/api/v1/*` REST contract**, alongside a tRPC path. | The tRPC path (`blocks.listAvailable`) is origin-gated and 401s an external caller with _"Please use the public API instead: https://developer.civitai.com/"_ (live probe). The `/api/v1/apps` REST endpoint is the sanctioned external contract. |
| Does browsing require auth? | **No** — `MixedAuthEndpoint`, anon-capable; a token only matters for whether you're in the privileged cohort. | `index.ts:18-35`, `[slug].ts:14-25`. |
| Does PLAYING require a civitai session? | **Offsite apps: no** (the target is a fully external URL). **Onsite apps: effectively yes today** — the play page is itself flag-gated. | Offsite `externalUrl` is arbitrary; onsite `/apps/run/<slug>` SSR-404s without `features.appBlocks && features.appBlocksPages` — `src/pages/apps/run/[slug]/[[...path]].tsx:46-62`. |

### Bottom line (blunt)

- **The API is real, stable, public, and designed for this feature.** We are not
  inventing an endpoint and not screen-scraping tRPC.
- **But there is nothing to discover yet.** The catalog is behind CivitAI's pre-launch
  Flipt gate; a normal user (incl. the project's own user) gets an empty list. So a
  browse page built today renders an honest **empty state** until CivitAI launches the
  Apps marketplace — at which point it lights up **with zero changes on our side**
  (the endpoint auto-widens; `index.ts:24-35`).
- **Recommended posture:** build against the stable REST contract now, ship it
  flag-tolerant (graceful empty state + a "no apps published yet" message), so the
  feature is ready and correct the day the marketplace goes public. Do **not** touch
  the origin-gated tRPC path, and do **not** fake data. This is option (a) done
  honestly — use the sanctioned public endpoint, accept that it's empty pre-launch.

The one genuinely load-bearing risk: **we cannot end-to-end verify the populated path
here** (no published apps are visible to us), only the empty/404 path + the exact
response contract from source. Say so when reporting (see §5).

---

## 2. What reality forces on the design (load-bearing findings)

### 2.1 The catalog contract is fixed and pleasant to consume

`GET /api/v1/apps` (`index.ts`) accepts only what the store service supports — **no
free-text `query`, no slot filter** (`index.ts:42-46` says they'd be inert). The real
filter axes:

- `kind` = `all | onsite | offsite`
- `category` (validated against `MARKETPLACE_CATEGORIES`)
- `sort` = `top-rated (default) | popular | newest | name` (`app-listing-read.schema.ts:46`)
- `cursor` (opaque base64url keyset) + `limit` (1..50, default 20)

Response: `{ items: ListingCard[], metadata: { nextCursor, nextPage } }` —
**keyset-cursor pagination**, same shape the manager's SDK/CLI already speaks.

`ListingCard` (`app-listing-read.schema.ts:177-194`) — **apps carry images and all the
card fields we need**:

```
id, slug, kind, name, tagline, category, contentRating,
iconUrl, coverUrl,                       // ← images for the card
creator{id,username,image},              // ← creator chip
recommend{recommendedCount,notRecommendedCount,recommendPct|null}, reviewCount,
kindData:  onsite  → { appBlockId, hasPage, liveUrl }      // ← play URL (onsite)
        |  offsite → { subKind, externalUrl }              // ← play URL (offsite)
```

Consequence for the UI: this is a **card grid with icon/cover thumbnails, name,
tagline, creator, a recommend %, and one primary action** — structurally the same as
the workflow/model card grid, but the item shape is DIFFERENT from a `ModelListItem`
(no `modelVersions[]`, no `parseSearchImages` gallery). So we **reuse the page
scaffold/nav/theme, but write a small app-card renderer** — we do NOT reuse
`parseSearchImages` (that parses model search `Raw`, which apps don't have).

### 2.2 The play URL is in the payload; two kinds behave differently

- **offsite / external-link** → `kindData.externalUrl` is a fully external target. A
  deep-link to it is always valid and needs no civitai session. This is the cleanest
  click-to-play. (`kind=offsite` is also what the future public flag opens FIRST —
  `app-blocks-flag.ts:651-654`, `public-external` scope.)
- **onsite (App Block)** → the play surface is `civitai.com/apps/run/<slug>` (slug ==
  `block_id`; `run/[slug]/[[...path]].tsx:16-18`) or the standalone
  `kindData.liveUrl` origin. **Caveat:** the `/apps/run` page SSR-404s unless the
  viewer has `features.appBlocks && features.appBlocksPages`
  (`run/[slug]/[[...path]].tsx:52-54`). `liveUrl` is described as an
  "already-public standalone block origin (no token/scope)" (`schema:166-168,206-208`)
  — likely linkable, but **unverifiable here** (no live onsite app to click). Prefer
  `liveUrl` when present; fall back to `/apps/<slug>` (the detail page) rather than a
  page that might 404.

Design implication: the click-to-play link is just an **external anchor** — reuse the
existing hardened "open on civitai.com in a new tab" pattern already in the codebase:
`h.Target("_blank")` + `rel=noopener noreferrer` (`internal/web/model_pages.go:681-685`).

### 2.3 There is no free-text search — "search" == filter + sort

The user asked for "browse/search". The API supports **filter (kind/category) + sort +
cursor paging**, not keyword search. So the discovery surface is a **filtered, sorted,
paginated browse** — honest to call it "Browse apps", not a search box. (A client-side
name filter over the current page is possible but shallow; don't oversell it.)

### 2.4 Auth: browsing is anon-capable; the token only changes cohort visibility

`MixedAuthEndpoint` (`index.ts:62`, `[slug].ts:46`) treats an absent/invalid token as
anonymous — never an error. Sending the user's configured CivitAI token is harmless and
free, and is the ONLY way a mod/app-dev-tester would see the (currently mod-only)
catalog early. For a normal user it changes nothing (still `none` → empty), as the live
authed probe confirmed. **Recommendation:** send the token if configured (so an enrolled
user sees more), but never require it.

### 2.5 Egress: browsing sends the filter/sort to civitai.com; playing opens the browser

Browsing issues a `GET` to `civitai.com/api/v1/apps` (query params only — no file
hashes, unlike scan `match_remote`). Playing opens the user's own browser to
civitai.com / the app's external URL. Both are outbound to civitai and must be obvious
to the user (mirror the repo `CLAUDE.md` egress-transparency invariant), but the
data-sensitivity is far lower than the scan hash-egress path.

### 2.6 Rate limits exist and are lenient

`enforceAppsCatalogRateLimit` gates both endpoints (`index.ts:74`, `[slug].ts:52`).
Cursor paging + a short client-side cache (mirror the manager's existing
`popularModels` / community-feed TTL cache) keeps us well under it.

---

## 3. Proposed integration (only as feasible)

Design principle: **mirror the shipped `discover_workflows.go` page scaffold; swap the
data source (apps REST) + the card renderer + the primary action (external
click-to-play).** Keep it flag-tolerant so an empty catalog renders cleanly.

### 3.1 Surface: a dedicated "Apps" browse page

A near-clone of `handleDiscoverWorkflows` (`internal/web/discover_workflows.go:29`),
differing in data source and card action:

- **Route:** `GET /apps/discover` (page) + the same handler serving the HX results
  fragment on `hx-get` — mirror `discover_workflows.go:86 workflowDiscoverPage` /
  `:123 workflowDiscoverResults` and the `hx("get", …)` wiring (`:95`). Register in
  `internal/web/server.go` next to `:373 GET /workflows/discover`. Add a nav entry via
  `navLink(...)` in `internal/web/layout.go:70` (where `/workflows/discover` "Discover"
  already lives).
- **Controls:** `kind` (all/onsite/offsite), `category`, `sort`
  (top-rated/popular/newest/name), and cursor paging — the exact axes the API exposes
  (§2.1). NO free-text search box (§2.3); if desired, a client-side filter over the
  loaded page only, clearly scoped.
- **Data:** a NEW thin client method on the civitai wrapper (`internal/civitai`) —
  `ListApps(ctx, params) (AppsPage, error)` — that GETs `/api/v1/apps`, sends the
  user's token if configured (§2.4), and decodes the `{items, metadata}` envelope into
  a small Go mirror of `ListingCard`. This is NOT `SearchModels`; apps are a different
  shape. Add matching `App` / `AppsPage` structs.
- **Card renderer:** a NEW small `appCard(...)` (do NOT reuse `parseSearchImages`) —
  icon/cover thumbnail (`iconUrl`/`coverUrl`), name, tagline, creator chip, recommend
  %/reviewCount, contentRating badge, and one primary action button (§3.2). Style with
  existing `.cm-*` / theme tokens; if a new Tailwind utility class is introduced,
  rebuild `output.css` per the repo invariant.
- **NSFW / maturity:** the API already hides mature (r/x) apps off a non-red host
  server-side (`index.ts:56-57,85-88`), so the manager receives SFW-only by default —
  honor `contentRating` in the card badge; no client NSFW omission logic is strictly
  required, but keep the display consistent with the app's `hide|blur|show` posture for
  any imagery.
- **Empty state (load-bearing):** when `items` is empty (the reality today), render an
  explicit, honest message — e.g. "No published apps yet — CivitAI's App marketplace
  hasn't launched publicly. This page will populate automatically when it does."
  This is the difference between a broken-looking page and a correct pre-launch one.

### 3.2 Click-to-play = an external deep-link (new tab)

The card's primary action is an **external anchor**, reusing the hardened new-tab
pattern at `internal/web/model_pages.go:681-685` (`h.Target("_blank")` +
`rel="noopener noreferrer"`):

- **offsite** → link to `kindData.externalUrl` (label e.g. "Open app ↗"). Always valid.
- **onsite** → link to `kindData.liveUrl` when present, else the civitai detail page
  `https://civitai.com/apps/<slug>` (avoid linking straight to `/apps/run/<slug>`,
  which can SSR-404 for a viewer without the page flag — §2.2).
- **Transparency:** the link text/affordance makes clear it opens civitai.com / an
  external site in the user's browser (egress posture, §2.5). No CSRF (GET browse +
  external links are not state-changing on our side).

Because this is a deep-link OUT, the offline/no-CDN invariant is preserved: the
manager's own JS/CSS/fonts stay vendored; we only emit an `<a href>` to civitai — no
embedded/iframed civitai assets, no new CDN reference in our UI.

### 3.3 Optional: a per-app detail fragment

If a card tap should show more before leaving, a lazy fragment
`GET /apps/discover/{slug}` calling `/api/v1/apps/{slug}` (`[slug].ts`) yields the
`ListingDetail` (description + ordered `screenshots[]` gallery + the same play action).
Cache-first + fail-open, mirroring the model community-feed fragment pattern. Lower
priority than the browse grid; only worth it if the card alone feels thin.

### 3.4 What we deliberately do NOT build

- No embedding/iframing/orchestration (out of scope by decision).
- No use of the origin-gated tRPC `blocks.*` procedures (they 401 external callers and
  are not a public contract).
- No import-into-library (apps aren't downloadable artifacts like Workflows models).
- No fabricated/placeholder catalog data to make the page "look populated".

---

## 4. Slicing (thin-first, independently shippable v0.1.x)

Each slice ships with **complete test coverage** when built (HTTP-level handler tests +
a table-driven client decode test against a captured/synthetic `{items,metadata}`
body, since we can't rely on a populated live catalog — mirror the existing
`discover_workflows_web_test.go` style).

- **Slice A1 — Apps browse page, browse-only (MVP, recommended first).** _[SHIPPED
  v0.1.49: browse + click-to-play; the POPULATED path live-verified against the
  user's own published apps]_
  `internal/civitai.ListApps` (GET `/api/v1/apps`, token-optional, envelope decode) +
  `GET /apps/discover` page/fragment cloned from `discover_workflows.go` + `appCard`
  renderer + kind/category/sort/cursor controls + nav entry + the honest empty state.
  Card action = external click-to-play (§3.2). **Fully verifiable here at the HTTP
  level for the empty + error + 404 paths and the exact request shape; the POPULATED
  render is verifiable only with a synthetic-body unit test** (no live apps visible).
  Lowest risk, self-contained.
- **Slice A2 — per-app detail fragment (optional).** _[OPEN]_ `GET /apps/discover/{slug}` →
  `/api/v1/apps/{slug}` (`ListingDetail`): description + screenshot gallery + play
  action; cache-first + fail-open. Independent of A1's grid.
- **Slice A3 — polish / QoL (optional).** _[OPEN]_ Client-side name filter over the loaded page
  (clearly scoped, §2.3), a short TTL cache on the list call (§2.6), a "launching soon"
  learn-more affordance, and — if/when CivitAI opens the `public-external` flag first
  (§2.2) — defaulting the kind filter to `offsite` so the first populated apps surface.

Recommended order: **A1 → A2 → A3.** A1 stands alone and is the whole feature at MVP;
A2/A3 are enhancements.

---

## 5. Open questions / risks / caveats

1. **Empty until launch (biggest).** The catalog returns nothing to a normal user today
   (§1, live-probed). The feature is correct and ready but visibly empty until CivitAI
   flips the marketplace Flipt segment. Manage expectations: this is a "build ahead of
   launch" feature, not one that lights up on merge. Its value is realized on CivitAI's
   schedule, not ours.
2. **Populated path unverifiable here.** We verified the empty-list, 404, auth-optional,
   and exact response contract (from source + live probes), but **cannot click a real
   published app** — none are visible to us. The card renderer + play-link must be
   covered by synthetic-body unit tests, and the populated path flagged as
   **untested against live data** when reporting. Do not claim end-to-end verification.
3. **API stability.** `/api/v1/apps` is documented as a stable public REST contract
   meant to back a CLI discovery command (`index.ts:10-16`), so it is a reasonable bet
   — but it is **pre-launch (W13)**; field names / filter axes could still shift before
   GA. Pin the decode to the documented allowlist DTO and fail-open on unknown fields.
4. **Onsite play-URL uncertainty (§2.2).** `/apps/run/<slug>` is flag-gated and can
   SSR-404; `liveUrl` is claimed public but unverified. Prefer `liveUrl`, else the
   `/apps/<slug>` detail page. Don't hard-link a path that may 404 for the user.
5. **No free-text search (§2.3).** "Discovery" here is filter+sort+paginate, not
   keyword search. Name the surface accordingly ("Browse apps"), don't ship a search box
   that maps to nothing.
6. **Egress (§2.5).** Browsing sends filter/sort params to civitai.com; playing opens
   the user's browser to civitai / an external site. Lower-sensitivity than scan
   hash-egress, but keep the "opens civitai.com" affordance obvious (repo `CLAUDE.md`
   egress invariant).
7. **Rate limits (§2.6).** Lenient but present (`enforceAppsCatalogRateLimit`); use
   cursor paging + a short cache; don't poll the list endpoint on a timer.
8. **Alternative if we want SOMETHING visible today:** the degenerate-but-honest option
   (option (b) from the brief) — a single "Browse CivitAI Apps ↗" nav link to the
   civitai Apps gallery page — is trivial and always works, but delivers no in-manager
   browsing. Given the real API exists and auto-populates at launch, **A1 (build the
   real page, empty-tolerant) is the better use of effort** unless the user wants a
   zero-effort placeholder now.

---

### Appendix — evidence anchors (file:line + live probes)

**Live probes (civitai.com, 2026-07-26):**
- `GET /api/v1/apps?limit=5` (anon) → `HTTP 200 {"items":[],"metadata":{}}`
- `GET /api/v1/apps?limit=5` (authed, personal API key `zachlowdenzx`) → `HTTP 200 {"items":[],"metadata":{}}`
- `GET /api/v1/apps?kind=offsite&limit=5` (authed) → `HTTP 200 {"items":[],"metadata":{}}`
- `GET /api/v1/apps/test` → `HTTP 404 {"error":"App not found"}`
- `GET /api/trpc/blocks.listAvailable` → `HTTP 401 {"…message":"Please use the public API instead: https://developer.civitai.com/","code":"UNAUTHORIZED"}`

**Prod repo (`/home/zach/workspace/civit/civitai`):**
- REST list: `src/pages/api/v1/apps/index.ts:10-47` (contract), `:62-93` (handler),
  `:77-82` (default-closed `none` → empty).
- REST detail: `src/pages/api/v1/apps/[slug].ts:8-31, 46-72`.
- DTOs / filter axes: `src/server/schema/blocks/app-listing-read.schema.ts:46`
  (sort), `:49-84` (list input + REST query), `:153-194` (`ListingCard` + `kindData`
  play URLs), `:196-244` (`ListingDetail` + screenshots).
- Scope gate: `src/server/services/app-blocks-flag.ts:235 isAppListingsEnabled`,
  `:655 StoreVisibilityScope`, `:672-682 resolveStoreVisibilityScope`.
- tRPC store read (origin-gated, NOT used): `src/server/routers/blocks.router.ts:2304
  listAvailable`, `:2363 getAppDetail`; unified `app-listings.router.ts:1075
  listAvailable`, `:1097 getAppDetail`.
- Onsite play page (flag-gated, can 404): `src/pages/apps/run/[slug]/[[...path]].tsx:16-18, 46-62`.

**CLI SDK (`github.com/civitai/cli`):** `internal/appapi/appblocks.go` — App-Blocks
client is **authoring/submit/dev-tunnel only** (submit-version, submissions, withdraw,
dev-token, Forgejo clone); it has **no app-listing/discovery call**. Confirms discovery
must go through the `/api/v1/apps` REST endpoint, not the SDK.

**Manager reuse anchors (this repo):**
- Page scaffold to mirror: `internal/web/discover_workflows.go:29 handleDiscoverWorkflows`,
  `:56-67` param assembly + `SearchModels` call, `:86 workflowDiscoverPage`, `:95 hx-get`,
  `:123 workflowDiscoverResults`.
- Route + nav: `internal/web/server.go:373 GET /workflows/discover`,
  `internal/web/layout.go:70 navLink`.
- External new-tab link pattern (click-to-play): `internal/web/model_pages.go:681-685`
  (`h.Target("_blank")` + `rel=noopener noreferrer`).
- Cache/fail-open patterns to reuse: `popularModels` TTL cache + the model community-feed
  lazy fragment (per `WORKFLOW-DISCOVERY-DESIGN.md` §3 anchors).
