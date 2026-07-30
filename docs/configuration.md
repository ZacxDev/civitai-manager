# Configuration & authentication

- [Where the config lives](#where-the-config-lives)
- [Resolution precedence](#resolution-precedence)
- [The CivitAI API token](#the-civitai-api-token)
- [Full config file](#full-config-file)
- [ComfyUI settings](#comfyui-settings)
- [Output gallery storage and its automatic deletion](#output-gallery-storage-and-its-automatic-deletion)
- [What talks to the network](#what-talks-to-the-network)
- [Security notes](#security-notes)

## Where the config lives

The config file location honours `XDG_CONFIG_HOME`, defaulting to
`~/.config/civitai-manager/config.yaml`. Override it with `--config <path>`.

The database defaults to `~/.config/civitai-manager/civitai-manager.db`.

## Resolution precedence

Settings resolve in this order (highest first):

1. Command-line flag (`--token`, `--base-url`, `--model-root`, `--db`, …)
2. Environment variable — `CIVITAI_TOKEN`, `COMFY_TOKEN`, `HF_TOKEN` (tokens only)
3. Config file
4. The official [`civitai` CLI](https://github.com/civitai/cli)'s config, if
   present — `~/.config/civitai/config.yaml`, the `token:` field (CivitAI token
   only, lowest precedence)
5. Built-in defaults

## The CivitAI API token

The public read endpoints work anonymously; a **token is required to download
most files**.

All three tokens (`token`, `comfy_token`, `hf_token`) are treated as secrets and
are **never logged** — diagnostic output redacts them to `****abcd`.

> **Already using the official `civitai` CLI?** Its login token lives in
> `~/.config/civitai/config.yaml` under `token:`. civitai-manager reads that as a
> last-resort fallback automatically, so you may not need to configure a token at
> all. To be explicit instead, copy that value into `CIVITAI_TOKEN`, pass
> `--token`, or set `token:` in your own config. A missing or unreadable
> official-CLI config is ignored.

## Full config file

Every key is optional; the values shown are the defaults.

```yaml
# --- CivitAI ---
token: ""                          # or set CIVITAI_TOKEN
base_url: "https://civitai.com"
model_root: "~/civitai-models"     # where downloads are laid out
default_poll_interval: "1h"        # floored at 15m (the API edge-caches ~5m)
download_jitter: "15m"             # anti-stampede window; "0" = start at once
max_file_size: ""                  # e.g. "2GB"; empty/"0" = unlimited
no_preview: false                  # true = never write the .preview.png sidecar
max_preview_size: ""               # e.g. "2MB"; skip a preview larger than this
                                   # (empty/"0" = no cap). The model file and
                                   # .civitai.info are always written regardless.

# --- Web UI ---
addr: "127.0.0.1:8787"             # loopback by default. Read the security notes
                                   # below before binding a LAN address.
web_scan_timeout: "2m"             # deadline for a web "Scan now" walk/hash
web_scan_max_files: 50000          # model-file cap for a web scan; over → aborts

# --- Library ---
library_paths: []                  # extra directories the library scan walks.
                                   # Point these at an existing ComfyUI/A1111
                                   # models folder to inventory it in place.
trash_dir: ""                      # empty = <model_root>/.trash
library_extensions: []             # empty = the built-in model-extension set

# --- ComfyUI ---
comfy_url: "http://127.0.0.1:8188" # local ComfyUI. CONFIG-ONLY by design — it is
                                   # never a per-request parameter, so a run
                                   # endpoint cannot be aimed at another target.
comfy_token: ""                    # or COMFY_TOKEN; only for a ComfyUI behind a
                                   # login node
comfy_model_path: ""               # ComfyUI's models/ dir. Required for the
                                   # download-a-missing-model flow; when empty
                                   # that flow is disabled.
comfy_root: ""                     # ComfyUI install root (holds custom_nodes/).
                                   # Only used by the helper-extension install.
                                   # Derived from comfy_model_path's parent when
                                   # that looks like a ComfyUI install.
# comfy_cloud: true                 # opt-in: enable "Run on CivitAI Cloud".
                                   # COMMENTED OUT ON PURPOSE — this sample is
                                   # meant to be copied, and the recommended
                                   # setup is to LEAVE THE KEY OUT entirely so
                                   # the web UI owns it (workflow detail → Run on
                                   # CivitAI Cloud → the on/off toggle, stored in
                                   # the DB). Uncommenting it EITHER WAY, true or
                                   # false, wins over that toggle, which then
                                   # renders read-only — so a pasted
                                   # `comfy_cloud: false` does not mean "off for
                                   # now", it means "off, and the UI can no
                                   # longer turn it on".
                                   # Cloud runs authenticate with the `token`
                                   # below — there is no separate cloud
                                   # credential, and none can be entered in the
                                   # web UI.

# --- Output gallery ---
outputs_dir: ""                    # empty = <db-dir>/outputs
outputs_max_bytes: "20GB"          # total size cap. Over it, the OLDEST
                                   # generations are DELETED. "0" = unlimited.

# --- HuggingFace fallback ---
hf_fallback: true                  # default ON; see the egress note below
hf_token: ""                       # or HF_TOKEN; optional, anonymous works

# --- Custom-node attribution ---
resolve_node_packs: true           # default ON; see the egress note below

# --- Storage ---
# db_path: "~/.config/civitai-manager/civitai-manager.db"
```

## ComfyUI settings

Two different directories are involved, and they are easy to confuse:

| Setting | Points at | Used for |
| --- | --- | --- |
| `comfy_model_path` | ComfyUI's `models/` folder (the one holding `checkpoints/`, `loras/`, `vae/`, …) | Writing a **missing model** into the correct per-type subfolder so a local run can find it. Optional — when empty, the missing-model panel degrades to CivitAI links only. |
| `comfy_root` | The ComfyUI **install** root (the folder holding `custom_nodes/`, `main.py`, `models/`) | The explicit, user-triggered "install the ComfyUI helper extension" action only. |

Neither is the same as `model_root`, which is civitai-manager's own download
layout for subscriptions.

When `comfy_model_path` is set it must be an **existing, writable directory** or
config resolution fails at startup with a clear error.

## Output gallery storage and its automatic deletion

Every successful ComfyUI workflow run has its output images **copied** into an
app-owned directory — `outputs_dir` / `--outputs-dir`, defaulting to
`<db-dir>/outputs` (i.e. `~/.config/civitai-manager/outputs`) — and recorded in
the database, so they stay browsable under **Outputs** after ComfyUI clears its
own output folder.

> ⚠️ **That directory is size-capped, and the cap deletes your images.** Once the
> total recorded size of the gallery exceeds `outputs_max_bytes`
> (`--outputs-max-bytes`), the app **automatically deletes the OLDEST generations
> — database rows and image files — until it is back under the cap.** There is
> **no undo and no trash**; deleted generations are gone. Each eviction is logged
> at INFO level with the generation id and the bytes reclaimed.
>
> - **Default: `"20GB"`.** Set it to any size string (`"500MB"`, `"2GB"`) or a
>   plain byte count.
> - **`"0"` means unlimited** — nothing is ever auto-deleted, and the directory
>   grows without bound.
> - A positive cap below 1 MiB is **rejected at startup** — almost always a unit
>   mistake (e.g. `outputs_max_bytes: 20` meaning 20 GB).
> - Eviction runs **only after a successful capture**, so lowering the cap does
>   nothing until the next successful run.
> - Keep anything you care about somewhere else — copy it out of the gallery.

## What talks to the network

civitai-manager is local-first, but it is not hermetic. These are the outbound
paths, so you can make an informed decision about each:

| Path | Destination | Default | Turn it off with |
| --- | --- | --- | --- |
| Search, model metadata, subscriptions, downloads | `civitai.com` | on (this is the point of the tool) | — |
| **Library scan hash matching** — sends your files' **SHA256 hashes** to CivitAI to identify them | `civitai.com` | **on** | CLI `scan --no-remote`; in the web UI untick "Match against CivitAI" |
| **HuggingFace fallback resolver** — when CivitAI has no match for a missing model filename, that **filename** is sent to HuggingFace to look for a download | `huggingface.co` | **on** | `hf_fallback: false` |
| **Custom-node attribution** — when a run reports missing ComfyUI node types a local ComfyUI-Manager could not place, those **node class names** are sent to the Comfy Registry and to ComfyUI-Manager's static index to find which pack provides them | `api.comfy.org`, `raw.githubusercontent.com` | **on** | `resolve_node_packs: false` |
| Workflow runs / preflight | your `comfy_url` (loopback by default) | on | — |
| **CivitAI Cloud runs** — sends the graph + resource list to CivitAI **and spends Buzz** from the account behind your token | `civitai.com` | **off** | opt in with `comfy_cloud: true`, or the toggle on a workflow's "Run on CivitAI Cloud" block |

The `hf_token` is sent **only** to HuggingFace hosts — never to civitai.com and
never to a CDN redirect target. The fallback works fully anonymously without it
for the public repos it targets.

Custom-node attribution asks a locally-installed **ComfyUI-Manager first** (that
is loopback and is never affected by `resolve_node_packs`); only the node class
names Manager could not place go out to the two public indexes, and the answers
are cached. With `resolve_node_packs: false` nothing leaves the machine and node
types Manager cannot place are simply reported as unmatched.

The web UI itself loads **no** external resources: the CSS and `htmx.min.js` are
vendored into the binary with `go:embed`. There is no CDN, no external font, and
no analytics.

## Security notes

> **The UI has no login.** It binds `127.0.0.1:8787` by default, so it is not
> reachable from other machines. Its only protection is a per-process CSRF token
> on the state-changing forms. Binding `--addr` to a non-loopback interface
> exposes an **unauthenticated** UI to anyone who can reach that interface — only
> do so on a trusted network, or put it behind your own auth proxy.

Specific things a non-loopback bind changes or exposes:

- **Arbitrary-path capabilities are disabled.** Because the Library "Scan now"
  form can walk and hash host directories, the **extra-scan-path input is not
  rendered on a non-loopback bind**, and any submitted `scan_paths` is rejected.
  A LAN-exposed server may only scan `model_root` and configured
  `library_paths`.
- **Your generated images are exposed.** The output gallery (`/outputs`) and the
  image bytes it serves (`/outputs/img/{id}`) are readable by any client that can
  reach the port — by design, so that a deliberately LAN-exposed instance still
  has a working gallery. The **"Recent outputs" sidebar** puts the most recent
  thumbnails and their workflow names on *every* page, so they are the first
  thing such a client sees.
- **NSFW "Blur" is not a privacy control.** NSFW previews are blurred by default,
  but blur is a CSS filter applied in the browser — the unblurred bytes still go
  over the wire.

Even on loopback, the web scan is confined and bounded:

- It refuses `/`, system directories (`/etc`, `/proc`, `/usr`, …), and your
  `$HOME` root itself.
- It is bounded by a deadline (`--web-scan-timeout`, default `2m`) and a
  model-file cap (`web_scan_max_files`, default 50 000), so an over-broad path
  aborts with a "narrow the path" message instead of tying up the server.
- The interactive **directory browser** is additionally constrained to paths
  within `$HOME`, `model_root`, and configured `library_paths` — it will not
  enumerate unrelated locations such as `/root` or another user's home, and the
  constraint is enforced on the symlink-resolved real path.

The CLI `scan` is deliberately **unbounded** — you typed the path knowingly —
though it is equally cancellable (Ctrl-C aborts the walk promptly).
