# `docs/` — project site and reference docs

This directory holds three things:

1. **`index.html`** — a self-contained landing page intended to be served by
   GitHub Pages.
2. **Reference documentation** (`install.md`, `configuration.md`, `cli.md`,
   `testing.md`) — the long-form material that used to live in the root
   `README.md`. These are linked from the README and read fine on github.com
   without Pages enabled.
3. **`install.sh`** — the `curl | sh` installer, served from this directory at
   `https://zacxdev.github.io/civitai-manager/install.sh`.

## Editing `install.sh`

It is a POSIX-sh script that strangers pipe into a shell, so it is held to a
higher bar than the rest of the docs:

- **POSIX `sh`, not bash.** CI runs `shellcheck --shell=sh docs/install.sh` on
  every change and the build fails on any finding.
- **Every URL is a hard-coded `https://github.com` URL**, and `fetch()` refuses
  anything that is not https. Do not add a URL override.
- **`--version` is validated against a semver pattern** before it is
  interpolated into a download URL.
- **The tarball is verified against the release's `checksums.txt`** and a
  mismatch aborts before anything is unpacked. Do not weaken or make this
  optional.
- **No surprise `sudo`.** With no `--prefix` it uses `/usr/local` only when
  already writable and otherwise `$HOME/.local`. When an explicitly requested
  prefix needs elevation, it prints the exact command before running it.
- **The only path it deletes is its own `mktemp -d`.**
- **busybox is a supported environment.** Alpine and most musl images ship
  busybox `wget` and no curl, and the binaries are `CGO_ENABLED=0` so they run
  there. busybox `wget` is not GNU `wget`: it has no `--https-only`, and it
  answers an unknown long option by printing its usage and failing — which is
  how a perfectly good URL once produced `download failed: <url>`. Probe for a
  flag (`wget --help 2>&1 | grep -q -- '--flag'`) before relying on it.

Test a change end to end against a real release into a scratch prefix, under
`sh` and `dash`, **and with only busybox on `PATH`** (which also removes curl,
so it exercises the wget branch):

```sh
sh docs/install.sh --version 0.1.75 --prefix /tmp/cm-test
/tmp/cm-test/bin/civitai-manager --version

# busybox-only: no curl, busybox wget
mkdir -p /tmp/bb && for a in $(busybox --list); do ln -sf "$(command -v busybox)" "/tmp/bb/$a"; done
env -i HOME=/tmp/cm-home PATH=/tmp/bb /tmp/bb/sh docs/install.sh --prefix /tmp/cm-test-bb
```

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
