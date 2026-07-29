# Contributing & release process

## Development

Requires **Go 1.25+**. SQLite is the pure-Go `modernc.org/sqlite` driver, so
everything builds with `CGO_ENABLED=0` — no C toolchain needed.

```sh
gofmt -l .        # must print nothing
go build ./...
go vet ./...
go test ./...
```

CI (`.github/workflows/ci.yml`) runs the above on every push to `main` and every
pull request, plus `go test -race` on the concurrency-sensitive packages, a
compile-only check of the build-tagged integration suite (`go build -tags
integration ./...`), `shellcheck --shell=sh docs/install.sh`, and `nix flake
check`.

## The Nix flake

```sh
nix build .              # build the package
nix run .# -- --version  # run it
nix flake check -L       # what CI runs
nix develop              # Go toolchain, goreleaser, actionlint, shellcheck, nixfmt
nix fmt                  # nixfmt
```

**`vendorHash` in `flake.nix` is not derived from anything the Go build reads**,
so a `go get` silently invalidates it and `nix build
github:ZacxDev/civitai-manager` starts failing for everyone with a hash
mismatch. That has already happened once. After any `go.mod`/`go.sum` change:
run `nix build .`, take the `got:` hash out of the error, and paste it into
`vendorHash`. Never guess it. The `nix flake check` CI job exists purely to catch
this.

The flake's `version` is a hard-coded string that feeds the same `-X
main.version` ldflag GoReleaser uses. **Bump it in the commit you tag** (see
below) or Nix users get a binary whose `--version` reports the previous release.

## Live integration tests

The live suite in `internal/integration/` hits the real CivitAI API and is gated
by a build tag + env vars so ordinary `go test ./...` stays green offline. Run it
locally with a token:

```sh
make integration-test               CIVITAI_TOKEN=xxx   # read/metadata + poller
make integration-test-download      CIVITAI_TOKEN=xxx   # + real-bytes download
```

In CI it runs via `.github/workflows/integration.yml` — **manually**
(`workflow_dispatch`) or on a **daily schedule**. It never runs on ordinary
pushes/PRs and self-skips (with a notice, not a failure) when the token is
absent, so forks and secret-less runs are safe.

To enable it on this repo:

1. Add a repository **secret** `CIVITAI_TOKEN` with a valid CivitAI API token
   (Settings → Secrets and variables → Actions → New repository secret).
2. Optionally add repository **variables** to override the default live targets
   if the defaults ever drift upstream:
   - `CIVITAI_TEST_MODEL_ID` (default `4384`, DreamShaper)
   - `CIVITAI_TEST_DOWNLOAD_VERSION_ID` (default `9208`, a small embedding)

## Cutting a release

Releases are built by [GoReleaser](https://goreleaser.com) (config:
`.goreleaser.yaml`) and published to **GitHub Releases** by
`.github/workflows/release.yml`, which triggers on any pushed `v*` tag.

To cut a release: bump `version` in `flake.nix` to the new number, commit that on
`main`, then tag and push.

```sh
# 1. flake.nix:  version = "0.1.76";
git commit -m "chore: flake version 0.1.76" flake.nix
git tag v0.1.76
git push origin main v0.1.76
```

The workflow then cross-compiles `civitai-manager` for linux/darwin/windows on
amd64/arm64 (`CGO_ENABLED=0`) and publishes a GitHub Release containing:

- **tar.gz / zip archives** (with `README.md` + `LICENSE`) for all six targets,
- **`.deb` and `.rpm`** packages for linux amd64 and arm64,
- **`checksums.txt`**,
- a conventional-commit changelog,
- **build attestations** — `actions/attest` signs Sigstore provenance for every
  artifact listed in `checksums.txt`, keyless via the workflow's OIDC token. No
  secret involved. Users verify with
  `gh attestation verify --owner ZacxDev <file>`.

The version, commit, and build date are injected into the binary via ldflags and
are visible with `civitai-manager --version`.

### Homebrew — how the cask is published

`.goreleaser.yaml` has a working `homebrew_casks:` block (not the deprecated
`brews:`, which GoReleaser removes in v3), configured for **pull-request**
delivery so a compromised release workflow can only propose a tap change rather
than force-push one.

Both halves are now in place: **`ZacxDev/homebrew-tap`** is public with a `main`
branch and a `Casks/` directory, and the **`HOMEBREW_TAP_GITHUB_TOKEN`** secret
(a fine-grained PAT scoped to that tap only, **Contents: read/write** +
**Pull requests: read/write**) is set on this repo. A tagged release therefore
opens a PR against the tap with the regenerated cask. homebrew-core is not an
option: its policy requires 75 stars (225 to self-submit).

**Do not delete `homebrew_casks[0].skip_upload`.** It is not leftover
scaffolding from before the tap existed — it is the reason a release does not
die on the secret. `skip_upload` evaluates to `true` whenever
`HOMEBREW_TAP_GITHUB_TOKEN` is empty, and GoReleaser resolves it *before* it
expands `repository.token`. Without it, any run where the secret is absent,
revoked, unavailable (a fork, a re-run from a PR context) or simply expired
fails template expansion and takes the whole release down — and by that point
the GitHub Release has already been published, because GoReleaser runs
`release.Pipe{}` before `cask.Pipe{}`. With the guard, the cask is skipped, the
`dist/homebrew/Casks/civitai-manager.rb` artifact is still written for
inspection, and the release stands.

If a release lands without a tap PR, check the secret before assuming a bug —
that is exactly what the guard looks like when it fires.

Notes:
- Use [conventional commit](https://www.conventionalcommits.org/) prefixes
  (`feat:`, `fix:`, `docs:`) so the generated changelog groups cleanly.
- Validate config changes locally without publishing:
  ```sh
  goreleaser check
  goreleaser release --snapshot --clean   # writes ./dist, no upload
  ```
