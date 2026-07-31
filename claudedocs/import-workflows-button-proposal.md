# Proposal — turn "Import workflows" into an icon+text button

**Status: RESOLVED and SHIPPED in v0.1.89.** Option C was chosen, with three
amendments made after checking the proposal against the code. Kept for the
reasoning; see the amendments at the bottom before treating any markup here as
what shipped.

Surface: `internal/web/model_pages.go` → `workflowImportDetailCard`, the
`imported == 0` branch. Screenshots were taken against a real model page
(`/models/1818841`, a Workflows-type model with nothing imported yet) by
injecting each option into the live DOM in Brave, both themes, then removing the
probe. Nothing was committed to the renderer.

## What is actually there today

```html
<div data-civitai-ui="card" data-with-border="true" data-padding="md">
  <h2 class="text-sm font-semibold text-slate-300 mb-2">Import workflows</h2>
  <div id="wf-import-1818841" class="flex flex-col gap-1">
    <button data-civitai-ui="button" data-variant="filled" data-size="sm" type="button"
            hx-post="/workflows/discover/1818841/import"
            hx-vals='{"csrf_token":"…"}'
            hx-target="#wf-import-1818841" hx-swap="innerHTML" hx-disabled-elt="this">
      Import workflow(s)
    </button>
  </div>
</div>
```

Two observations worth having before choosing:

1. **The action is labelled TWICE.** The `<h2>` says "Import workflows" and the
   button underneath says "Import workflow(s)". For a card whose entire content
   is one button, the heading is chrome restating the button.
2. **The button is already full-bleed**, and not by design — its container is
   `flex flex-col`, so the button stretches to the card width. That is why it
   currently reads as a banner rather than a button. Any option that keeps that
   container inherits it.

There is **no `<form>` here** — it is an htmx `<button type="button">`. So
"keeps the form keyboard-operable" reduces to: stay a real `<button>`, keep it in
tab order, keep an accessible name. All three options do.

---

## Option A — one icon+text button, heading removed

```html
<div data-civitai-ui="card" data-with-border="true" data-padding="md">
  <div id="wf-import-1818841" class="flex flex-col items-start gap-1">
    <button data-civitai-ui="button" data-variant="filled" data-size="md" type="button"
            hx-post="/workflows/discover/1818841/import"
            hx-vals='{"csrf_token":"…"}'
            hx-target="#wf-import-1818841" hx-swap="innerHTML" hx-disabled-elt="this">
      <span class="cm-cta-icon" aria-hidden="true">⤓ </span>Import workflows
    </button>
  </div>
</div>
```

- **Accessible name:** the visible text. No `aria-label` needed.
- **New CSS:** none. `.cm-cta-icon` already exists in `app.css` and is used by
  the "View in library" / "Run" / "Add a workflow" CTAs.
- **New Tailwind:** `items-start` is the only addition, and it is already in the
  purged `output.css` — no rebuild. (Without it the button stays full-bleed, as
  it is today.)
- **Semantics / keyboard:** unchanged — same `<button type="button">`, same htmx
  attributes, same tab stop. One fewer heading in the document outline.
- **Cost:** the section loses its heading. On this page that is fine — the card
  sits directly under the download card and its own label is now the button.

Screenshot: `OPTION A` row in `prop-light.png` / `prop-dark.png`.

## Option B — icon-only button + visible adjacent label

```html
<div data-civitai-ui="card" data-with-border="true" data-padding="md">
  <div class="flex items-center gap-2">
    <h2 class="text-sm font-semibold text-slate-300">Import workflows</h2>
    <div id="wf-import-1818841" class="flex flex-col gap-1">
      <button data-civitai-ui="button" data-variant="filled" data-size="sm" type="button"
              aria-label="Import workflows from this model into your library"
              title="Import workflows from this model into your library"
              hx-post="/workflows/discover/1818841/import"
              hx-vals='{"csrf_token":"…"}'
              hx-target="#wf-import-1818841" hx-swap="innerHTML" hx-disabled-elt="this">⤓</button>
    </div>
  </div>
</div>
```

- **Accessible name:** `aria-label` (mandatory — the glyph is `aria-hidden` to
  nobody, it is the whole content, and a lone `⤓` announced literally is useless).
- **New CSS / Tailwind:** none.
- **Semantics / keyboard:** unchanged. The `<h2>` is NOT a `<label>` and does not
  programmatically name the button, which is why the `aria-label` carries the
  full sentence rather than repeating "Import workflows".
- **Cost:** the smallest tap target of the three, for the section's only and
  most consequential action (it downloads a zip from civitai.com using your
  token). An icon-only control also has to teach `⤓` = import; the app has no
  established icon vocabulary to lean on.

Screenshot: `OPTION B` row.

## Option C — compact inline: section heading left, icon+text button right

