#!/bin/sh
#
# civitai-manager installer.
#
#   curl -fsSL https://raw.githubusercontent.com/ZacxDev/civitai-manager/main/docs/install.sh | sh
#
# You are piping this into a shell, so it is written to be readable end to end
# and to do as little as possible:
#
#   * every URL is a hard-coded https://github.com URL — nothing is taken from
#     the environment or from a redirect and turned into a download target;
#   * the tarball is verified against the release's checksums.txt before it is
#     unpacked, and a mismatch aborts;
#   * it never runs sudo unless you asked for a prefix you cannot write, and it
#     tells you the exact command first;
#   * it only ever removes its own mktemp directory;
#   * POSIX sh — no bashisms.
#
# Options (flags, or the equivalent environment variables):
#   --version <x.y.z>   VERSION=   release to install (default: latest)
#   --prefix <dir>      PREFIX=    install prefix; binary goes in <prefix>/bin
#   --help
#
set -eu

REPO="ZacxDev/civitai-manager"
BINARY="civitai-manager"
RELEASES="https://github.com/${REPO}/releases"
API_LATEST="https://api.github.com/repos/${REPO}/releases/latest"

VERSION="${VERSION:-}"
PREFIX="${PREFIX:-}"
tmpdir=""

log() { printf '%s\n' "$*" >&2; }
die() {
	printf 'install.sh: error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat >&2 <<'EOF'
Install civitai-manager.

Usage:
  install.sh [--version <x.y.z>] [--prefix <dir>]

Options:
  --version <x.y.z>  Release to install. Default: the latest GitHub release.
                     Also settable as VERSION=.
  --prefix <dir>     Install prefix; the binary lands in <dir>/bin. Default:
                     /usr/local if writable, otherwise $HOME/.local.
                     Also settable as PREFIX=.
  --help             Show this message.

The downloaded archive is checked against the release's checksums.txt and the
install aborts on a mismatch.
EOF
}

cleanup() {
	if [ -n "$tmpdir" ] && [ -d "$tmpdir" ]; then
		rm -rf "$tmpdir"
	fi
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------- arguments --

while [ "$#" -gt 0 ]; do
	case "$1" in
	--version)
		[ "$#" -ge 2 ] || die "--version needs a value"
		VERSION="$2"
		shift 2
		;;
	--version=*)
		VERSION="${1#--version=}"
		shift
		;;
	--prefix)
		[ "$#" -ge 2 ] || die "--prefix needs a value"
		PREFIX="$2"
		shift 2
		;;
	--prefix=*)
		PREFIX="${1#--prefix=}"
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage
		die "unknown argument: $1"
		;;
	esac
done

# --------------------------------------------------------------- downloader --

if command -v curl >/dev/null 2>&1; then
	DOWNLOADER=curl
elif command -v wget >/dev/null 2>&1; then
	DOWNLOADER=wget
else
	die "need curl or wget on PATH"
fi

# fetch <url> <dest-file>
fetch() {
	case "$1" in
	https://*) ;;
	*) die "refusing to fetch a non-https URL: $1" ;;
	esac
	if [ "$DOWNLOADER" = curl ]; then
		curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1" ||
			die "download failed: $1"
	else
		wget -q --https-only -O "$2" "$1" ||
			die "download failed: $1"
	fi
}

# ------------------------------------------------------------------ version --

resolve_latest() {
	# Follow the /releases/latest redirect where we can: it needs no API token
	# and is not rate limited. Fall back to the API for wget.
	if [ "$DOWNLOADER" = curl ]; then
		effective="$(curl -fsSL --proto '=https' -o /dev/null \
			-w '%{url_effective}' "${RELEASES}/latest")" || return 1
		# .../releases/tag/v0.1.75 -> v0.1.75
		printf '%s\n' "${effective##*/}"
	else
		wget -q --https-only -O - "$API_LATEST" 2>/dev/null |
			sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
			head -n 1
	fi
}

if [ -z "$VERSION" ]; then
	log "==> resolving the latest release"
	VERSION="$(resolve_latest)" ||
		die "could not resolve the latest release; pass --version <x.y.z>"
fi

# Normalise "v0.1.75" and "0.1.75" to the bare number; the tag re-adds the v.
VERSION="${VERSION#v}"

# VERSION ends up inside a URL, so it is validated rather than trusted. Anything
# that is not a plain semver (optionally with a -prerelease/+build suffix) is
# rejected outright.
case "$VERSION" in
*[!0-9A-Za-z.+-]* | "") die "not a valid version: '$VERSION'" ;;
esac
if ! printf '%s' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([.+-][0-9A-Za-z.-]+)?$'; then
	die "not a valid version: '$VERSION' (expected something like 0.1.75)"
fi

TAG="v${VERSION}"

# ------------------------------------------------------------- os and arch --

os="$(uname -s)"
case "$os" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "unsupported operating system: $os (Windows users: download the .zip from ${RELEASES})" ;;
esac

arch="$(uname -m)"
case "$arch" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "unsupported architecture: $arch (released: amd64, arm64)" ;;
esac

