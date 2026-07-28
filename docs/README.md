# `docs/` — project site and reference docs

This directory holds two things:

1. **`index.html`** — a self-contained landing page intended to be served by
   GitHub Pages.
2. **Reference documentation** (`configuration.md`, `cli.md`, `testing.md`) —
   the long-form material that used to live in the root `README.md`. These are
   linked from the README and read fine on github.com without Pages enabled.

## Enabling GitHub Pages

Pages is **not** enabled automatically — a repository admin has to turn it on:

1. Go to **Settings → Pages**.
2. Under **Build and deployment → Source**, choose **Deploy from a branch**.
3. Set the branch to **`main`** and the folder to **`/docs`**.
4. Click **Save**.

The site is then published at `https://<owner>.github.io/civitai-manager/`,
serving `docs/index.html` as the root page. Publishing takes a minute or two on
the first deploy.

Once it is live, set the repository's **Website** field to that URL (Settings →
General, or the ⚙ next to "About" on the repo home page) so the landing page is
linked from the sidebar.

## Notes for editing the landing page

- `index.html` is **fully self-contained**: all CSS is inline, and there are no
  external scripts, stylesheets, fonts, or CDN references. This mirrors the
  application's own offline invariant — please keep it that way.
- All asset paths are **relative** (`assets/…`), so the page works whether it is
  served from the domain root or from a project subpath.
- The page is theme-aware via `prefers-color-scheme`; if you change colours,
  update both the light and dark blocks.
- Screenshots live in `assets/`. See [`assets/SHOTLIST.md`](assets/SHOTLIST.md)
  for the exact filenames the page and the README expect, and what each image is
  supposed to show.
