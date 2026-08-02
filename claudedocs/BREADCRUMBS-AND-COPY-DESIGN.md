# Breadcrumbs & copy-reduction — design document

**Status:** design only. Nothing in this document has been implemented.
**Base:** `main` @ `e9c89f0` (v0.1.97), read on 2026-07-31.
**Read `CLAUDE.md` before acting on any deletion proposed here.**

> ⚠ **This document was written while `feat/app-shell` was in flight.** That branch
> already changes `page()`'s SIGNATURE and touches ~20 of the same template files.
> Section 5 lists exactly what that invalidates. Re-check before implementing.

---

## 0. Scope and method

Everything below comes from reading `internal/web/*.go` and from GET-only
reproduction against the user's live dogfood instance on `:8972` (v0.1.97). No
file in the repo was modified; no POST was issued.

Two live findings turned up during the sweep that are **pre-existing bugs, not
design proposals**. They are recorded in §6 because both bear directly on the
work planned here.

---

## 1. Page / route inventory

### 1.1 What counts as a "page"

A page is anything that calls `page(...)` in `internal/web/layout.go:34` — the
full HTML document shell (nav + `<main>` + outputs rail). Everything else the
mux serves is an htmx fragment, a POST action, an image byte route, or a
redirect.

There are **17 full-page render sites** across 15 reachable surfaces.

### 1.2 The inventory

| # | Route | Builder (file:line) | `<h1>` | Logical parent | Reachable from |
|---|---|---|---|---|---|
| 1 | `GET /{$}` | — (302 → `/search`, `handlers.go:57`) | n/a | — | brand wordmark (`brand.go:73`), address bar |
| 2 | `GET /search` | `searchPage` `pages.go:567` → `browse_surface.go:54` | **Search models** | none (top level) | nav "Find models" (`layout.go:135`); brand → `/` → 302 |
| 3 | `GET /models/{id}` | `modelDetailPage` `model_pages.go:586`, h1 at `model_pages.go:919` | **{model name}** | **ambiguous — 6 inbound edges** | search cards, creator cards, discover cards, subscriptions table, library file cards, workflow resource chips, generation provenance |
| 4 | `GET /creators/{username}` | `creatorPage` `pages.go:719`, h1 at `pages.go:732` | **@{username}** | ambiguous | model cards (`pages.go:665`), model header (`model_pages.go:924`) |
| 5 | `GET /subscriptions` | `dashboardPage` `pages.go:94` | **Subscriptions** | none (top level) | nav Library menu only (`layout.go:325`) |
| 6 | `GET /library` (`?tab=sources\|files\|workflows`) | `libraryPage` `library_pages.go:189` | **Library** | none (top level) | nav Library menu × 2 entries (`layout.go:321‑322`) — **`?tab=sources` has no nav entry** |
| 7 | `GET /disks` | `disksPage` `disks_pages.go:51` | **Disks** | none (top level) | nav "Disks" |
| 8 | `GET /trash` | — (302 → `/disks`, `disks_handlers.go:102`) | n/a | — | **direct URL / old bookmarks only** — `nav_labels_web_test.go:74` asserts `href="/trash"` is gone from the nav |
| 9 | `GET /outputs` | `outputsGalleryPage` `outputs_pages.go:332`, h1 at `:356` | **Outputs** | none (top level) | outputs-rail heading + foot link (`outputs_rail.go:246`, `:257`) — **conditional, see §1.3** |
| 10 | `GET /outputs/{id}` | `generationDetailPage` `outputs_pages.go:523`, h1 at `:542` | **{generation label}** | `/outputs` (real) | rail tiles (`outputs_rail.go:334`), gallery tiles, batch tiles |
| 11 | `GET /outputs/batch/{id}` | `batchGalleryPage` `outputs_pages.go:455`, h1 at `:466` | **Batch «{label}»** | `/outputs` (real) | rail ×N tiles (`outputs_rail.go:338`), tile caption "Batch i/N" (`outputs_pages.go:256`) |
| 12 | `GET /workflows` | — (303 → `/library?tab=workflows`, `workflow_handlers.go:27`) | n/a | — | legacy POST-redirect-GET targets |
| 13 | `GET /workflows/{id}` | `workflowDetailPage` `workflow_pages.go:1071`, h1 at `:1081` | **{workflow name}** | `/library?tab=workflows` (real) | Workflows tab cards, generation provenance, model "used by workflows" |
| 14 | `GET /workflows/discover` | `workflowDiscoverPage` `discover_workflows.go:118` | **Discover workflows** | none (top level) | nav "Find workflows" |
| 15 | `GET /apps/discover` | `appsDiscoverPage` `discover_apps.go:139` | **Apps** | none (top level) | nav "Apps" |
| 16 | `POST /workflows/{id}/open-in-comfyui` | `renderOpenComfyPage` `workflow_open_comfy.go:330` | **Open in ComfyUI** | `/workflows/{id}` (real) | **form `target=_blank` only — no GET route exists** |
| 17 | error page | `page("Not found", …)` `handlers.go:430`; `handlers.go:879` | — (renders an alert, **no `<h1>` at all**) | — | error state |

### 1.3 Orphans and reachability defects

Flagged explicitly, worst first.

**O1 — `trashPage` is DEAD CODE with no route at all.**
`library_pages.go:1336` builds a full page whose `<h1>` is "Quarantine trash"
(`library_pages.go:1339`). Its handler `handleTrash` (`library_handlers.go:619`)
is **not registered in `server.go`** — verified by diffing the registered-handler
set against the defined-handler set; `handleTrash` and `handleComfyExtensionAction`
(a shared helper, legitimately unregistered) are the only two definitions with no
`mux.HandleFunc`. `GET /trash` goes to `handleTrashRedirect` instead. So this page
is unreachable by nav, by link and by URL — yet it is still rendered by
`fullPages()` (`ux_audit_web_test.go:75`) and asserted by
`TestEmptyStatesGuideTheUser` (`ux_audit_web_test.go:760`). It is a third orphan,
stronger than the two already known: not "lost its nav entry" but "cannot be
reached at all".
*Recommendation: delete `handleTrash` + `trashPage` and re-point the two tests at
`disksPage`, which already carries the same quarantine table and the same empty
state. Out of scope for both passes here, but it should not be given a breadcrumb.*

**O2 — `/outputs` is nav-less and its only entry point is conditional.**
Documented honestly at `layout.go:117‑124`. The rail renders only when
`railData.visible()` is true (`outputs_rail.go:143`, gated at `:150` and `:178`),
i.e. only when at least one generation exists. On a fresh install `/outputs` has
**zero** in-app links.

**O3 — `/subscriptions` is reachable from exactly one control.**
The nav Library popover (`layout.go:325`). `nav_reachability_web_test.go:118`
exists precisely because that one link is the whole reachability story.

**O4 — `/library?tab=sources` is the page's DEFAULT tab but has no nav entry.**
`libraryMenu` links `?tab=files` and `?tab=workflows` (`layout.go:245‑248`); a bare
`/library` (which nothing links) falls through to `sources`
(`library_pages.go:196‑198`). The Sources tab is reachable only via the in-page tab
strip. Not an orphan, but the nav under-describes the page.

**O5 — `/trash` is a live route with no in-app referrer.** Bookmark compatibility
only, by design (`disks_handlers.go:90‑96`). Correct as-is; listed for completeness.

**O6 — The "Open in ComfyUI" result page has no GET route.** It exists only as the
body of a `POST … target=_blank` (deliberate, `workflow_open_comfy.go:327‑329`).
A reload of that tab re-POSTs or 405s. It is the one page where a back-link is
genuinely the only navigation, because there is no URL to link *to* it.

