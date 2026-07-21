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
pull request, plus `go test -race` on the concurrency-sensitive packages and a
compile-only check of the build-tagged integration suite (`go build -tags
integration ./...`).

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

To cut a release, tag a commit on `main` with a semver tag and push it:

```sh
git tag v0.1.0
git push origin v0.1.0
```

That is the **only** step. The workflow then cross-compiles `civitai-manager`
for linux/darwin/windows on amd64/arm64 (`CGO_ENABLED=0`), builds tar.gz/zip
archives (with `README.md` + `LICENSE`), a `checksums.txt`, and a
conventional-commit changelog, and uploads them all to a new GitHub Release. The
version, commit, and build date are injected into the binary via ldflags and are
visible with `civitai-manager --version`.

Notes:
- Use [conventional commit](https://www.conventionalcommits.org/) prefixes
  (`feat:`, `fix:`, `docs:`) so the generated changelog groups cleanly.
- Homebrew/tap publishing is **not** configured (it needs a separate tap repo +
  token). A commented stub is in `.goreleaser.yaml` if you want it later.
- Validate config changes locally without publishing:
  ```sh
  goreleaser check
  goreleaser release --snapshot --clean   # writes ./dist, no upload
  ```