ARCHIVE="${BINARY}_${VERSION}_${os}_${arch}.tar.gz"
ARCHIVE_URL="${RELEASES}/download/${TAG}/${ARCHIVE}"
SUMS_URL="${RELEASES}/download/${TAG}/checksums.txt"

# ------------------------------------------------------------------- prefix --

if [ -z "$PREFIX" ]; then
	# No sudo by surprise: /usr/local only when it is already writable,
	# otherwise a per-user prefix that needs no privileges at all.
	if [ -w /usr/local/bin ] 2>/dev/null; then
		PREFIX=/usr/local
	elif [ "$(id -u)" = "0" ]; then
		PREFIX=/usr/local
	else
		PREFIX="${HOME}/.local"
	fi
fi

BINDIR="${PREFIX}/bin"
DEST="${BINDIR}/${BINARY}"

# ---------------------------------------------------------------- checksums --

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | sed 's/.*= *//'
	else
		die "need sha256sum, shasum, or openssl to verify the download"
	fi
}

# ----------------------------------------------------------------- download --

tmpdir="$(mktemp -d)" || die "could not create a temporary directory"

log "==> civitai-manager ${VERSION} (${os}/${arch})"
log "==> downloading ${ARCHIVE}"
fetch "$ARCHIVE_URL" "${tmpdir}/${ARCHIVE}"
fetch "$SUMS_URL" "${tmpdir}/checksums.txt"

log "==> verifying checksum"
# Exact field match via awk, not a grep pattern: the file name contains dots and
# must not be treated as a regexp.
expected="$(awk -v want="$ARCHIVE" '$2 == want { print $1; exit }' "${tmpdir}/checksums.txt")"
[ -n "$expected" ] ||
	die "${ARCHIVE} is not listed in checksums.txt for ${TAG}"
printf '%s' "$expected" | grep -Eq '^[0-9a-fA-F]{64}$' ||
	die "checksums.txt has a malformed sha256 for ${ARCHIVE}"

actual="$(sha256_of "${tmpdir}/${ARCHIVE}")"
if [ "$expected" != "$actual" ]; then
	log ""
	log "CHECKSUM MISMATCH — refusing to install."
	log "  file:     ${ARCHIVE}"
	log "  expected: ${expected}"
	log "  actual:   ${actual}"
	log ""
	log "The download does not match the checksum published with the release."
	log "Do not run it. Retry, and if it happens again please open an issue at"
	log "https://github.com/${REPO}/issues."
	exit 1
fi
log "    sha256 ok (${actual})"

# ------------------------------------------------------------------ install --

mkdir -p "${tmpdir}/unpack"
tar -xzf "${tmpdir}/${ARCHIVE}" -C "${tmpdir}/unpack" "$BINARY" ||
	die "could not extract ${BINARY} from ${ARCHIVE}"
[ -f "${tmpdir}/unpack/${BINARY}" ] || die "${ARCHIVE} did not contain ${BINARY}"

SUDO=""
if [ ! -d "$BINDIR" ]; then
	if mkdir -p "$BINDIR" 2>/dev/null; then
		:
	elif [ "$(id -u)" != "0" ] && command -v sudo >/dev/null 2>&1; then
		log "==> ${BINDIR} does not exist and cannot be created as $(id -un)."
		log "    Running: sudo mkdir -p ${BINDIR}"
		sudo mkdir -p "$BINDIR" || die "could not create ${BINDIR}"
		SUDO="sudo"
	else
		die "cannot create ${BINDIR}; re-run with --prefix \$HOME/.local"
	fi
fi

if [ -z "$SUDO" ] && [ ! -w "$BINDIR" ]; then
	if [ "$(id -u)" = "0" ]; then
		die "${BINDIR} is not writable"
	elif command -v sudo >/dev/null 2>&1; then
		log "==> ${BINDIR} is not writable by $(id -un)."
		log "    Installing with: sudo cp ... ${DEST}"
		SUDO="sudo"
	else
		die "${BINDIR} is not writable and sudo is not available; re-run with --prefix \$HOME/.local"
	fi
fi

log "==> installing to ${DEST}"
# Stage next to the destination and rename, so an in-place upgrade is atomic and
# does not hit ETXTBSY against a running binary.
staged="${DEST}.new-$$"
$SUDO cp "${tmpdir}/unpack/${BINARY}" "$staged" || die "could not write ${staged}"
$SUDO chmod 0755 "$staged" || die "could not chmod ${staged}"
$SUDO mv -f "$staged" "$DEST" || die "could not move ${staged} into place"

# ------------------------------------------------------------------- report --

log ""
if "$DEST" --version >&2 2>/dev/null; then
	:
else
	log "installed ${DEST} (could not run it to print its version)"
fi

case ":${PATH}:" in
*":${BINDIR}:"*) ;;
*)
	log ""
	log "NOTE: ${BINDIR} is not on your PATH. Add it, e.g.:"
	log "    export PATH=\"${BINDIR}:\$PATH\""
	;;
esac

log ""
log "Next: ${BINARY} serve --comfy-model-path ~/ComfyUI/models"