### 1.4 Cross-check against `e2e/uxaudit/walk.go`

`Views()` (`walk.go:34‑91`) captures **7** views:
`dashboard` (`/subscriptions`), `workflows-list`, `workflow-import` (dialog on the
same path), `workflow-detail`, `run-missing-models` (hero), `discover-workflows`,
`library-sources`.

**Missing from the curated list — 9 reachable surfaces, including the app's front
door:**

| Missing view | Why it matters |
|---|---|
| `/search` | This is `/`. The app's front door is not in the funnel at all. |
| `/models/{id}` | The single richest page in the app (version tabs, showcase, community, related workflows). |
| `/creators/{username}` | — |
| `/library?tab=files` | The scan/summary half of Library; `library-sources` covers only the other tab. |
| `/disks` | Added after the walk list was written; has three distinct body branches (see `ux_audit_web_test.go:96‑109`). |
| `/apps/discover` | — |
| `/outputs` | — |
| `/outputs/{id}` | — |
| `/outputs/batch/{id}` | Also missing from `fullPages()` in `ux_audit_web_test.go` (see §5.2). |

*This is not a breadcrumbs problem, but if breadcrumbs land they will change the
top of every one of those pages, and 9 of them have no axe/screenshot baseline.*

---

## 2. Breadcrumbs design

### 2.1 The honest conclusion first

**Most of this app is two levels deep and flat.** Nine of the fifteen reachable
surfaces are nav destinations with no parent, and the two most-visited detail
pages (`/models/{id}`, `/creators/{username}`) have genuinely ambiguous parents.
A universal breadcrumb bar would therefore be **mostly fabricated hierarchy** —
`Home › Disks`, `Home › Apps` — which adds a row of chrome to every page and
tells the user nothing they cannot read from the nav's active state.

So: **breadcrumbs are proposed for 4 pages, not 15.** On those 4 the trail is
real, it is already half-expressed by a hand-rolled back-link, and in two cases
it carries information no current control carries.

### 2.2 Per-page trails

#### Real trails — implement these

| Page | Proposed trail | Basis |
|---|---|---|
| `/workflows/{id}` | `Library › Workflows › {workflow name}` | Real. The existing back-link already asserts this parent *and* the deep anchor: `href="/library?tab=workflows#wf-{id}"` (`workflow_pages.go:1082`). First crumb → `/library?tab=workflows`; second is the same href, so **collapse to two crumbs**: `Workflows › {name}` (see §2.2.1). |
| `/outputs/{id}` (unbatched) | `Outputs › {generation label}` | Real. Replaces `← Back to Outputs` (`outputs_pages.go:544`). |
| `/outputs/{id}` (batched) | `Outputs › Batch {i}/{N} › {generation label}` | Real **and net-new information**. Today the batch relationship is expressed only in the tile caption on the *gallery* (`outputs_pages.go:246‑259`); on the detail page itself the back-link says "Outputs" and the batch is invisible. This is the single strongest argument for the feature. |
| `/outputs/batch/{id}` | `Outputs › Batch «{label}»` | Real. Replaces `← All outputs` (`outputs_pages.go:469`). |
| `POST /workflows/{id}/open-in-comfyui` | `Workflows › {workflow name} › Open in ComfyUI` | Real. Replaces `← Back to the workflow` (`workflow_open_comfy.go:342`). ⚠ The handler currently has the workflow **id** but renders the back-link from the id alone; a name crumb needs the name threaded in, or the middle crumb degrades to `workflow #{id}` exactly as `workflowDetailPage` already does (`workflow_pages.go:1075‑1077`). |

##### 2.2.1 Why the workflow trail is two crumbs, not three

`Library` and `Workflows` would both link to `/library?tab=workflows` — the tab
*is* the Library page. Two crumbs pointing at one URL is the fake-hierarchy
failure mode in miniature. Render `Workflows › {name}`, where `Workflows` is the
tab href. This matches the label the nav menu already uses
(`layout.go:322`: "Workflows").

#### No trail — do NOT invent one

| Page | Why not |
|---|---|
| `/search` | It **is** `/`. `Home › Search models` would mean `Search models › Search models`. |
| `/subscriptions`, `/disks`, `/apps/discover`, `/workflows/discover` | Top-level nav destinations. `Home › X` where Home is the search page is a lie about the information architecture. |
| `/library` | The tab strip (`library_pages.go:217‑248`) already *is* the second level, complete with `aria-current="page"` on the active tab (`:238`). A breadcrumb above it would restate it one row higher. |
| `/models/{id}` | **Six inbound edges, no owner.** A trail would have to pick one (`Search models › {model}`?) and be wrong most of the time. Referer-driven trails are worse: non-deterministic, un-shareable, and they make the same URL render differently for two users. Explicitly rejected. |
| `/creators/{username}` | Same. Reached from a card or from a model header; CivitAI creators are not a container the app browses. |
| `/trash`, `/workflows`, `/{$}` | Redirects. |
| Error page (`handlers.go:430`) | Has no `<h1>` today; a trail on an error page is chrome around a failure. |

### 2.3 What breadcrumbs REPLACE — the deletions that make this a net reduction

This is the load-bearing half. **If the trail lands and these do not go, the
change makes every affected page noisier.**

| Delete | file:line | Current markup | Replaced by |
|---|---|---|---|
| `← Back to Workflows` anchor | `workflow_pages.go:1082‑1083` | `h.A(h.Href("/library?tab=workflows#wf-"+id), …, g.Text("← Back to Workflows"))` | crumb 1 of the workflow trail. **Preserve the `#wf-{id}` fragment on the crumb href** — it is the only reason the back-link returns the user to their position in a long list. |
| the `flex items-center justify-between` header wrapper | `workflow_pages.go:1080‑1084` | exists only to put the h1 and the back-link on one row | with the link gone the h1 stands alone; drop the wrapper div |
| `← All outputs` anchor | `outputs_pages.go:468‑469` | `h.A(h.Href("/outputs"), …, "← All outputs")` | crumb 1 of the batch trail |
| the batch header `justify-between` wrapper | `outputs_pages.go:465‑470` | same shape as above | drop |
| `← Back to Outputs` anchor | `outputs_pages.go:543‑544` | `h.A(h.Href("/outputs"), …)` | crumb 1 of the generation trail |
| the generation header `justify-between` wrapper | `outputs_pages.go:541‑545` | same | drop |
| `← Back to the workflow` anchor + `openComfyBackLink` | `workflow_open_comfy.go:339‑345`, call site `:333‑335` | whole helper | crumbs 1–2 of the open-in-comfy trail; **delete the function** |
| `pageTitle("Open in ComfyUI")` | `workflow_open_comfy.go:331` | the page's `<h1>` | **KEEP.** See §2.4 — the h1 must survive. |
| **Empty-state CTA** `"Back to all outputs"` → `/outputs` | `outputs_pages.go:481` | inside `emptyState(...)` on the batch page | **KEEP.** It is the empty branch's only action and `TestEmptyStatesGuideTheUser` does not cover it, but `batch_gallery_web_test.go:100` does assert `← All outputs`. See §5.2. |

**Headings that merely repeat their section — evaluated, mostly keep:**

- `sectionTitle("Outputs")` on `/outputs/{id}` (`outputs_pages.go:536`) sits under
  an h1 that is the *generation label*, not "Outputs". It does not duplicate. Keep.
- `sectionTitle("Models")` on `/creators/{u}` (`pages.go:737`) under an h1 of
  `@username`. Does not duplicate. Keep.
- `sectionTitle("Your subscriptions")` (`pages.go:127`) was **already** renamed
  away from "Subscriptions" for exactly this reason — see the comment at
  `pages.go:123‑126`. Leave alone.
