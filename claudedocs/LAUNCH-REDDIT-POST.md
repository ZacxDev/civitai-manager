# Reddit post — launch draft

**Status: DRAFT. Do not post. Posting is the owner's call, from his account.**

## Recommended posting order

**1. r/comfyui first. 2. r/StableDiffusion second, days later, with the body
reworked per the traps below. Do not post to r/golang.**

Why that order: r/comfyui is the exact audience (the hook is a ComfyUI failure
mode), it has effectively no self-promo rules to trip on, and it's small enough
(~203k) that a mediocre reception costs little. r/StableDiffusion is ~983k and
has a **once per tool/update** cap — you get one shot, so spend it after
r/comfyui has told you which parts of the pitch land and which comment you keep
having to answer. Fold that answer into the r/StableDiffusion body.

Leave several days between them. Both allow it; back-to-back cross-posting reads
as a campaign.

---

## Sub rules — what was found, and the traps

**Retrieval note:** these were read from the subreddits' own first-party
endpoints (`about/rules.json`, `about.json`, `api/v1/<sub>/post_requirements.json`,
`link_flair_v2.json`), plus current stickies — not from secondary write-ups.
**AutoModerator config could not be verified for any sub** (account-age/karma
gating, rate limits, keyword auto-removal are mod-only and not publicly exposed).
No evidence either way; don't assume you're safe from an automod rule nobody can
see.

### r/comfyui (~203k) — 🟢 SAFE TO POST

Sources: <https://old.reddit.com/r/comfyui/about/rules> · sidebar at
<https://old.reddit.com/r/comfyui/> · mod sticky 2026-07-07,
<https://reddit.com/r/comfyui/comments/1uq0mjy/closed_model_flair_required/>

- **There is no structured rule list at all.** `about/rules.json` returns
  `{"rules": []}`; only Reddit's site-wide rules are attached. No wiki, no
  submit-page text.
- The de-facto rules are the sidebar: *"Please keep posted images SFW. Paywalled
  workflows not allowed. Please stay on topic. … BE NICE. … if this is new and
  exciting to you, feel free to post, but don't spam all your work."*
- **Self-promotion: no rule.** No ratio, no 9:1, no author-disclosure
  requirement, no posting-day restriction. A free OSS tool has nothing to trip on.
- **"I made a thing" posts are mainstream here.** There's a dedicated `Resource`
  flair, and a currently-hot post is *"I made a free, open-source timeline editor
  that runs inside your ComfyUI workflow"*, flaired `Resource`.
- **Flair is mandatory** (`is_flair_required: true`). Available: `Workflow
  Included`, `News`, `Tutorial`, **`Resource`**, `Help Needed`, `Show and Tell`,
  `No workflow`, `Commercial Interest`, `Security Alert`, `Closed Model`.
  → **Use `Resource`.** Fallback `Show and Tell`. **Do not use `Commercial
  Interest`** — that flair is for "looking for a paid partner" posts and would
  actively mislabel this.
- **Low-effort:** the mod sticky says *"Low value posts just showing off a video
  without any workflow or other helpful information will be removed."* Not aimed
  at tool posts, but treat the body as needing to stand alone.
- **No rule on AI-generated post text. No domain blacklist/whitelist** — GitHub
  links are fine, self-posts and link posts both enabled.
- **Frequency:** nothing codified beyond "don't spam all your work".

