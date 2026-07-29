# CivitAI article — launch draft

**Status: DRAFT. Do not publish. Publishing is the owner's call, from his account.**

Everything below the `---` line is the article body, written to be pasted into
CivitAI's article editor. Image placement notes are in `> **[IMAGE …]**` blocks —
delete those blocks after you upload the actual PNG in that slot.

## Title options

1. **Run a downloaded workflow without hunting for its models** ← recommended
2. I got tired of `value not in list`, so I wrote a thing that fetches the models for you
3. civitai-manager: import a workflow, click Run, let the missing models fetch themselves
4. The workflow ran. I didn't download a single file by hand.
5. A local tool that closes the gap between "downloaded a workflow" and "the workflow runs"

Recommendation: **#1**. It states the outcome, contains the words a search would
use ("workflow", "models"), and promises nothing that isn't in the article.
Avoid #4 as a title — it reads as a claim rather than a description, and this
audience is allergic to that.

**Suggested tags:** `comfyui`, `workflow`, `tools`, `open source`, `automation`

## Image plan (5 screenshots, all in `docs/assets/`)

| Slot | File | Where | Why there |
| --- | --- | --- | --- |
| 1 | `hero-run-missing-models.png` | Immediately after the opening problem section | It *is* the pitch. A reader who has seen `value not in list` recognises the frame instantly and reads on. |
| 2 | `run-downloading.png` | Inside "What actually happens", under the download-and-run paragraph | It is the proof of the one claim that separates this from a downloader: the run **waits** rather than failing. |
| 3 | `run-params.png` | Under "Change the prompt without opening ComfyUI" | Answers the obvious next question before it's asked, and the "this run only" note is a trust signal. |
| 4 | `outputs-gallery.png` | Under "Your outputs stop evaporating" | The most attractive screen in the app; it carries the "this is a finished thing" impression. |
| 5 | `library-candidates.png` | Under "The other half: the library" | Three reason labels (duplicate / superseded / broken) communicate that whole half faster than a paragraph. |

Do not add a sixth image. CivitAI articles with more screenshots than argument
read as a brochure.

---

# Run a downloaded workflow without hunting for its models

You find a workflow on CivitAI. You download it, drop it into ComfyUI, hit Run,
and get:

```
Prompt outputs failed validation:
ckpt_name: 'value not in list'
```

So you open the node, work out which checkpoint that string was supposed to
mean, search for it, download it, figure out whether it belongs in
`checkpoints/` or `unet/`, restart, hit Run — and it fails on the *next* missing
file. Then a LoRA. Then a VAE that was never on CivitAI in the first place.

I wrote **civitai-manager** because I kept losing evenings to that loop. It
imports the workflow, diffs the graph against your actual ComfyUI install,
finds the model files you're missing, resolves them on CivitAI, downloads them
into the correct `models/` subfolder, and **starts the run when the download
finishes**. You don't re-click.

It's a single Go binary, Apache-2.0, runs entirely on your machine, currently
**v0.1.77**.

> **[IMAGE 1: `hero-run-missing-models.png`]** — caption: *A run that would have
> failed. The missing files are matched against CivitAI instead, with alternates
> when more than one model fits.*

## The honest caveat, before anything else

**It does not install custom nodes.** It resolves and downloads **model files**
only. If a workflow needs a node pack you don't have, the preflight names the
missing node classes — and then stops. Installing them is still your job, with
ComfyUI-Manager or by hand.

I'm putting that in the third paragraph rather than the last one because it's
the first thing you'd have found out yourself, and finding it out yourself would
have been annoying. Missing models and missing nodes are two different problems;
this only solves one of them.

## What actually happens when you click Run

Before anything is submitted, the workflow is checked against your live
ComfyUI's `/object_info`, and you get three separate lists:

**Missing custom nodes** — named, so you know what to go install. (No install
action. See above.)

**Incompatible options** — a saved sampler, scheduler, or other enum value your
build doesn't have, shown next to a dropdown of the values it *does* have. Pick
one and run. Your choice is re-validated server-side against the real list, so
an off-list value is refused rather than injected.

**Missing models** — this is the part the tool is for. For each file the graph
wants and you don't have, it infers the model type, searches CivitAI, and offers
you:

- the **best match**, plus **other possible matches** when more than one model
  fits — so you disambiguate instead of it guessing on your behalf;
- an **already-installed substitute**, if you have something that would do,
  without editing the workflow;
- **download and run** — it fetches the file into the right `models/` subfolder,
  and the run begins on its own when the download completes.

> **[IMAGE 2: `run-downloading.png`]** — caption: *The run is queued behind the
> download and starts by itself. This is the bit that saves the evening.*

### Substitutions are offered, never silent

This is the design decision I'd most want scrutinised. CivitAI models frequently
don't publish a file under the exact name a workflow asks for — same model,
different filename, different quantisation. When that happens, the tool does
**not** quietly grab the closest thing. The first click gets you a confirmation
telling you what it *would* install instead, and it waits for a second click.
Once running, the progress line reads `<the real file> as <the expected file>`,
so what's on disk and what the graph asked for are both visible.