- `pageTitle("Apps")` inside a `card()` (`discover_apps.go:142`) and
  `pageTitle("Disks")` inside a `card()` (`disks_pages.go:54`) are the only two
  h1s wrapped in a card. Neither gets a trail, so neither changes.

**Net element delta for the 4 trailed pages:** −4 anchors, −3 wrapper `<div>`s,
+4 `<nav><ol>` blocks (2 or 3 `<li>` each). Roughly neutral in element count;
the win is that the batched generation page gains a relationship it could not
previously express, and the four "← Back to …" strings collapse into one shared,
consistently-styled component.

### 2.4 Markup and accessibility

```go
// internal/web/breadcrumbs.go  (NEW FILE — see §2.5 for why not layout.go)

type crumb struct {
    Label string // untrusted for name/label crumbs — always g.Text
    Href  string // "" == the current page (last crumb)
}

func breadcrumbs(items ...crumb) g.Node // <nav aria-label="Breadcrumb"><ol class="cm-crumbs">…
```

Rules:

1. `<nav aria-label="Breadcrumb">` wrapping an `<ol>`; one `<li>` per crumb.
   Separators are CSS (`.cm-crumbs li + li::before { content: "›" }`), **not**
   text nodes — a text separator is announced by screen readers on every item.
2. The **last** crumb is not a link and carries `aria-current="page"`. That is the
   same idiom the library tab strip already uses (`library_pages.go:238`), so it
   is an established convention in this codebase, not a new one.
3. **The trail contains NO heading element.** This is what keeps
   `TestEveryFullPageHasExactlyOneH1` (`ux_audit_web_test.go:144`) green. That
   test asserts two things: exactly one `<h1>`, **and** that `<h1>` is the *first*
   heading in document order (`:159‑163`). Since a `<nav><ol>` emits no `h*` tag,
   a trail rendered above the h1 satisfies both. **Do not be tempted to make the
   last crumb an `<h1>`** — that pattern (breadcrumb-as-heading) would put the
   trail's own markup in the heading outline and would collide with the page h1
   that must stay.
4. The last crumb **duplicates the h1 text** by design (`Outputs › Batch «X»` above
   an h1 reading `Batch «X»`). That is the standard trade and it is acceptable
   here because the h1 carries the styling and the trail carries the ancestry.
   The alternative — dropping the h1 — fails the test above and leaves the page
   with no level-1 heading.
5. `title=` on a truncated crumb: **mandatory**. `TestEveryTruncatedTextHasATitle`
   (`ux_audit_web_test.go:677‑706`) sweeps every non-test `.go` file in
   `internal/web` for `h.Class("… truncate …")` and fails unless an `h.Title(` or
   `pathCell(` appears within a ±1/+3-line window. `breadcrumbs.go` will be a new
   file and is **not** in the `allowed` exemption map (`:687‑693`), so a truncated
   crumb without a title fails immediately. Either carry the title, or truncate
   with a `.cm-crumb-name { max-width; overflow:hidden; text-overflow:ellipsis }`
   custom class instead of Tailwind's `truncate` — the sweep keys on the literal
   token `truncate`, so a `.cm-*` class sidesteps it. **Prefer carrying the title
   anyway**: the sweep's rule exists because hidden content needs an escape hatch,
   and a clipped workflow name is exactly that case.

### 2.5 Where it renders — ⚠ RE-CHECK AFTER `feat/app-shell` MERGES

**Decision: the trail is emitted by each page builder as the first child of
`<main>`, from a NEW file `internal/web/breadcrumbs.go`. It does NOT go in the
shell.**

Rationale:

- Only 4 of 15 pages get a trail. A shell-level strip would need a
  "render nothing" state on 11 pages, which is a conditional row of chrome for
  no benefit.
- `layout.go` is being rewritten on `feat/app-shell` right now. Touching `page()`
  or `navbar()` would collide on nearly every hunk. A new file collides with
  nothing.
- Only the page builder knows the trail. Threading a `[]crumb` through `page()`
  means changing `page()`'s signature — which `feat/app-shell` is **already
  doing** (see §5.1). Two independent signature changes to the same function is
  the worst possible merge.

Shell implications, stated but **not verified against the final shell**:

- The nav is `position: sticky; top: 0; z-index: 30` (`app.css:2147‑2151`). A
  trail inside `<main>` scrolls under it normally. **Do not make the trail
  sticky.** A second sticky band would need its own `top: var(--cm-nav-h)` and a
  z-index slot, and the STACKING ORDER ledger (`app.css:2104‑2135`) has no free
  value between 25 (`.cm-lift` open-popover) and 30 (nav) — taking one would mean
  amending the ledger, which the ledger's own comment says must stay in sync.
- `<main>` is `mx-auto max-w-[1800px] px-4 py-6 space-y-6` (`layout.go:70`).
  `space-y-6` puts a 1.5rem gap between direct children, which is too much between
  a trail and its h1. **Wrap the trail + h1 in one `<div>`**, exactly as
  `libraryPage` already wraps `pageTitle` + the tab strip (`library_pages.go:201‑205`).
- 🔴 **The rail is moving from the RIGHT to the LEFT and becoming a multi-widget
  container.** The trail sits inside `<main>`, whose left edge is set by the
  shell's reserved rail width (`railShellClass`, `layout.go:41‑43`). If the rail
  becomes a left column, the trail's left edge moves with `<main>` — which is the
  behaviour you want, but it has not been checked. **Re-verify the trail's
  alignment against the nav's left edge after the merge**; `shellMeasure`
  (`layout.go:88`) is deliberately shared by nav and main so their edges line up,
  and the trail must land on that same edge.
- The maturity `<select>`s are becoming an icon-button popover. Irrelevant to the
  trail, but it means `maturityControl` / `maturityEnd` (`layout.go:186‑231`) and
  their copy are **out of scope for the copy sweep too** — they are being replaced
  wholesale.

### 2.6 Truncation, overflow, untrusted text

**Which segments are untrusted:**

| Crumb | Source | Bound today |
|---|---|---|
| workflow name | `store.Workflow.Name` — from an imported CivitAI zip or a user paste | **unbounded** |
| generation label | `generationLabel(gen)` — workflow name or preset name | unbounded via workflow name |
| batch label | `batchLabel(first)` — preset name (clamped to 80 bytes) **or** workflow name | unbounded via the workflow-name branch — this is stated at `outputs_pages.go:459‑464` |
| `Batch {i}/{N}` | integers from the DB | safe |
| `Outputs`, `Workflows`, `Open in ComfyUI` | our own constants | safe |

**Escaping:** all crumb labels go through `g.Text`, which HTML-escapes. Never
`g.Raw`. This is not optional — `outputs_pages.go:385` and `workflow_pages.go:1134`
both call out that these strings are untrusted.

**Length bound:** the existing precedent is `min-w-0 break-all` on the h1
(`outputs_pages.go:466`) with the reason spelled out at `:459‑464`: a flex item's
default `min-width: auto` refuses to shrink below its content, so an 80-char
unbreakable name forces the whole page into horizontal scroll at 390px.
`TestLongUntrustedStringsCanBreak` (`ux_audit_web_test.go:406`) guards it.

For a crumb, `break-all` is wrong (a trail that wraps onto four lines is worse
than a clipped one). Proposed:

- The `<ol>` is `display: flex; flex-wrap: nowrap; min-width: 0; overflow: hidden`.
- Ancestor crumbs (`Outputs`, `Workflows`, `Batch i/N`) are `flex: 0 0 auto` — they
  are short constants and must never be the thing that clips.
- The **last** crumb — the only untrusted one on every trail — is
  `flex: 1 1 auto; min-width: 0` with `.cm-crumb-name` doing
  `overflow:hidden; text-overflow:ellipsis; white-space:nowrap`, plus a `title`
  carrying the full value.