**Conditions:** set `Resource` flair; make it a self-post with real substance,
not a bare link; the tool must not be paywalled (it isn't).

### r/StableDiffusion (~983k) — 🟡 POST WITH CONDITIONS

Sources: <https://old.reddit.com/r/StableDiffusion/about/rules> · sidebar at
<https://old.reddit.com/r/StableDiffusion/> ·
<https://www.reddit.com/api/v1/StableDiffusion/post_requirements.json>

This sub runs **two overlapping rule sets** — an 8-rule structured widget and a
10-item sidebar list. Both are live, and the self-promo carve-out you need is
only spelled out in the **sidebar** version:

> **Limited self-promotion** — Open-source, free, or local tools can be promoted
> at any time (once per tool/guide/update). Paid services or paywalled content
> can only be shared during our monthly event.

That is an explicit OSS carve-out: **free/open-source tools may be posted any
time**, capped at **one post per release**.

Structured rules that bear on this post:

- **Rule 1 — Posts Must Be Open-Source or Local AI Related.** *"Posts must center
  on open-source/local AI tools… When relevant you should clearly list tools in
  the title/body and explain your workflow."*
- **Rule 6 — No Reposts, Spam, Low-Quality, or Excessive Self-Promo.** *"Don't
  post just to promote yourself or drive traffic to paid tools, services, or
  downloads. No paywalls, affiliate links, or hidden URLs. Low-effort posts like
  images/videos without context or workflows are not allowed. **Tutorials and
  tools must be free and directly accessible.** Always search before posting."*
- **Rule 7 — Use the Correct Flair.** Mandatory (`is_flair_required: true`).
  Available: `News`, `Discussion`, `Question - Help`, `Tutorial - Guide`,
  **`Resource - Update`**, `Comparison`, `IRL`, `Meme`, `Animation - Video`,
  `Workflow Included`, `No Workflow`.
  → **Use `Resource - Update`.** That is what every comparable post uses; one of
  the two current stickies is itself an OSS ComfyUI tool release under that flair.
- **Rule 3 — no lewd or sexually suggestive imagery**, tags are not an exception.
- **Rule 8 — Mod discretion**, with *"If in doubt, message the mods before
  posting."*
- **No rule on AI-generated post text.** No domain blacklist/whitelist — GitHub
  links fine. Link shorteners are effectively banned by rule 6's "no hidden URLs".

#### 🚨 Hard trap: the literal string `nsfw` is server-side blacklisted

`post_requirements.json` returns:

- `title_blacklisted_strings`: `["titties", "mauh ai", "titty", "nsfw"]`
- `body_blacklisted_strings`: `["mauh ai", "nsfw"]`

**The string `nsfw` in the title *or* the body causes an outright submission
reject** — not a mod judgment call, a server-side refusal. civitai-manager has a
content-blur mode, and the obvious way to describe it is the forbidden word. The
draft body below deliberately contains **zero** occurrences of it; if you edit
the post, keep it that way. Say "content filtering" or "preview blurring"
instead.

Second trap: **rule 3** means no screenshot containing adult model thumbnails,
even blurred. If you attach the hero image, check it.

Third: **rule 1 framing.** The sub is wary of anything that reads as driving
traffic to a commercial platform. Lead with the *local ComfyUI* benefit; the
CivitAI integration is the mechanism, not the headline. The draft below already
does this — the first CivitAI mention is functional. Do not retitle it around
CivitAI for that sub.

**Conditions:** flair `Resource - Update` · never write the blacklisted string ·
once per release only · self-contained body, no bare link · direct GitHub URL, no
shortener · tool genuinely free with public source · SFW screenshots · frame
around local/open workflow.

### Subs to skip

- **r/golang — recommend against.** OSS Go project announcements are on-topic in
  principle, but the "Must be Go Related" rule requires dev tools to be
  *"specifically targeted at Go developers"* — this is written in Go and aimed at
  AI artists, which is exactly the mismatch that gets removed. It also has the
  strictest bar of any sub here: a
  [project_requirements wiki](https://old.reddit.com/r/golang/wiki/project_requirements)
  demanding stated purpose, goals-vs-results, and concision; projects with *"a few
  months of effort, one contributor, and no real-world usage"* are **required** to
  go in the weekly pinned "Small Projects" thread rather than a top-level post;
  and a hard **AI policy** — *"No GPT or other AI-generated content is allowed as
  posts… posts will be removed based on their appearance"*, with emoji
  automatically blocked. Low upside, real removal risk.
  Source: <https://old.reddit.com/r/golang/about/rules>
- **r/selfhosted — only via the megathread, for now.** Self-promo runs on
  Reddit's 9:1 guideline, promoted apps *"must be production ready and have
  docs"*, and critically: projects **younger than 3 months** (from first public
  commit) may **only** go in the current "New Project Megathread". Check the
  repo's first-commit date against that before considering it — and "production
  ready" is a claim this project explicitly declines to make at v0.1.x.
  Source: <https://old.reddit.com/r/selfhosted/about/rules>
- **r/LocalLLaMA — off-topic.** Rule 2 scopes it to Llama/LLMs; this is
  image-model tooling. It also carries a 1/10th self-promo rule, a mandatory
  affiliation disclosure, and a ban on primarily-LLM-generated copy.
  Source: <https://old.reddit.com/r/LocalLLaMA/about/rules>

---

## Title options

Pick one. All are descriptive rather than clickbait, and all disclose that it's
the author posting.

1. **I got tired of hunting down a workflow's missing models by hand, so I wrote a tool that fetches them and then starts the run**
2. **Open-source tool: import a ComfyUI workflow, click Run, and the missing model files download themselves before the run starts (it does NOT install custom nodes)**
3. **Made a local tool that diffs a workflow against your ComfyUI install, downloads the models it's missing, and queues the run behind the download**
4. **civitai-manager v0.1.77 — automatic missing-*model* resolution for downloaded workflows (models only; custom nodes are still on you)**
5. **`value not in list: ckpt_name` — I automated the part after that error**

Recommendation: **#1** for r/comfyui — it's a problem statement, it's first-person,
and it doesn't lead with the project name. **#2** if the flair or the sub's culture
wants the tool named up front; putting the custom-nodes caveat in the title itself
pre-empts the top comment.

Avoid #5 as a solo title (too cute, and the error string may not render well in
some clients) — but it's a good *first line* of the body.