I'd rather you get one extra click than discover three weeks later that your
"reproduction" of someone's workflow used a different file.

### When CivitAI doesn't have it

VAEs, upscalers and detection models often never had a CivitAI page. There's a
**HuggingFace fallback** for those, deliberately narrow: it only auto-downloads
for a curated filename map or a recognised org, when the file isn't gated and
its SHA256 is known. Everything else degrades to a link with the reason stated.
It will not go wandering around HuggingFace grabbing files that merely look
right.

## Change the prompt without opening ComfyUI

Prompt, seed, steps, cfg, sampler, scheduler, denoise, width, height and batch
size are editable in the run panel. The values are pulled from the nodes that
actually hold them — including through converted-widget links, so template packs
that route everything through primitive nodes still surface their prompt instead
of showing you an empty panel.

**The stored workflow is never modified.** Every edit, substitution and option
fix applies to an in-memory copy for that one run. The graph you imported stays
byte-identical to how you imported it.

> **[IMAGE 3: `run-params.png`]** — caption: *Editable per-run. The saved
> workflow is untouched. Only appears for UI-format workflows.*

### Multi-mode template packs

A lot of the good template packs ship several pipelines in one file — T2V, I2V,
first-to-last-frame — with all but one bypassed, and a note in the description
telling you which groups to toggle. Those get a mode picker that enables the
pipeline you choose.

Detection keys on the author's *own declared exclusivity*: an rgthree Fast
Groups Bypasser/Muter with a `toggleRestriction` of "max one" / "always one" is
the author saying, in the file, that these groups are mutually exclusive. It
doesn't guess from group names or colours. If the pack doesn't declare it, you
get no picker rather than a wrong one.

## Getting workflows in

Paste the JSON, drop in a **PNG that ComfyUI generated** (the graph comes out of
its metadata), scan your ComfyUI install for graphs already on disk, or browse
**Discover** and import straight from CivitAI's Workflow models — those ship as
zips, so it unpacks them for you and skips graphs you already have (deduped by a
canonical hash of the graph itself, not the filename).

Discover is browsable **by ecosystem and by use case** — Flux, SDXL, Wan, Qwen,
SD 1.5 …, and inpaint / upscale / detailer / ControlNet / i2v — over a curated
vocabulary rather than raw CivitAI tags. Raw workflow tags are dominated by
noise; `tool`, `comfyui` and `workflow` are on most of them and tell you nothing.

The **UI → API conversion** underneath handles real-world graphs, not clean ones:
it expands **subgraphs**, resolves **Get/Set teleport** nodes back to their real
source, splices through **bypassed and muted** nodes, drops UI-only **rgthree**
helpers, and handles converted-widget inputs without shifting every widget value
after them. If conversion produces warnings it can't resolve — an unknown node
class, an ambiguous teleport — the run is **aborted** rather than submitted as a
half-broken graph.

**Open in ComfyUI** saves the graph into ComfyUI's own Workflows menu. An
optional helper extension, installable from the UI, makes that button open the
workflow directly rather than just telling you where it landed — it needs one
ComfyUI restart to activate.

## Your outputs stop evaporating

Every successful local run has its images copied into an app-owned gallery,
recorded alongside the parameters that produced them. Re-run reconstructs those
parameters. It survives ComfyUI clearing its own output folder.

It is disk-capped — default 20 GiB, oldest-first eviction. That means **it
deletes things**; if you want it unlimited, set the cap to 0 explicitly.

> **[IMAGE 4: `outputs-gallery.png`]** — caption: *Every successful local run,
> with the parameters that made it. Disk-capped, oldest-first.*

## The other half: the library

If you're on CivitAI, your `models/` folder is probably a landfill. That's the
other half of the tool, and it works with ComfyUI closed.

**Subscribe to models and creators.** A poller diffs each subscription's version
list against a per-subscription ledger and queues genuinely new versions. Every
download is verified against the API's SHA256 where one is published, finalized
atomically, and written with `.civitai.info` / `.preview.png` sidecars.

Subscribing is conservative on purpose: it **seeds** the ledger with the current
back-catalog *without* downloading it, so a new subscription never retro-pulls
300 GB overnight. `--backfill-latest` opts into grabbing the current newest
version.

**Scan what you already have.** Point it at your ComfyUI or A1111 model
directories. It hashes everything — with an mtime/size cache so re-scans skip
unchanged multi-GB files — matches the whole set against CivitAI in **one** batch
by-hash lookup, and flags **duplicates**, **superseded** versions, and **broken**
files.

**Nothing is ever hard-deleted.** Acting on those flags *moves* files into a
trash directory with an undo manifest; `library restore <batchID>` puts them
back. The mover refuses to leave zero copies of a duplicate set, refuses
unmatched files, refuses the newest version, and refuses anything that changed
since the scan.

**`verify --repair`** reconciles what the tool downloaded against what's on disk
and re-fetches anything missing or corrupt. It's the one path that recovers a
model you deleted, since a normal re-subscribe considers that version already
seen.

> **[IMAGE 5: `library-candidates.png`]** — caption: *Duplicate, superseded and
> broken candidates after a scan. Acting on them quarantines; it never deletes.*