- Below `sm`, hide all but the **last two** crumbs
  (`@media (max-width: 640px) { .cm-crumbs li:not(:nth-last-child(-n+2)) { display:none } }`).
  Two crumbs is "one level up + here", which is the whole navigational value at
  phone width. Do not collapse to an ellipsis crumb — that needs a popover, which
  needs a z-index slot (see §2.5).

All of this is **custom CSS in `app.css`, not Tailwind utilities** — see §5.3.

---

## 3. Copy inventory and reduction plan

### 3.0 Scale and method

A full sweep of `internal/web/*.go` (non-test) found **159 `h.P(` sites** and
**2,004 words** of literal `g.Text(".…")` copy (measured, not estimated;
interpolated/`fmt.Sprintf` strings are not counted, so the true total is higher).
The heaviest files by word
count: `cloud_pages.go` (233), `library_pages.go` (168), `workflow_open_comfy.go`
(143), `workflow_pages.go` (133), `run_resolve.go` (124), `run_preset_pages.go`
(123), `nodepack_pages.go` (121).

**A large fraction of that is load-bearing and is listed in §3.5 as untouchable.**
The proposals below are the residue: verbatim duplicates, paragraphs that narrate
a control sitting two lines away, and label vocabularies that have drifted.

Every row cites `file:line` at `main@e9c89f0`. **Line numbers will move** — see §5.1.

### 3.1 CUT — verbatim or near-verbatim duplicates

The safest class: the same sentence rendered twice, or a paragraph that restates
its own adjacent control's label.

| file:line | current text | action | rationale |
|---|---|---|---|
| `library_pages.go:494‑495` | "Matches your files against CivitAI by hash (sends file hashes to civitai.com). Uncheck to scan offline." | **cut the first sentence**, keep "Uncheck to scan offline." | The checkbox label two lines up (`:492`) already reads "Match against CivitAI (sends file hashes to civitai.com)". 🔴 **The egress clause must survive somewhere** — it does, in the label. Verified both strings live side by side. |
| `run_resolve.go:515` | "Matched from filename — verify this is the model you want." | **cut**, call the `:167` renderer | Byte-near-duplicate of `run_resolve.go:167` ("Matched from **the** filename …"). Two renderers, one message, already drifting by one word. |
| `run_resolve.go:591` | "Set comfy_model_path to install here." | **see §3.6 — unsure which half to cut** | Byte-identical to the `title=` on the disabled button at `:586`. |
| `run_pages.go:800` | "Set comfy_model_path to install this file here, or fetch it manually." | **same case as above** | Near-duplicate of the `title=` at `:795`. |
| `disks_pages.go:59‑61` | "Quarantining a model file MOVES it to the trash directory instead of deleting it. Every batch below is restorable to its original locations." | **cut** | Near-word-for-word duplicate of the empty-state explanation at `library_pages.go:1354‑1356`, and both render on `/disks` — the section paragraph when batches exist, the empty state when they don't. One message, keep the empty state's (it is the one a first-time user meets). |
| `scan_pages.go:33` | "Scan the selected directories for model files (opens the Model files tab)." | **cut** | Sits under a button reading "Scan for models". The parenthetical is the only new information and belongs in the button's own `title`. |
| `discover_pages.go:99‑100` | "Scan all disks for ComfyUI / Automatic1111 installs" | **cut** | Inline beside a button reading "Discover installs". |
| `library_pages.go:418‑419` | "Re-run the scan to pick up new, changed, or removed files." | **cut** | In a dialog titled "Scan for model files" whose submit reads "Scan for model files". |
| `library_pages.go:1219` | `Use "Quarantine all" to act on every candidate.` | **cut** | The "Quarantine all …" buttons are the next elements in the tree (`:1259`). |
| `cloud_pages.go:118‑121` | "Running on CivitAI's cloud is opt-in. Turn it on with the toggle above — no restart needed. You can also set `comfy_cloud: true` …" | **shorten** — drop sentence 1 and the "no restart needed" clause | The alert is already titled "Cloud run is off" and the toggle is directly above. **Keep the config-file-precedence sentence** — that is a real, non-obvious rule (`cloud_connect.go:223‑226` states it again for the opposite case). |
| `workflow_pages.go:226` vs `:443` | two paragraphs both saying "import an API/UI graph, extract one from a ComfyUI PNG, or scan your ComfyUI installs" | **cut the browse-surface blurb at `:226`**, keep the empty state at `:443` | The blurb renders on a populated list where the user has already done the thing it explains; the empty state renders exactly when it is needed. |
| `run_pages.go:58` / `:68` | `sectionTitle("Generate")` × 2 branches, directly above a button also reading "Generate" | **keep the h2, keep the button** | Considered and rejected: the h2 is the section's only heading, and the run panel's heading outline needs it. Noted so a later reader does not re-litigate. |
| `workflow_pages.go:1119` + `workflow_resources.go:315` | "Referenced resources" as both an `<h2>` and a popover title | **keep both** | Different surfaces (detail page section vs. list-card popover); they never co-render. |

**Subtotal: ~9 paragraphs / ~135 words removed, 0 information lost.**

### 3.2 SHORTEN — paragraphs that narrate a visible control

Lower confidence than §3.1; each needs a judgement call at implementation time.

| file:line | current text (abridged) | action | rationale |
|---|---|---|---|
| `library_pages.go:269‑270` | "Find and select the ComfyUI / Automatic1111 install directories to scan. Switch to “Model files” to scan them." | shorten to the second sentence | The first restates the tab name (`:221` "Install directories") and the "Discover installs" button below. The cross-tab pointer is the useful half. |
| `discover_pages.go:47‑48` | "No install directories yet — use “Discover installs” above to find them automatically, or add a path/browse below. Selected directories appear here and become scannable in the “Model files” tab." | shorten — drop the middle clause | Narrates three controls all visible on screen. |
| `library_pages.go:311‑312` | "Point the scanner at your model folders first. Add install directories, then come back here to scan them for model files and match your library against CivitAI." | shorten to sentence 1 + the CTA | Sentence 2 restates the CTA at `:318` ("Add install directories"). |
| `outputs_pages.go:362‑366` | "Images and videos captured from your successful workflow runs. These are your own local generations and render plain." | drop "and render plain" | "Render plain" is implementation vocabulary from the maturity work (`CLAUDE.md`, maturity invariant). The *behaviour* — outputs are exempt from the maturity band — is correct and load-bearing, but the user-facing phrasing does not communicate it. Replace with nothing, or with "not filtered by the maturity setting" if the disclosure is judged necessary. **Flag to the reviewer: this is the one §3.2 row that touches maturity semantics.** |
| `run_pages.go:70‑71` | "Runs this workflow on your local ComfyUI (local ComfyUI). UI-format graphs are converted to API format first; missing nodes or models are reported before anything is submitted." | drop sentence 1 | It restates the "Generate" h2 + button. Sentence 2 is real, non-obvious behaviour — **keep**. |
| `run_pages.go:716‑717` | "These saved values are no longer valid choices on your installed nodes. Pick a valid option for each, then run." | drop "then run" | Restates the "Run with selected options" submit at `:720`. |
| `run_resolve.go:452` | "Or handle them one at a time — pick a CivitAI match, or swap in a model you already have." | shorten to "Or handle them one at a time." | The rest narrates the two sections of the dialog it opens. |
| `run_resolve.go:635` | "These are already installed in ComfyUI and safe to run." | **keep** | Considered: it restates the h3 "Replace with a model from my library". But "safe to run" is a real reassurance about a substitution the user is being asked to trust. Not a cut. |
| `run_install_all.go:241‑243` | "Finds each file on CivitAI and downloads it into your ComfyUI models folder, then starts the run. If any of them cannot be matched, nothing is downloaded…" | drop the first clause only | 🔴 The second half is the **all-or-nothing guarantee** and is load-bearing (§3.5). |
| `run_preset_pages.go:553‑554` | "Workflow mode comes from the picker above — this preset pre-selected it when you opened the tab…" | **keep** | Explains a non-obvious interaction between two controls, not a restatement. |
| `nodepack_pages.go:63‑65` | "This workflow uses node types your ComfyUI does not have. Each one belongs to a custom-node pack — install the packs below…" | **keep** | 🔴 Nodepack copy is deliberately wordy because the detector is weak (`CLAUDE.md`). Do not touch. |
| `workflow_facets.go:505` | "Your library has workflows, just none matching this filter. Clear it, or find more on CivitAI." | shorten to sentence 1 | Sentence 2 restates the two CTAs at `:507`/`:509`. |
| `discover_facet_pages.go:119‑120` | "Nothing on CivitAI matches this combination. Widen the time window or drop a filter." | shorten to sentence 1 | Restates the 2–4 CTAs beneath. |
| `workflow_pages.go:1027` | "Leave both blank and submit to detach (also clears golden)." | **keep** | Non-discoverable behaviour of a form. Exactly the copy that earns its words. |
| `library_pages.go:900‑912` | four stat-chip popover explanations | **keep all four** | Each popover is opt-in (hover/focus) and explains a *count* whose derivation is genuinely non-obvious — especially `:900‑904` ("counted from cached model details only, so a freshly-scanned library can read low"), which pre-empts a "the number is wrong" bug report. |