**Flair is mandatory on both subs and enforced at submit time:** `Resource` on
r/comfyui, `Resource - Update` on r/StableDiffusion. And check any title you
write for the blacklisted string described above — it's rejected in titles too.

---

## Post body (Reddit markdown)

> **Paste only what is between the two `BEGIN`/`END` markers.** The rest of this
> file — including the rules section above — contains the blacklisted string and
> would get an r/StableDiffusion submission auto-rejected if pasted in.

<!-- ===== BEGIN POST BODY ===== -->

Every downloaded workflow is a scavenger hunt. You drop it in, hit Run, and get:

    Prompt outputs failed validation:
    ckpt_name: 'value not in list'

So you open the node, guess which checkpoint that string meant, go find it,
figure out whether it belongs in `checkpoints/` or `unet/`, restart, hit Run —
and it dies on the next missing file. Then a LoRA. Then a VAE that was never on
CivitAI to begin with.

I wrote a thing that does that part for me. It imports the workflow, diffs the
graph against what your ComfyUI actually has, finds the missing model files,
resolves them on CivitAI, downloads them into the right `models/` subfolder, and
**starts the run when the download finishes**. You don't re-click.

**Up front, because it's the first thing you'd ask: it does NOT install custom
nodes.** Model files only. If a workflow needs a node pack you don't have, it
names the missing node classes and stops. ComfyUI-Manager already does that job
and does it well; this is the other half of the problem.

Other things it does:

* **Preflight** against your live `/object_info` — missing nodes, missing models,
  and saved sampler/scheduler values your build doesn't have (those get a
  dropdown of the ones it does have).
* **Multi-match resolution.** When more than one CivitAI model fits, you get the
  alternates and pick, instead of it guessing. If the model has no file matching
  the exact name your workflow wants, it tells you what it *would* install
  instead and waits for a second click — substitutions are never silent.
* **HuggingFace fallback** for the VAEs and upscalers that never had a CivitAI
  page. Narrow on purpose: curated filename map or recognised org, ungated, known
  SHA256. Everything else becomes a link with the reason stated.
* **Editable run params** — prompt, seed, steps, cfg, sampler, scheduler,
  denoise, dimensions — resolved through converted-widget links, so packs that
  route everything through primitives still expose their prompt. **The stored
  workflow is never modified**; edits apply to an in-memory copy for that run.
* **UI→API conversion** that handles subgraphs, Get/Set teleports, bypassed and
  muted nodes, and rgthree helpers.
* **Multi-mode template packs** (T2V / I2V / first-to-last-frame in one file, all
  but one bypassed) get a mode picker. Detection keys on the author's own
  declared exclusivity — an rgthree Fast Groups Bypasser with `toggleRestriction`
  — not on guessing from group names.
