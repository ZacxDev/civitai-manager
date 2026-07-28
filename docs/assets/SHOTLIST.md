# Screenshot shot list

Exact filenames expected by [`../index.html`](../index.html) and the root
[`README.md`](../../README.md). Drop the PNGs in this directory using these
names and both surfaces pick them up with no edits.

Slots are ordered by importance. **1–4 are the ones that carry the pitch**; 5–8
are supporting. If you only capture four, capture the first four.

## Before you capture — read this

- **The "Recent outputs" rail is on every page.** It renders your most recent
  generations and their workflow names in the sidebar of *every* screen, so it
  will appear in the background of nearly every shot. Check it before you press
  the shutter, or turn it off (the outputs-rail toggle) for the capture session.
- **NSFW previews are blurred by default, not hidden.** Blur is a browser-side
  CSS filter. A screenshot of a blurred thumbnail is still a screenshot of that
  image, softened — don't rely on it for anything you wouldn't post.
- **Scrub identifying data**: your CivitAI username, local filesystem paths that
  contain your real name, and anything in the queue you'd rather not publish.
- **Capture at ~1440px wide**, in the **dark** theme unless a slot says
  otherwise (dark is what the landing page hero is designed around). Retina/2x is
  welcome; the page scales images down responsively.
- **Trim aggressively.** Full-page shots at 1440×3000 read as a grey smear on a
  phone. Crop to the region the caption is talking about, plus enough chrome
  (nav, page title) to locate it.
- Keep each file **under ~400 KB** where you can — the landing page has no
  lazy-loading beyond the browser default and these are committed to the repo.

---

## 1. `hero-run-missing-models.png` — **the hero**

**Used in:** landing page hero, README top.

**Must be visible:**
- A workflow's run panel reporting **missing models**, with at least one CivitAI
  match card and its install/"Download & run" action.
- Ideally an "Other possible matches" alternate or an installed-substitute option
  in the same frame, so the multi-match nature is legible.
- The workflow name, so it's obvious this is a real imported workflow.

**Why it earns its place:** this is the entire pitch in one frame. Every other
screenshot is a feature; this one is the reason someone keeps reading. A ComfyUI
user who has ever stared at a red "value not in list" error recognises this
instantly.

**Do not** capture this with zero missing models — a green "ready to run" panel
proves nothing and wastes the most valuable slot on the page.

---

## 2. `run-downloading.png`

**Used in:** landing page "how it works" step 2, README workflow section.

**Must be visible:** the run status mid-flight showing the download phase —
the "Downloading …" / "Download complete — starting run…" progress text, in the
same panel the run status streams into.

**Why it earns its place:** it is the *proof* of the specific claim that
distinguishes this tool: the run **waits** for the fetch instead of failing and
making you re-click. Without this frame, "it downloads the models" reads like
every other downloader. With it, the sequencing is self-evident.

---

## 3. `run-params.png`

**Used in:** landing page features, README workflow section.

**Must be visible:** the editable run-parameters panel with several real fields
populated — prompt, seed, steps, cfg, sampler, scheduler, and the
width/height/batch-size group.

Include the note that says the edits apply to **this run only** and the saved
workflow is unchanged, if it fits in the crop. If a field shows its
"from #N … widget (via …)" provenance line, even better — that's the
converted-widget resolution being visible.

**Why it earns its place:** it answers "so do I have to open ComfyUI to change
the prompt?" before the reader asks. The non-mutation note is also a trust
signal — it shows the tool is careful with your files.

**Caveat to respect in the caption:** this panel only appears for **UI-format**
workflows.

---

## 4. `outputs-gallery.png`

**Used in:** landing page features, README workflow section.

**Must be visible:** the Outputs gallery grid with several generations, and
enough of a detail/hover state that it's clear each one carries its parameters.
A visible re-run affordance is a bonus.

**Why it earns its place:** it's the "and your results don't evaporate" payoff,
and it's visually the most attractive screen in the app — it does the work of
making the project look finished. Use your own SFW generations.

---

## 5. `preflight-options.png`

**Used in:** README workflow section.

**Must be visible:** the incompatible-options section — a bad option with its
current (invalid) value and the dropdown of valid choices from your installed
node, plus the "Run with selected options" action.

**Why it earns its place:** it covers the *second* most common ComfyUI failure
after missing models — a saved sampler/scheduler/enum value that your install
doesn't have. It shows the preflight is a real diff against `/object_info`, not a
guess.

---

## 6. `library-candidates.png`

**Used in:** landing page second value prop, README library section.

**Must be visible:** the Library page after a scan, showing candidates flagged as
**duplicate / superseded / broken** — ideally at least two different reasons in
one frame — and the quarantine action.

**Why it earns its place:** it is the whole "it also manages your library" half
of the project in one image, and the three reason labels communicate the value
faster than a paragraph. Make sure a "nothing is hard-deleted / quarantine is
reversible" affordance is legible if one is on screen.

---

## 7. `workflows-discover.png`

**Used in:** README workflow section.

**Must be visible:** the Discover-workflows browse grid (CivitAI Workflow models)
with an import action visible on a card.

**Why it earns its place:** it closes the loop on "where do I get workflows" —
showing you don't have to go find a zip yourself. Lower priority because it looks
like a generic search grid; it's supporting evidence, not a hook.

---

## 8. `dashboard.png`

**Used in:** README library section.

**Must be visible:** the dashboard with a few real subscriptions, the activity
feed, and ideally a live download in the queue.

**Why it earns its place:** it orients a reader who wants to know what the app
*is* between the workflow screens, and it's the one shot that shows the
subscription/poller half actually running. Lowest priority — if the queue is
empty and the feed is bare, skip it rather than shipping a hollow screenshot.

---

## Optional

`social-card.png` — 1280×640, for the GitHub repo's social preview (Settings →
General → Social preview). Not referenced by the landing page or README; purely
for link unfurls. A crop of slot 1 with the project name overlaid works well.