**Subtotal: ~10 shortenings, ~120 words.**

### 3.3 REPLACE WITH AN ICON — proposals

**Constraints (from `CLAUDE.md` and `import_button_web_test.go:62‑75`):**
the `.cm-cta-icon` text-glyph vocabulary is exactly `→ ＋ ↗ ▶` and a test asserts
`⤓` is not in it. **Propose inline SVG, never a new glyph.** The existing SVG
vocabulary lives at `library_pages.go:123‑139` (`modelsIconSVG`,
`duplicateIconSVG`, `outOfDateIconSVG`, `unmatchedIconSVG`, `rescanIconSVG`,
`updateAvailableIconSVG`, `infoIconSVG`), `pages.go:712‑713`
(`downloadIconSVG`, `thumbsUpIconSVG`), `model_pages.go:1342` (`clockIconSVG`),
`workflow_resources.go:133` (`folderIconSVG`). All are feather-style,
`stroke="currentColor"`, `aria-hidden`, `focusable="false"`, sized by a `.cm-*`
class so no new Tailwind utility is needed.

**Accessible-name rule** (this repo's own convention, `workflow_resources.go:151‑159`,
`pages.go:697‑705`): an icon that *replaces* text must carry the name via
`aria-label` (and, where a tooltip is wanted, `title`). An `aria-label` beside
*visible* text is a smell — see §3.4.

| # | file:line | current | proposal | confidence |
|---|---|---|---|---|
| I1 | `model_pages.go:1633` (rendered once per file row, `:1585‑1604`) | `Download` text button | `downloadIconSVG` + `aria-label="Download {filename}"` | **High.** The glyph already exists, the row already names the file, and the label repeats N times in one menu. |
| I2 | `workflow_pages.go:843` | `Delete` | trash SVG (new) + `aria-label="Delete {workflow name}"` | **Medium.** Destructive; icon-only destructive controls are a known hazard. Only do this if it keeps the existing `hx-confirm`. |
| I3 | `workflow_pages.go:837/840` | `Set golden` / `Unset golden` | star SVG, filled ⇄ outline + `aria-label` | **Medium.** A star is unambiguous for "mark as canonical" only if the term "golden" is dropped everywhere; it currently also appears as a badge (`:732` `golden ✓`) and a metaRow (`:983`). Icon-only here without renaming the concept would make the term unlearnable. |
| I4 | `discover_pages.go:387` | `"📁 " + label` — **the only emoji still shipping in the UI** | `folderIconSVG` (`workflow_resources.go:133`) | **High.** `library_pages.go:114‑121` records that emoji were removed from the summary pills precisely because they "render at the mercy of the platform emoji font, ignore currentColor, and do not respond to the theme". This one survived the sweep. Note it sits on a `truncate` element with a `title` already (`:386`), so the SVG must not displace that. |
| I5 | `outputs_pages.go:499‑510` | `← Newer` / `Older →` (+ disabled `<span>` variants) | keep the words, keep the arrows | **Rejected.** `TestDisabledPaginationIsVisible` (`ux_audit_web_test.go:198`) pins the disabled variants; and pagination arrows without words are worse at small sizes. Recorded so it is not re-proposed. |
| I6 | `pages.go:418` | `Unsubscribe` in the subscriptions table row | **keep the text** | **Rejected.** The row's `hx-confirm` (`pages.go:416`) is the safety net, but an icon-only destructive control in a dense table is exactly where mis-clicks happen. |
| I7 | `workflow_pages.go:829` `View` / `:918` `View post` / `:165` + `:952` `View on CivitAI ↗` | four "view" verbs, one duplicated verbatim | **not an icon change — a vocabulary fix.** Standardise on `View on CivitAI ↗` for external and `View` for internal; delete one of the two `:165`/`:952` duplicates if they can co-render | **High value, low risk.** |
| I8 | `workflow_open_comfy.go:451/508/515/542/568` | "ComfyUI helper" repeated in the button, the aria-label, the summary, the consequence paragraph and the confirm | **do NOT icon-ify** | 🔴 The helper install/remove wording is load-bearing (§3.5). It already lives behind a collapsed "ComfyUI helper (advanced)" disclosure (`:482`) for a recorded reason (`CLAUDE.md`: an inline "Remove helper" button got clicked by a user who did not know it disabled the feature). Icon-only here would make an already-dangerous control less legible. |

**Realistic icon subtotal: I1 + I4 + I7 — ~3 changes, ~10 repeated words removed
per render on the model page's file menu.** The other five are recorded as
*considered and declined*, which is most of this section's value.

### 3.4 `aria-label` alongside visible text — 12 smells