## What leaves your machine

Worth being specific, since this reads local files:

- **Library scan matching sends your files' SHA-256 hashes to civitai.com** —
  that's how the by-hash match works. It's **on by default**; `scan --no-remote`
  turns it off, and you still get local duplicate detection.
- **The HuggingFace fallback sends a missing model's filename to
  huggingface.co.** `hf_fallback: false` turns it off.
- Ordinary CivitAI API calls and downloads for the things you asked for.
- If you explicitly turn on **CivitAI cloud runs** (off by default), that sends
  the graph to CivitAI's orchestration endpoint. That one's obvious from what it
  does, but it belongs on the list.

That's the list. **No telemetry, no analytics, no account, no phone-home.** The
web UI is fully offline — CSS and htmx are compiled into the binary, so there is
no CDN, no external font, and no third party that learns you opened it. It binds
loopback by default.

## Install

```sh
# install script — detects OS/arch, verifies against the release checksums.txt
curl -fsSL https://zacxdev.github.io/civitai-manager/install.sh | sh
```

It installs to `/usr/local` only if `/usr/local/bin` is already yours, otherwise
`~/.local` — it will not surprise you with a `sudo` prompt. Read it first if
you'd rather; that's the right instinct for anything piped into a shell.

Other routes:

```sh
# Nix — run it once without installing anything
nix run github:ZacxDev/civitai-manager -- serve --comfy-model-path ~/ComfyUI/models

# Homebrew (a cask; works on macOS and Linux)
# Since Homebrew 6.0 a third-party tap must be trusted — the fully qualified
# name below trusts this one cask only, which is the narrower grant.
brew install --cask ZacxDev/tap/civitai-manager

# Debian/Ubuntu and Fedora/RHEL packages are attached to every release
sudo dpkg -i civitai-manager_0.1.77_linux_amd64.deb
sudo rpm  -i civitai-manager_0.1.77_linux_amd64.rpm

# Go 1.25+
go install github.com/ZacxDev/civitai-manager@latest
```

Plain tarballs are on the releases page for **linux, macOS and Windows** on
**amd64 and arm64**. Every artifact carries a Sigstore-signed GitHub build
attestation, so you can check it actually came out of this repository's
pipeline:

```sh
gh attestation verify --owner ZacxDev civitai-manager_0.1.77_linux_amd64.tar.gz
```

Then:

```sh
civitai-manager serve --comfy-model-path ~/ComfyUI/models
# → http://localhost:8787
```

`--comfy-model-path` is what lets it put a downloaded model in the right place.
Without it you still get the diagnosis, just not the fix.

You'll want a **CivitAI API token** before downloading much — the browse
endpoints work anonymously but most file downloads need auth. If you already use
the official `civitai` CLI, your existing token is picked up automatically.

## Limitations — read these before deciding it's broken

- **Custom nodes are not installed for you.** Said three times now on purpose.
- **A graph the converter can't fully understand blocks the run** rather than
  submitting something half-broken.
- **Parameters inside subgraphs aren't editable.** The panel reads top-level
  nodes and stops at a subgraph boundary. It also only appears for UI-format
  workflows — an API-format graph carries no widget values to edit.
- **CivitAI cloud runs don't resolve custom node packs.** Model resources are
  mapped to AIR URNs automatically; node packs are not. (Cloud runs are off by
  default anyway — they send the graph to CivitAI and spend Buzz. You get a cost
  estimate before committing.)
- **Cloud run results are not captured into the output gallery.** Local runs only.
- **The ComfyUI helper extension needs one restart to activate**, and another to
  fully remove.
- **Preflight checks against civitai-manager's own file index**, not ComfyUI's
  directory. A file in your library but not where ComfyUI can see it will pass
  preflight and then fail inside ComfyUI.
- **NSFW previews are blurred, not withheld.** Blur is a browser-side CSS filter
  — the bytes still go over the wire. It's a shoulder-surfing control, not a
  privacy boundary.
- **The web UI has no login.** It binds loopback and its only protection is a
  per-process CSRF token. Binding a LAN address exposes an unauthenticated UI —
  and your output gallery.
- **The macOS Homebrew cask is untested on real hardware.** I don't own a Mac.
  It's built and published by CI, and the Gatekeeper handling is reasoned from
  Homebrew's own source, but nobody has run it on an actual Mac. If you try it,
  I'd genuinely like to hear what happened.
- **This is v0.1.x.** The database schema and internal APIs still move between
  releases. Migrations run automatically and in order, but treat it as unstable
  software.
- Interrupted downloads are re-fetched whole, not resumed. Search results are the
  first page only.

## Links

- **Repository:** https://github.com/ZacxDev/civitai-manager
- **Landing page:** https://zacxdev.github.io/civitai-manager/
- **Releases:** https://github.com/ZacxDev/civitai-manager/releases/latest

Apache-2.0. Issues and PRs welcome — particularly workflow graphs the converter
chokes on. A graph that fails to convert is a more useful bug report than a
description of it, and it's the class of bug I can't find on my own, because I
only have my own workflows to test against.
