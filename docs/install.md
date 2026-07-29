# Installing civitai-manager

civitai-manager is a single static Go binary. There is no runtime to install, no
database server, and no container. Pick whichever of these suits you.

> **What ships when.** The install script, `.deb`/`.rpm` packages, the Homebrew
> cask and build attestations all arrive with **v0.1.76**. Releases up to and
> including v0.1.75 have tarballs, zips and `checksums.txt` only. The Nix flake
> and `go install` work today.

| Method | Best for |
| --- | --- |
| [Install script](#install-script) | Linux/macOS, one command, verifies the download |
| [Homebrew](#homebrew) | macOS and Linux users already on brew |
| [Nix](#nix) | Reproducible installs, or just trying it once |
| [.deb / .rpm](#deb--rpm) | Debian/Ubuntu, Fedora/RHEL/openSUSE |
| [Manual download](#manual-download) | Windows, or when you want to see every step |
| [go install](#go-install) | You have Go 1.25+ and want the tip of `main` |

---

## Install script

```sh
curl -fsSL https://zacxdev.github.io/civitai-manager/install.sh | sh
```

It detects your OS and CPU, downloads the matching release tarball, **checks it
against the release's `checksums.txt`, and aborts if they disagree**, then
installs the binary.

Options, as flags or environment variables:

```sh
# a specific release
curl -fsSL https://zacxdev.github.io/civitai-manager/install.sh | sh -s -- --version 0.1.76

# a specific prefix — the binary lands in <prefix>/bin
curl -fsSL https://zacxdev.github.io/civitai-manager/install.sh | sh -s -- --prefix ~/.local

# same thing with env vars
curl -fsSL https://zacxdev.github.io/civitai-manager/install.sh | VERSION=0.1.76 PREFIX=/opt/cm sh
```

**It will not quietly ask for your password.** With no `--prefix` it installs to
`/usr/local` only if `/usr/local/bin` is already writable by you, and otherwise
falls back to `$HOME/.local` — which needs no privileges at all. If you name a
prefix you can't write, it prints the exact `sudo` command before running it.

If you would rather read it first — which is the right instinct for anything you
pipe into a shell — it is [`docs/install.sh`](install.sh) in this repository:

```sh
curl -fsSL https://zacxdev.github.io/civitai-manager/install.sh -o install.sh
less install.sh
sh install.sh
```

The same file is served straight from the default branch at
`https://raw.githubusercontent.com/ZacxDev/civitai-manager/main/docs/install.sh`
if you prefer that origin.

---

## Homebrew

> **From v0.1.76 on.** [`ZacxDev/homebrew-tap`](https://github.com/ZacxDev/homebrew-tap)
> is live and the release workflow publishes the cask to it, but the cask file
> is written by the release itself — so the commands below only work once a
> release from **v0.1.76** onwards has been cut. Check the
> [releases page](https://github.com/ZacxDev/civitai-manager/releases).

civitai-manager is distributed as a **cask** from a personal tap. Casks cover
Linux as well as macOS these days, so the same command works on both.

Since **Homebrew 6.0.0** (June 2026), a non-official tap must be explicitly
**trusted** before Homebrew will load code from it, and an untrusted tap is not
auto-loaded. There are two ways to satisfy that.

**Trust just this cask** (recommended — installing a fully qualified name trusts
only that one item):

```sh
brew install --cask ZacxDev/tap/civitai-manager
```

**Or trust the tap**, if you would rather install by short name afterwards:

```sh
brew tap ZacxDev/tap
brew trust --cask ZacxDev/tap/civitai-manager   # or: brew trust ZacxDev/tap
brew install --cask civitai-manager
```

`brew trust ZacxDev/tap` trusts every current *and future* formula, cask and
external command in that tap; `brew trust --cask ZacxDev/tap/civitai-manager`
trusts only this one. Prefer the narrower grant.

Upgrade and removal are the usual:

```sh
brew upgrade --cask civitai-manager
brew uninstall --cask civitai-manager
```

The released binaries are not code-signed or notarized, so the cask strips
`com.apple.quarantine` at install time. Without that, macOS refuses the first
run with "cannot be opened because the developer cannot be verified".

---

## Nix

Run it once without installing anything:

```sh
nix run github:ZacxDev/civitai-manager -- serve --comfy-model-path ~/ComfyUI/models
```

Install it into your profile:

```sh
nix profile install github:ZacxDev/civitai-manager
```

Use it from your own flake:

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    civitai-manager.url = "github:ZacxDev/civitai-manager";
    civitai-manager.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs = { nixpkgs, civitai-manager, ... }: {
    # either the package directly …
    packages.x86_64-linux.default =
      civitai-manager.packages.x86_64-linux.default;

    # … or the overlay, which adds pkgs.civitai-manager
    # nixpkgs.overlays = [ civitai-manager.overlays.default ];
  };
}
```

A `devShells.default` with the Go toolchain, GoReleaser, actionlint, shellcheck
and nixfmt is available for hacking on the project:

```sh
nix develop github:ZacxDev/civitai-manager
```

---

## .deb / .rpm

Every release attaches `.deb` and `.rpm` packages for amd64 and arm64. They
install the binary to `/usr/bin/civitai-manager` and the licence and README to
`/usr/share/doc/civitai-manager/`.

```sh
VERSION=0.1.76
ARCH=amd64   # or arm64

# Debian / Ubuntu
curl -fsSLO "https://github.com/ZacxDev/civitai-manager/releases/download/v${VERSION}/civitai-manager_${VERSION}_linux_${ARCH}.deb"
sudo dpkg -i "civitai-manager_${VERSION}_linux_${ARCH}.deb"

# Fedora / RHEL / openSUSE
curl -fsSLO "https://github.com/ZacxDev/civitai-manager/releases/download/v${VERSION}/civitai-manager_${VERSION}_linux_${ARCH}.rpm"
sudo rpm -i "civitai-manager_${VERSION}_linux_${ARCH}.rpm"
```

There is no apt or yum repository — upgrades mean downloading the newer package.
If you want unattended upgrades, use the install script or Nix instead.

---

## Manual download

```sh
VERSION=0.1.76
FILE="civitai-manager_${VERSION}_linux_amd64.tar.gz"   # or darwin_arm64, linux_arm64, darwin_amd64

curl -fsSLO "https://github.com/ZacxDev/civitai-manager/releases/download/v${VERSION}/${FILE}"
curl -fsSLO "https://github.com/ZacxDev/civitai-manager/releases/download/v${VERSION}/checksums.txt"
sha256sum --check --ignore-missing checksums.txt
tar -xzf "$FILE"
sudo install civitai-manager /usr/local/bin/
```

**Windows**: download `civitai-manager_<version>_windows_amd64.zip` (or
`windows_arm64.zip`) from the
[releases page](https://github.com/ZacxDev/civitai-manager/releases/latest),
unzip it, and put `civitai-manager.exe` somewhere on your `PATH`. Verify the zip
against `checksums.txt` first:

```powershell
Get-FileHash .\civitai-manager_0.1.76_windows_amd64.zip -Algorithm SHA256
```

---

## go install

```sh
go install github.com/ZacxDev/civitai-manager@latest
```

Needs Go 1.25+. This builds from source, so the binary reports its version from
the module metadata rather than from release ldflags.

## From source

```sh
git clone https://github.com/ZacxDev/civitai-manager
cd civitai-manager
go build -o civitai-manager .
```

SQLite is the pure-Go `modernc.org/sqlite` driver, so everything builds with
`CGO_ENABLED=0` — no C toolchain, no build tags, and it cross-compiles trivially.

---

## Verifying a download

Two independent checks are available.

**1. Checksums.** Every release publishes `checksums.txt`:

```sh
sha256sum --check --ignore-missing checksums.txt
```

This proves the file you have matches the file the release lists. It does not by
itself prove where that file came from.

**2. Build provenance.** From v0.1.76, every published artifact carries a
[GitHub artifact attestation](https://docs.github.com/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds)
— a Sigstore-signed statement of which repository, workflow and commit produced
it. Verify it with the GitHub CLI:

```sh
gh attestation verify --owner ZacxDev civitai-manager_0.1.76_linux_amd64.tar.gz
```

A pass means that exact file was produced by a workflow run in
`ZacxDev/civitai-manager`. Signing is keyless via OIDC, so there is no public key
to fetch and no signing key that can leak. Tarballs, zips, `.deb` and `.rpm` are
all covered.

---

## Where its data lives

Nothing above touches your configuration or database. State is a single SQLite
file, by default at `~/.config/civitai-manager/civitai-manager.db`, so
uninstalling the binary leaves your subscriptions and library index intact. See
[configuration](configuration.md) for how to move it.

## After installing

```sh
civitai-manager serve --comfy-model-path ~/ComfyUI/models
# → http://localhost:8787
```

`--comfy-model-path` is what lets it download a missing model into the right
place. Set it once in the [config file](configuration.md) so you don't have to
pass it every time.