The convention this codebase already documents (`layout.go:222‑226`,
`model_pages.go:571‑573`): **a visible text label IS the accessible name**; an
`aria-label` on top of it *replaces* the name the user can see, which breaks
voice control ("click Add a workflow" fails when the name is "Add a workflow —
paste JSON or upload a ComfyUI PNG").

| file:line | visible | aria-label | proposed |
|---|---|---|---|
| `workflow_pages.go:262‑265` | `＋ Add a workflow` | "Add a workflow — paste JSON or upload a ComfyUI PNG" | move to `title=`, drop `aria-label`. ⚠ **`e2e/uxaudit/walk.go:47` selects this button by `button[aria-label^="Add a workflow"]`** — the prefix still matches if the attribute is kept, but **the walk breaks if it is removed**. Update the selector in the same commit. |
| `workflow_pages.go:449‑452` | `＋ Add a workflow` | "Add your first workflow" | drop the `aria-label` |
| `workflow_pages.go:785‑787` | `▶ Run` | "Run {name} on your local ComfyUI" + a `title=` too | three strings on one control; keep `title`, drop `aria-label` |
| `workflow_open_comfy.go:698‑701` | `Open in ComfyUI ↗` | "Open this workflow in the ComfyUI editor" | drop the `aria-label` |
| `workflow_open_comfy.go:554‑555`, `:567‑568` | `Install/Uninstall ComfyUI helper` | "…the civitai-manager helper into/from ComfyUI" | drop |
| `model_card_pages.go:602`/`:612` | `Subscribe` | "Subscribe to this model" | drop |
| `model_card_pages.go:604` | `Notify me` | "Get notified about new versions of this workflow post" | move to `title=` |
| `model_card_pages.go:813`/`:816` | `Unsubscribe` | "Unsubscribe from this model" | drop |
| `model_pages.go:698‑711` | `Import workflows` / `View workflows` | "Go to the workflow import section" / "…imported from this model" | drop — the announced name shares **no word** with the visible label |
| `model_pages.go:1283‑1284` | `{N} older` | "Show {N} older versions" | drop |
| `library_pages.go:415‑416` | `✕` (**not** aria-hidden) | "Close" | add `aria-hidden` to the glyph — the ✕ is currently exposed text under an overriding label |
| `outputs_rail.go:295‑310` | vertical `Recent outputs` (aria-hidden) | "Expand recent outputs" + identical `title` | three labels on one button; keep `aria-label`, drop the duplicate `title` |
| `outputs_rail.go:231‑249` | `<aside aria-label="Recent outputs">` whose first child link reads "Recent outputs" | — | AT announces it twice. Drop the aside's label (the heading link names the region) **only if** the rail keeps a heading after the `feat/app-shell` rework — ⚠ it is becoming a multi-widget container, so **defer this one entirely**. |

**These are the highest value-per-word changes in the whole sweep**: 11 attribute
deletions, no visible text lost, and each one restores a broken accessible name.

### 3.5 🔴 KEEP — the load-bearing register

**Read this list before deleting anything.** Each entry has a recorded reason in
`CLAUDE.md` or in the code's own comment block. Proposing to cut any of these
requires arguing against that reason explicitly.

**Egress disclosures (data leaves the machine):**
- `library_pages.go:492` — "Match against CivitAI (sends file hashes to civitai.com)" *(checkbox label — this is the one that survives the §3.1 cut)*
- `scan_pages.go:35` — "…(sends file hashes to civitai.com). Turn off “Match against CivitAI” on the Model files tab to scan offline."
- `pages.go:575` — "Search CivitAI for models to subscribe to. Your query is sent to civitai.com."
- `discover_workflows.go:133` — "…Your search is sent to civitai.com." — the comment at `:122‑132` records that this is the **only** egress statement on that surface after the import half was removed by explicit request.
- `model_related_workflows.go:495‑497` — "Importing downloads the workflow zip with your token and stores each workflow locally." — `discover_workflows.go:129‑132` says in as many words: *do not remove it as "duplicate copy"*.
- `discover_apps.go:143‑144` — filters sent to civitai.com + external launch
- `nodepack_pages.go:110‑118` — both states naming `api.comfy.org`, `raw.githubusercontent.com`, `resolve_node_packs: false`
- `cloud_pages.go:91‑93` — "Sends data to civitai.com + spends Buzz"

**Buzz / money:**
- `cloud_connect.go:181‑184` — "That token is also what SPENDS your Buzz… the confirmation on the run button is the only one you get."
- `cloud_pages.go:350‑352` (per-second billing), `:367‑372` (insufficient Buzz), `:392` ("Run for real (spends Buzz)"), `:406‑410` + `:428` (5-min minimum), `:448‑450` ("Buzz may still have been charged")

**Subscribe explanation + download-size disclosure (v0.1.97):**
- `model_card_pages.go:667‑705` — "What subscribing does" / "What this does", both arms
- `subscribe_disclosure.go:165‑206` — "Nothing is downloaded now: the versions that already exist are marked as seen, not fetched." + `autoDownloadSentence`'s four variants (unknown size / over cap / known size)

**Nodepack blocker (headline conditional, caveat tier-agnostic):**
- ⚠ This entry used to read "detector is deliberately weak" and cited the "flagged by a short list of known built-in node types" caveat as design intent. **Both are stale.** `comfy.NodeOrigins` now classifies on the registering `python_module` from a cached/freshly-fetched `/object_info` and is authoritative whenever one exists; `coreNodeClasses` is only the fallback tier. The mechanism clause was deleted from the copy because it was false on a warm cache.
- `cloud_pages.go` `cloudNodepackBlocker` — the conditional headline, the CustomComfy/snapshot explanation, and the tier-agnostic "this app may not recognise every built-in node type" caveat. It states NO mechanism on purpose: the function receives only `[]ResolvedResource` and cannot know which tier answered, and the caveat has to stay true in both (cold cache → a 47-of-790 table; warm cache → authoritative about the LOCAL install, while the banner is about CivitAI's REMOTE runner). A tier-aware rewrite means plumbing provenance down from the handler and was deliberately deferred.
- `nodepack_pages.go:82‑83`, `:147‑148`, `:219`, `:356‑357`, `:478‑480`

**Install-and-run substitution offer (must name BOTH files):**
- `run_download.go:533‑538` — the offer paragraph naming `requested` and `remote`
- `run_resolve.go:224` — the button `"Install {remote} as {requested}"`
- `run_install_all.go:241‑243` (second half), `:402‑405`, `run_download.go:1006` — the all-or-nothing scoping

**"Open in ComfyUI" / helper:**
- `workflow_open_comfy.go:366`, `:376` (zombie), `:387` (the literal `Workflows → …` path — the whole point of the fallback), `:443`/`:499` (foreign directory), `:450`, `:514`, `:524‑530`, `:539`, `:541`, `:566`

**Loopback gating notices:** `library_pages.go:260‑261`, `library_handlers.go:448‑450`,
`disks_pages.go:73‑75`, `workflow_pages.go:255`, `run_pages.go:60`.

**Guided empty states:** all four `emptyState(...)` calls
(`disks_pages.go:78`, `library_pages.go:1352`, `outputs_pages.go:367`, `:477`)
plus the two hand-rolled clones (`library_pages.go:308‑319`,
`discover_facet_pages.go:116‑122`). `TestEmptyStatesGuideTheUser`
(`ux_audit_web_test.go:754`) asserts a heading **and** an explanation paragraph
**and** a real button CTA, and explicitly checks that the old bare
`"Trash is empty."` / `"No results."` strings have not returned. **A "shorten the
empty state" proposal fails that test by design.**

**Honesty copy about our own data:** `outputs_pages.go:404` (partial batch),
`:430‑439` (`batchParamsNote` — the comment records two earlier phrasings that
were *false*), `:628‑631` (pre-capture prompts), `:806‑813` (bypassed models),
`library_pages.go:900‑904` (cache-derived count reads low),
`run_preset_pages.go:502‑506` (identity- not position-matching).

### 3.6 Cases I could not resolve — decide at implementation time

1. **`run_resolve.go:591` / `run_pages.go:800` — which half of the duplicate goes?**
   The visible `<p>` and the `title=` on the *disabled* button carry the same
   string. Deleting the `<p>` looks like the tidier cut, **but a `title` on a
   `disabled` button is not reliably reachable**: disabled buttons are not
   focusable, so keyboard and screen-reader users may never get the tooltip, and
   the `<p>` may be the only accessible copy. I did not verify this in a browser.
   **Recommendation: delete the `title=`, keep the `<p>`** — but verify with the
   `browser` skill before committing either way.
2. **`library_pages.go:1290` — "Restore from the Trash page."** is **stale**:
   `/trash` is a 302 into `/disks` (`disks_handlers.go:102`) and the nav has no
   Trash entry. This is a copy *bug*, not fluff. Should read "Restore from Disks."
   I have not checked whether a test pins the string.
3. **`outputs_pages.go:481`** — the batch empty state's CTA is labelled
   `"Back to all outputs"` but the shared `emptyState` helper prefixes it with
   the forward `→` glyph (`layout.go:474`). The arrow points the wrong way. Fixing
   it means either a per-call icon override on `emptyState` (new API surface) or
   relabelling the CTA to "All outputs". **Relabel — do not add a parameter.**
4. **Two h1 type scales coexist.** `pageTitle` is `text-lg` (`layout.go:448`)
   while the three outputs h1s and the workflow/model h1s are hand-rolled
   `text-xl`/`text-2xl`. Unifying them is a visual change with no copy component,
   and `TestPageTitleAndSectionTitleAgreeVisually` (`ux_audit_web_test.go:169`)
   pins `pageTitle`'s classes to `sectionTitle`'s. Out of scope here; flagged
   because breadcrumbs sit directly above these headings and the inconsistency
   will be visible.
5. **`discover_workflows.go` says "Discover workflows" in its h1 while the nav
   entry that opens it says "Find workflows"** (`layout.go:136`). `TestNavbarLabels`
   pins the nav label. One of the two should move. I do not know which the user
   prefers — **present the choice rather than picking**.

### 3.7 Estimated reduction

| Class | Count | Words removed | Confidence |
|---|---|---|---|
| §3.1 verbatim/near-duplicate paragraphs | ~9 | ~135 | High |
| §3.2 shortenings | ~10 | ~120 | Medium |
| §3.3 icon replacements (I1, I4, I7) | 3 | ~10 per render ×N rows | High |
| §3.4 `aria-label` attribute deletions | 11 | ~70 (attribute text) | High |
| §2.3 back-links + wrappers deleted by breadcrumbs | 4 anchors + 3 divs | ~14 | High |

**Total: roughly 340 words and ~17 elements, against 2,004 words of literal UI
copy — a ~17% reduction, concentrated in the surfaces that had duplicated
themselves rather than in the ones that explain something.** That is the shape
you want: the register in §3.5 is deliberately larger than the cut list.

---

## 4. Sequencing

### 4.1 Order

1. **Wait for `feat/app-shell` to merge.** Non-negotiable — see §5.1.
2. **Re-derive every line number in this document** against the post-merge tree.
   Treat the `file:line` citations here as *pointers to the right paragraph*, not
   as addresses.
3. **Pass A — `aria-label` smells (§3.4).** Smallest diff, highest a11y value, no
   visual change, no new CSS. Ship first so the rest builds on a clean base.
   ⚠ Update `e2e/uxaudit/walk.go:47` in the same commit.
4. **Pass B — copy cuts (§3.1, then §3.2).** Pure deletions. §3.1 first (safe),
   §3.2 second (judgement).
5. **Pass C — breadcrumbs (§2).** New file `internal/web/breadcrumbs.go` + new
   `.cm-crumbs*` CSS in `app.css` + the four back-link deletions.
6. **Pass D — icons (§3.3).** Last, because I4 (the 📁 emoji) touches a
   `truncate` element that the title sweep guards.

Each pass is independently revertable. Do **not** combine B and C: if a copy cut
turns out to be wrong, you want to revert it without also reverting the trail.

### 4.2 Tests that will need updating

| Test | file | Why |
|---|---|---|
| `TestEveryFullPageHasExactlyOneH1` | `ux_audit_web_test.go:144` | Must stay green. The trail emits no `h*`, so it *should* pass unchanged — **but assert that deliberately**, and see §6.1 for a pre-existing violation this test cannot currently see. |
| `TestEmptyStatesGuideTheUser` | `ux_audit_web_test.go:754` | Pins the `trash` empty state via `trashPage`, which §1.3/O1 recommends deleting. If `trashPage` goes, re-point at `disksPage` (already a second case in the same table). |
| `TestEveryTruncatedTextHasATitle` | `ux_audit_web_test.go:677` | Sweeps every non-test `.go` in the package. `breadcrumbs.go` is a new file and is **not** exempt. Also relevant to icon change I4. |
| `TestDisabledPaginationIsVisible` | `ux_audit_web_test.go:198` | Blocks icon proposal I5. Already declined. |
| `TestNavbarLabels` | `nav_labels_web_test.go:23` | Pins "Find models"/"Find workflows"/"Apps"/"Library"/"Disks" **and** asserts the removed labels stay gone. §3.6 item 5 (the "Discover workflows" vs "Find workflows" mismatch) lands here. |
| `TestNavLibraryDropdown` | `nav_labels_web_test.go:114` | Untouched by this work; listed because `feat/app-shell` already edits this file. |
| `TestOutputsStaysReachableFromTheRail` | `nav_reachability_web_test.go:18` | Asserts the rail's two `/outputs` links by exact class. Unaffected by breadcrumbs but will be re-touched by the rail move. |
| `TestEveryTemplateClassExistsInAStylesheet` | `class_coverage_web_test.go:74` | **Every new `.cm-crumb*` class must have a rule in `app.css` or `output.css` or this fails.** |
| `TestClassCoverageBlindSpotsAreBounded` | `class_coverage_web_test.go:113` | Pins `classCoverageOpaqueBudget = 4` **exactly** — it fails if the count goes *down* too. Build the breadcrumb classes from literals/consts so the number does not move. |
| `TestImportButtonCopy` (glyph vocabulary) | `import_button_web_test.go:62‑75` | Asserts `⤓` is not in `.cm-cta-icon`. Any new glyph proposal dies here — which is why §3.3 proposes SVG. |
| `batch_gallery_web_test.go:100` | — | Asserts the literal `"← All outputs"` **and** `href="/outputs"`. §2.3 deletes that anchor. **This test must be rewritten to assert the breadcrumb instead.** It is the only test in the package asserting a back-link string. |
| `contrast_web_test.go` | — | **Only if a new coloured pair appears.** The trail should reuse the existing link colours (`text-indigo-400 hover:text-indigo-300`) and the muted `text-slate-400`, both already in the table. If a new muted tone is introduced, add the pair — and note the light theme is being retired (§5.1), which will reshape this file's 25 debt entries. |
| `e2e/uxaudit/walk.go:47` | — | The `button[aria-label^="Add a workflow"]` selector. §3.4 row 1. |

Tests asserting strings proposed for change: only `batch_gallery_web_test.go:100`
turned up in a package-wide grep for the back-link labels. The §3.1/§3.2
paragraph cuts should be re-grepped per string at implementation time — several
test files carry 25–60 `strings.Contains` assertions each
(`workflow_c1_web_test.go` 60, `workflow_open_comfy_web_test.go` 58,
`run_install_all_web_test.go` 57).

### 4.3 Tailwind is a purged static build

`internal/web/assets/output.css` is a committed Tailwind v3.4.17 build with a
content glob of `./*.go`. **A new utility class in an `h.Class("…")` string is
unstyled until the build is regenerated**, and `TestEveryTemplateClassExistsInAStylesheet`
will fail loudly rather than shipping it silently.

For breadcrumbs, **do not add Tailwind utilities at all** — write `.cm-crumbs`,
`.cm-crumb`, `.cm-crumb-name` as custom CSS in `internal/web/assets/app.css`,
which is served as-is and survives the purge. That is the established pattern for
`.cm-*` (`CLAUDE.md`; `library_pages.go:117‑121` says the same about icon sizing).
Same for the icon size classes in §3.3.

If a utility class does become necessary:

```sh
cd internal/web && nix-shell -p tailwindcss --run \
  "tailwindcss -c tailwind.config.js -i input.css -o assets/output.css --minify"
```

### 4.4 Verification

- HTTP-level GET reproduction against a dogfood instance for every trailed page
  (server-side markup).
- 🔴 **Browser verification is required for the breadcrumbs**, not optional.
  v0.1.82 was a pure rendering bug that passed every server-side test. Use the
  `browser` skill against the user's Brave, and specifically:
  hit-test the trail at 390px to confirm the ellipsis behaviour, and confirm the
  trail is not covered by the sticky nav at scroll position 0.
- `AUDITLOOP_CHROMIUM=/run/current-system/sw/bin/brave make ux-audit` for the axe
  delta — but note 9 of the affected surfaces have **no baseline** (§1.4).

---

## 5. Risk register

### 5.1 🔴 What `feat/app-shell` invalidates

The branch exists locally with **one commit so far** (`1b516b0`, "retire the light
theme from the UI, keep its CSS dormant"); the maturity-popover and left-rail work
described in the brief is **not yet on it**. So the invalidation below is a
*lower bound* — more is coming.

**Already on the branch (34 files, +297/−254):**

| What changed | Impact on this document |
|---|---|
| **`page()` loses its `theme` parameter** — `page(title, theme, csrf, mr, rail, …)` → `page(title, csrf, mr, rail, …)` (`layout.go:34`) | **Every page-builder call site changes.** This is why §2.5 puts breadcrumbs in a new file rather than threading a `[]crumb` through `page()`: two independent signature changes to one function is the worst possible merge. |
| `<html data-theme>` pinned to a new `shellTheme` const; `themeToggle` and `currentTheme()` removed from the nav/handlers | The light theme is going. **`contrast_web_test.go`'s 25 light-theme debt entries are in the blast radius** — do not plan against them. `theme_web_test.go` already gains ~150 lines on the branch. |
| Files already edited on the branch that this plan also touches | `handlers.go`, `layout.go`, `library_pages.go`, `library_handlers.go`, `model_pages.go`, `outputs_pages.go`, `outputs_handlers.go`, `disks_pages.go`, `discover_apps.go`, `discover_workflows.go`, `workflow_pages.go`, `workflow_open_comfy.go`, `workflow_handlers.go`, `pages.go`, `server.go` — **i.e. essentially the whole surface of both passes.** |
| Test files already edited | `ux_audit_web_test.go`, `nav_labels_web_test.go`, `library_web_test.go`, `library_tabs_web_test.go`, `outputs_rail_web_test.go`, `maturity_control_web_test.go`, `dashboard_search_web_test.go`, `discover_web_test.go`, `web_test.go`, `workflow_c1_web_test.go`, `workflow_open_comfy_web_test.go`, `theme_web_test.go`, `popover_controller_web_test.go`, `tab_controls_ux_web_test.go`, `subscribe_state_reflect_web_test.go`, `brand_web_test.go` — **every test named in §4.2 that lives in one of these files will have moved.** |

**Announced but not yet on the branch — treat every claim about these as unverified:**

| Coming change | What it invalidates here |
|---|---|
| Maturity `<select>`s → icon-button popover + two-sided slider | `layout.go:186‑231` (`maturityControl`, `maturityEnd`) is replaced wholesale. **Its copy is out of scope for the copy sweep.** A popover also means a new z-index or top-layer decision — check the STACKING ORDER ledger (`app.css:2104‑2135`) before the trail claims any budget. |
| Outputs rail moves **right → left**, becomes a multi-widget container | §2.5's alignment claim (`<main>`'s left edge sets the trail's left edge) is **unverified against the final shell**. Re-check. §3.4's last row (the `<aside aria-label>` duplication) is **deferred entirely** — the rail may not have that heading any more. §1.3/O2 (the `/outputs` orphan) may be *fixed* or *worsened* by the rail becoming a widget container; re-derive the reachability claim. |
| `dashboardPage`'s activity feed + queue "are slated to become left-sidebar widgets" (`pages.go:91‑93`) | `/subscriptions` loses two of its four cards. Any copy proposal touching `pages.go:133‑151` ("Download queue" / "Activity") is likely moot. |

**Concrete instruction to the implementer: do not trust this document's
`file:line` citations. Re-grep each proposed string on the post-merge tree, and
re-run §1's route enumeration against the post-merge `server.go` before
implementing §2.**

### 5.2 Other risks

- **The breadcrumb scope is deliberately small (4 pages).** If the reviewer
  expected "breadcrumbs everywhere", §2.1 is the argument for why that would be a
  regression. Surface that disagreement *before* implementing, not after.
- **`/outputs/batch/{id}` is missing from `fullPages()`** (`ux_audit_web_test.go:73‑109`)
  — the batch page is not in the heading audit at all, and it is one of the four
  pages getting a trail. Add it to `fullPages` **before** touching it, so the h1
  assertion actually covers the change.
- **The `📁` → SVG swap (I4)** sits on a `truncate` + `title` element
  (`discover_pages.go:385‑387`). Both guards apply; do it last.
- **Two of the four trails carry a preset/workflow name of unbounded length.**
  The h1 already has a documented phone-overflow bug class here
  (`outputs_pages.go:459‑464`, `TestLongUntrustedStringsCanBreak`). A trail is a
  *second* place to make the same mistake.

---

## 6. Pre-existing bugs found while surveying

Not proposals — live defects, found while reading, both relevant to this work.

### 6.1 🔴 `/models/{id}` renders TWO `<h1>` elements in production

**Reproduced live** against the dogfood instance on `:8972` (v0.1.97):

```
$ curl -s http://127.0.0.1:8972/models/4384 | grep -o "<h1[^>]*>[^<]*"
<h1 class="text-xl font-semibold">DreamShaper
<h1 id="heading-133">DreamShaper - V∞!
```

The second `<h1>` comes from the **sanitized model description**.
`internal/web/sanitize.go:14` uses `bluemonday.UGCPolicy()`, which permits
`h1`–`h6`, and CivitAI descriptions routinely contain headings. `/models/257749`
renders one h1; `/models/4384` renders two — so it depends on the description,
which is exactly why it has never been caught.

`TestEveryFullPageHasExactlyOneH1` (`ux_audit_web_test.go:144`) cannot see it: its
`model` fixture (`:65‑67`) has an empty `Description`, so
`modelDetailPage`'s description card is never rendered
(`model_pages.go:623` guards on `strings.TrimSpace(v.Description) != ""`).

**Why it matters here:** the heading-outline invariant is the exact constraint
§2.4 designs around, and it is already violated on the app's richest page. Fix:
add a description-carrying fixture to `fullPages`, and either demote description
headings in the sanitizer policy (`h1`→`h2`, or strip `h1`) or accept and document
the exception. **I did not verify which fix bluemonday supports cleanly.**

### 6.2 `versionStatusFragment` breaks the repo's own popover/`title` rule

`model_card_pages.go:284‑291` sets **both** `title="Update available: {latest}"`
**and** renders a `.cm-vstatus-pop` custom popover on the same `<span>`. The
codebase forbids this (`model_pages.go:522‑530`: "an element that owns a custom
popover must NOT also carry `title=`" — the native tooltip races the custom one).
`popover_no_title_web_test.go` guards `updatedHeaderStat`, `updatedCardLine`,
`versionDatePopover`, `comfyStatusIcon`, `workflowResourcesPopover` and
`versionTab` — but **not** `versionStatusFragment`, so this one slipped the net.
It also carries an `aria-label` over visible badge text (§3.4 class).

### 6.3 `handleTrash` / `trashPage` are unreachable dead code

See §1.3/O1. `handleTrash` (`library_handlers.go:619`) has no `mux.HandleFunc`
in `server.go`; `GET /trash` is served by `handleTrashRedirect` instead. The page
is still rendered and asserted by two tests.

### 6.4 Stale copy: "Restore from the Trash page."

`library_pages.go:1290`. There is no Trash page — `/trash` 302s to `/disks` and
the nav entry was removed. See §3.6 item 2.