* **Output gallery** — successful local runs are copied to disk with the params
  that made them, and re-run reconstructs those params. Disk-capped, default
  20 GiB, oldest-first eviction (i.e. it deletes things).
* **Library half**: subscribe to models/creators with a poller, and scan your
  `models/` dir to flag duplicates / superseded / broken. Quarantine is a
  reversible move with an undo manifest — nothing is hard-deleted.

**What it doesn't do:** install custom nodes, edit parameters inside subgraphs,
resolve node packs for CivitAI cloud runs, or resume an interrupted download
(it re-fetches whole).

**What leaves your machine:** library scan matching sends your files' SHA-256
hashes to civitai.com (that's how the by-hash match works — on by default,
`scan --no-remote` turns it off), and the HuggingFace fallback sends a missing
model's filename to huggingface.co (`hf_fallback: false` turns it off). Plus the
ordinary API calls for things you asked for. No telemetry, no account, no
analytics. It binds loopback, and the web UI is fully offline — the CSS and htmx
are compiled into the binary, so there's no CDN and no external font.

Single Go binary, Apache-2.0, linux/macOS/Windows on amd64+arm64.

    curl -fsSL https://zacxdev.github.io/civitai-manager/install.sh | sh
    civitai-manager serve --comfy-model-path ~/ComfyUI/models
    # → http://localhost:8787

(Also Nix, Homebrew, .deb/.rpm, `go install`. The installer verifies against the
release `checksums.txt` and won't surprise you with sudo.)

It's **v0.1.x** — the schema and flags still move between releases, and I'd call
it "works for me and the graphs I have" rather than stable. The macOS Homebrew
cask is built and published by CI but **untested on real hardware**, because I
don't own a Mac.

Repo: https://github.com/ZacxDev/civitai-manager
Landing page: https://zacxdev.github.io/civitai-manager/

The bug I most want reported is **a workflow graph the converter chokes on** —
that's the class of failure I can't find myself, since I only have my own
workflows to test against. Attach the JSON and I'll take it.

<!-- ===== END POST BODY ===== -->

---

## Per-sub adjustments to the body above

The body as written is the **r/comfyui** version. For **r/StableDiffusion**,
change these four things and nothing else:

1. **Flair `Resource - Update`** instead of `Resource`.
2. **Add one line near the top** establishing local/open scope, for rule 1:
   *"Everything runs locally against your own ComfyUI — the CivitAI half is just
   where the model files come from."* Rule 1 wants the post to visibly centre on
   local/open tooling.
3. **Re-check for the blacklisted string** before submitting. The body above has
   none; any edit you make could reintroduce it and the submission will be
   refused outright.
4. **Fold in the top comment from the r/comfyui run.** Whatever question you had
   to answer three times there, answer it pre-emptively here. That's the whole
   reason for posting second.

Do **not** change the title to lead with CivitAI for that sub.

---

## Anticipated comments — draft replies

Adapt, don't paste verbatim. Tone: answer the question, concede the real point,
don't sell.

### 1. "Why not just use ComfyUI-Manager?"

> Different problem, and I use ComfyUI-Manager too. It installs **custom nodes**;
> this resolves **model files**. Mine explicitly doesn't do node installs — the
> preflight names what's missing and stops there.
>
> The overlap is model downloads, and there the difference is that this searches
> CivitAI for the filename the graph asked for, shows you the alternates when
> more than one model matches, and then **queues the run behind the download** so
> it starts on its own. Nothing stops you running both; they sit on different
> halves of the failure.

### 2. "Does this phone home? What data leaves my machine?"

> Three things, all listed in the README:
>
> 1. **Library scan matching sends the SHA-256 hashes of your model files to
>    civitai.com.** That's the mechanism — CivitAI has a batch by-hash lookup and
>    that's how it identifies what you've got. It's **on by default**;
>    `scan --no-remote` disables it and you still get local duplicate detection.
>    A hash isn't the file, but it does tell CivitAI you hold that file, so it's
>    a real disclosure and I'd rather state it than bury it.
> 2. **The HuggingFace fallback sends a missing model's filename to
>    huggingface.co** when CivitAI doesn't have it. `hf_fallback: false` turns it
>    off.
> 3. Ordinary CivitAI API calls and downloads for the things you asked for. If
>    you explicitly enable CivitAI cloud runs (off by default), that obviously
>    sends the graph to CivitAI.
>
> That's the whole list — the only hosts in the codebase are civitai.com,
> huggingface.co, and your local ComfyUI. No telemetry, no analytics, no crash
> reporting, no account. It binds loopback by default, and the web UI has no CDN
> or external font, so nothing third-party learns you opened it. The offline UI
> was a deliberate constraint, not an accident.

### 3. "Why Go? Why not just write it as a custom node?"

> Mostly because I wanted it to work with ComfyUI **closed**. Roughly half the
> tool — subscriptions, the download queue, the library scanner, workflow import
> and browsing — has nothing to do with a running ComfyUI, and a custom node only
> exists while ComfyUI is up. I also wanted a `cron`-able CLI for the
> subscription polling.
>
> Go specifically: it's a single static binary with no cgo (the SQLite driver is
> pure Go), so shipping it is `download, run` on six platforms with no Python
> environment to collide with yours. Putting a package into the same env as
> ComfyUI is exactly the class of problem I was trying to stop having.
>
> There *is* a small optional ComfyUI extension, but it does one narrow thing —
> makes "Open in ComfyUI" actually open the workflow — and the tool works without
> it.