```html
<div data-civitai-ui="card" data-with-border="true" data-padding="md">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h2 class="text-sm font-semibold text-slate-300">Workflows</h2>
    <div id="wf-import-1818841" class="flex flex-col gap-1">
      <button data-civitai-ui="button" data-variant="filled" data-size="sm" type="button"
              hx-post="/workflows/discover/1818841/import"
              hx-vals='{"csrf_token":"…"}'
              hx-target="#wf-import-1818841" hx-swap="innerHTML" hx-disabled-elt="this">
        <span class="cm-cta-icon" aria-hidden="true">⤓ </span>Import workflows
      </button>
    </div>
  </div>
</div>
```

- **Accessible name:** the visible text.
- **New CSS / Tailwind:** none. `flex flex-wrap items-center justify-between
  gap-2` is the exact class string `showcaseCard` already uses for its header
  row, so this reuses an established pattern rather than inventing one.
- **Semantics / keyboard:** unchanged.
- **Cost:** the heading changes from "Import workflows" to **"Workflows"**, which
  is deliberate and is the real win: it then matches the heading the *imported*
  state already uses. Today the card is titled "Import workflows" before import
  and "Workflows" after, so the section appears to rename itself. On a narrow
  viewport `flex-wrap` drops the button to its own line — already handled.

Screenshot: `OPTION C` row.

---

## Recommendation: **Option C**

Reasoning, in order of weight:

1. **It fixes a real inconsistency, not just the button.** The two states of this
   one card are titled differently today ("Import workflows" → "Workflows").
   Option C makes the heading stable and lets the *button* carry the verb, which
   is what a heading and a button are each for.
2. **It keeps a full text label on a consequential, irreversible-ish action.**
   This button reaches out to civitai.com with the user's token and writes rows
   into the local library. Option B shrinks precisely that control to a glyph.
3. **It costs nothing.** No new CSS, no new Tailwind class, no purge rebuild, no
   change to the htmx contract or the tab order — and it reuses `showcaseCard`'s
   existing header-row class string, so it cannot drift into a one-off.
4. **Option A is the honest runner-up** and is better than today. I rank it second
   only because dropping the heading leaves the section with no stable name, and
   the "already imported" state still has one — so the inconsistency in (1)
   survives in a different form.

**Against Option B:** it is the only one needing an `aria-label` to be usable at
all, it gives the page's most network-consequential button the smallest hit area,
and `⤓` has no precedent in this UI to make it self-evident.

One thing to decide alongside whichever you pick: the button's label is currently
`Import workflow(s)`. If the button becomes the primary label, **`Import
workflows` reads better than `Import workflow(s)`** — the parenthetical plural is
hedging about a count the user cannot know yet, and the result line reports the
real number afterwards anyway. All three mock-ups above use `Import workflows`.


---

## RESOLUTION (v0.1.89) — Option C, amended

Shipped, with three corrections to what is written above:

1. **The heading is `Workflows from this model`, not a bare `Workflows`.** This
   proposal was written in PARALLEL with the workflow-discovery branch, so it could
   not see that a model page now carries THREE sections whose headings begin with
   "Workflows": `Workflows that use this model` (local library, matched by file) and
   `Workflows for <ecosystem>` (remote, by base model) both landed in v0.1.88. A bare
   "Workflows" would have been the ambiguous one of three siblings — verified live on
   `/models/1386234`, which renders "Workflows from this model" directly alongside
   "Workflows for SDXL family".
2. **The glyph is `＋`, not `⤓`.** The proposal argues against its own Option B partly
   because "`⤓` has no precedent in this UI to make it self-evident" — then uses `⤓`
   in Options A and C. It appears **zero** times in the codebase; the `cm-cta-icon`
   vocabulary is `→` (navigate ×4), `＋` (add ×2), `↗` (external), `▶` (run). Importing
   into your library IS adding, and `＋` is already the "Add a workflow" glyph.
3. **The header row keeps `mb-2`.** Both existing uses of that class string carry it;
   the markup above dropped it. If the argument is "reuses an established pattern
   rather than inventing one", reuse it verbatim.

Accepted as written: Option C over A (the card sits among siblings that all have
headings, and A leaves the section unnamed while the imported state has a name),
Option B rejected (only one needing an `aria-label` to be usable, smallest hit area
for the page's most network-consequential control), and `Import workflow(s)` →
`Import workflows`.

**One consequence the proposal did not call out:** `workflowImportAction` is SHARED
with the discover cards, so the label and glyph changed on that surface too. That is
consistent rather than a regression, but it was not a model-detail-only change.

Guarded by `internal/web/import_button_web_test.go` — the stable heading across both
states, the anti-collision check on the heading, the `＋`-not-`⤓` glyph, and the
hx-swap contract surviving the move into the header row. The first two were
mutation-verified.