### 4. "Is this AI-written slop?"

> Fair question to ask about anything that appears fully formed with a landing
> page. I use AI assistance while writing it, and I'm not going to pretend
> otherwise. What I'd point at instead of a promise:
>
> The limitations section is longer than the feature list and it's specific —
> "parameters inside subgraphs aren't editable", "preflight checks my own file
> index rather than ComfyUI's directory, so a file in the wrong place passes
> preflight and then fails inside ComfyUI", "the macOS cask is untested because I
> don't own a Mac". Slop doesn't volunteer that.
>
> It's also v0.1.x and I've said so everywhere. Try it or don't — it's Apache-2.0
> and it's a single binary you can delete. If it doesn't work on your graphs I'd
> rather have the graph than the benefit of the doubt.

### 5. "So it still can't run half the workflows on CivitAI, because they all need custom nodes."

> Correct, and that's a real limit rather than a nitpick. If the workflow needs a
> node pack you don't have, this gets you as far as a named list of what to
> install and no further.
>
> What it does buy you is that the *models* stop being a separate scavenger hunt
> on top of that — and in my experience the model hunt is the longer of the two,
> because node packs are usually named in the post whereas `ckpt_name:
> 'value not in list'` doesn't tell you which checkpoint it wanted.
>
> Auto-installing nodes is a much harder and much more dangerous problem
> (arbitrary code into your Python env, dependency conflicts with what you
> already have), and ComfyUI-Manager already owns it. I don't have a good reason
> to build a second one.

### 6 (bonus). "Why does it need my CivitAI token?"

> Only for downloads. The public browse/search endpoints work anonymously, but
> most model file downloads require auth on CivitAI's side. If you already use
> the official `civitai` CLI, your existing token is picked up automatically —
> otherwise it's an env var or a line in the config file. It's never sent
> anywhere except civitai.com.

---

## Deliberately left out of the post

- **The content-blur / preview-filtering feature.** Not because it's
  embarrassing, but because the only natural word for it is server-side
  blacklisted on r/StableDiffusion and would get the submission rejected. It's
  documented in the repo; it doesn't need to be in a launch post.
- Any performance claim, benchmark, timing, percentage, or user count.
- "Production-ready", "fast", "seamless", "just works".
- The Discover/taxonomy browse feature and the subscription poller's internals —
  interesting, but they dilute the single hook and lengthen a post that Reddit
  will punish for length. They're in the CivitAI article instead.
- Build provenance / `gh attestation verify` — real and worth knowing, but it
  reads as trust-signalling in a Reddit post. It's in the repo and the article.
- A screenshot gallery. One image at most if the sub allows it; the hero
  (`docs/assets/hero-run-missing-models.png`) is the only one that earns a slot.
