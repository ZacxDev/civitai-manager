#!/usr/bin/env bash
#
# The reachability gate. Runs golang.org/x/tools/cmd/deadcode across every GOOS
# this repo ships and fails when unreachable code appears.
#
# WHY THIS EXISTS: CLAUDE.md's "Reachability" section records this repo's real
# recurring defect — not broken code, code that never runs. A deliberate sweep
# found 17 confirmed-dead items (11 functions, an orphan route, a flag plumbed
# through five layers and read by nobody for 89 releases). Nothing on the
# standard gate — build, vet, test, -race, gofmt — can see any of them.
#
# TWO TIERS, because -test answers two different questions and only one of them
# can hold a zero:
#
#   Tier A (-test):  "is anything at all reachable to this function, tests
#                     included?"  A hit here has NO caller anywhere in the repo.
#                     Expected set: EMPTY. No allowlist, so it cannot rot.
#
#   Tier B (default): "is this reachable from PRODUCTION roots?"  A hit here is
#                     reachable only from its own tests. This is the tier that
#                     would have caught the 17-item sweep; Tier A would have
#                     caught none of them, because each dead helper had tests.
#                     Expected set: exactly the checked-in allowlist below.
#
# 🔴 DO NOT READ A GREEN TIER A AS "NOTHING IS DEAD" — it is the WEAK tier, and
# measurably so. Under -test, RTA finds this path:
#
#   $ deadcode -test -whylive='…/internal/queue.Worker.ProcessOne' ./...
#     civitai-manager.main -> internal/cli.Execute -> cobra.Command.Execute
#       -> … -> cobra.tmpl -> text/template.Template.Execute -> …
#       -> reflect.Value.Call
#       dynamic --> internal/queue.TestNoPreviewSkipsPreviewSidecar
#       static  --> internal/queue.Worker.ProcessOne
#
# One reflect.Value.Call inside cobra's help templating makes RTA treat EVERY
# address-taken function as callable, which pulls the entire test corpus into the
# reachable set — and with it everything the tests touch. So Tier A can only ever
# report a function that literally nothing references. That is still worth
# gating (it is exactly the five deleted alongside this commit), but it is a
# floor, not a measurement. Tier B is where the real signal is: the same
# ProcessOne is reported dead the moment -test comes off.
#
# Both tiers over-approximate reachability SOUNDLY (dynamic calls are followed),
# so the failure direction is false NEGATIVES — dead code the gate misses — never
# a false accusation. A report is strong evidence; confirm with grep before
# deleting anything exported.
#
# Tier B's allowlist is ASSERTED, not skipped — the same discipline as
# contrast_web_test.go's 25 accepted debt entries. It fails on a NEW entry
# (something became production-unreachable) AND on a STALE one (an entry that is
# no longer dead: delete the line and take the win). Do not add an entry to make
# a red build green without saying, in the commit message, why the function must
# stay.
#
# 🔴 THE INTERSECTION IS LOAD-BEARING, NOT TIDINESS. internal/diskusage is split
# by build tag: fromBlocks (usage_unix.go, //go:build unix) and fromByteCounts /
# diskFreeSpaceExOut.usage (usage_windows.go) are each dead on one GOOS and live
# on the other. A single-GOOS run reports whichever one as a false positive. Only
# the intersection is trustworthy on a repo that cross-compiles to 6 targets.
#
# 🔴 HOW THIS HARNESS AVOIDS A FALSE GREEN. An empty deadcode result is
# indistinguishable from a deadcode that never ran — which is exactly how
# `GOOS=windows go run <pkg>@latest` fails: it CROSS-COMPILES THE TOOL, dies with
# `exec format error`, and with stderr swallowed reads as "0 dead functions".
# Three structural defences:
#   1. The tool is built ONCE for the host; GOOS is set only for the analysis.
#      Never `go run <pkg>@latest` under a foreign GOOS.
#   2. Every deadcode invocation's exit status is checked and its stderr is
#      shown. A load failure fails the job instead of printing nothing.
#   3. Tier B's expected set is NON-EMPTY, so a silently-broken analysis reports
#      every allowlist entry as "no longer dead" and goes RED. That makes Tier
#      A's zero trustworthy: the two tiers share one tool invocation path, and
#      Tier B is its negative control.
#
# Usage:
#   .github/deadcode.sh            # gate (this is what CI runs)
#   .github/deadcode.sh --print    # print the current sets, gate nothing
#
# Requires Go >= the nested module's directive (e2e/uxaudit/go.mod), because one
# tool binary analyzes both modules.
set -euo pipefail

# A set CDPATH makes `cd` ECHO the directory it landed in, which lands inside
# every $(cd … && pwd) below and yields a two-line path. Caught here for real.
unset CDPATH

# Pinned, never @latest: CI must be reproducible, and a tool that silently
# changes its analysis changes what this gate means.
DEADCODE_VERSION="v0.48.0"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ALLOWLIST="${REPO_ROOT}/.github/deadcode-allow.txt"
NESTED="e2e/uxaudit"
GOOSES=(linux windows darwin)

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# One line per dead function, as `<import path>.<func>`. Deliberately NOT the
# default file:line format: a line number churns on every edit above it, so an
# allowlist keyed on it would go stale for reasons unrelated to reachability.
# shellcheck disable=SC2016  # $p is a Go template variable; the shell must NOT expand it.
FORMAT='{{$p := .Path}}{{range .Funcs}}{{printf "%s.%s\n" $p .Name}}{{end}}'

die() { printf '%s\n' "$*" >&2; exit 1; }

# --- toolchain check -------------------------------------------------------
# The nested module declares a newer Go than the root one. One tool binary has
# to load both, so the local toolchain must satisfy the higher directive. Fail
# with the actual numbers rather than letting deadcode emit 200 lines of
# "package requires newer Go version".
need="$(awk '$1 == "go" { print $2; exit }' "${REPO_ROOT}/${NESTED}/go.mod")"
have="$(go env GOVERSION)"                       # e.g. go1.26.0
have="${have#go}"
ver_ge() { [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -1)" = "$2" ]; }
ver_ge "$have" "$need" || die "deadcode gate: need Go >= ${need} (${NESTED}/go.mod); have ${have}"

# --- build the tool ONCE, for the HOST -------------------------------------
if [ -n "${DEADCODE_BIN:-}" ]; then
  DC="$DEADCODE_BIN"
else
  printf 'installing deadcode %s\n' "$DEADCODE_VERSION" >&2
  GOBIN="$WORK/bin" go install "golang.org/x/tools/cmd/deadcode@${DEADCODE_VERSION}"
  DC="$WORK/bin/deadcode"
fi
[ -x "$DC" ] || die "deadcode gate: tool not executable at $DC"

# run <outfile> <goos> <dir> [extra flags...]
run() {
  local out="$1" goos="$2" dir="$3"; shift 3
  ( cd "${REPO_ROOT}/${dir}" && GOOS="$goos" "$DC" -f="$FORMAT" "$@" ./... ) \
    | LC_ALL=C sort -u > "$out" \
    || die "deadcode gate: deadcode failed for GOOS=${goos} in ${dir:-.} (see stderr above)"
}

# intersect <outfile> <n> <infiles...> — keep lines present in ALL n inputs.
intersect() {
  local out="$1" n="$2"; shift 2
  LC_ALL=C sort "$@" | uniq -c | awk -v n="$n" '$1 == n { $1 = ""; sub(/^ +/, ""); print }' \
    | LC_ALL=C sort > "$out"
}

# --- Tier A: dead with -test, i.e. no caller anywhere ----------------------
tier_a=()
for goos in "${GOOSES[@]}"; do
  run "$WORK/a-root-$goos" "$goos" "." -test
  run "$WORK/a-nested-$goos" "$goos" "$NESTED" -test
  cat "$WORK/a-root-$goos" "$WORK/a-nested-$goos" | LC_ALL=C sort -u > "$WORK/a-$goos"
  tier_a+=("$WORK/a-$goos")
done
intersect "$WORK/tier-a.txt" "${#GOOSES[@]}" "${tier_a[@]}"

# --- Tier B: dead without -test, i.e. no PRODUCTION caller -----------------
# Root module only. The nested module has NO main package — measured:
#   $ cd e2e/uxaudit && deadcode ./...   ->  "deadcode: no main packages"
# deadcode's RTA starts only from main packages, so the default mode cannot
# analyze that module at all. It is covered by Tier A, which roots at its test
# binaries. This is a structural fact, not an omission.
tier_b=()
for goos in "${GOOSES[@]}"; do
  run "$WORK/b-$goos" "$goos" "."
  tier_b+=("$WORK/b-$goos")
done
intersect "$WORK/tier-b.txt" "${#GOOSES[@]}" "${tier_b[@]}"

if [ "${1:-}" = "--print" ]; then
  printf '# Tier A — dead WITH -test (no caller anywhere). Expected: empty.\n'
  cat "$WORK/tier-a.txt"
  printf '\n# Tier B — dead WITHOUT -test (no production caller).\n'
  cat "$WORK/tier-b.txt"
  exit 0
fi

fail=0

if [ -s "$WORK/tier-a.txt" ]; then
  fail=1
  cat >&2 <<'EOF'

DEAD CODE (tier A): unreachable on linux AND windows AND darwin, even counting
tests as roots. Nothing in this repo calls these. Delete them.

EOF
  sed 's/^/  /' "$WORK/tier-a.txt" >&2
fi

[ -f "$ALLOWLIST" ] || die "deadcode gate: missing ${ALLOWLIST}"
grep -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' "$ALLOWLIST" | LC_ALL=C sort -u > "$WORK/allow.txt"

new="$(LC_ALL=C comm -23 "$WORK/tier-b.txt" "$WORK/allow.txt")"
stale="$(LC_ALL=C comm -13 "$WORK/tier-b.txt" "$WORK/allow.txt")"

if [ -n "$new" ]; then
  fail=1
  cat >&2 <<'EOF'

DEAD CODE (tier B): no PRODUCTION caller on any GOOS — reachable only from its
own tests. This is the shape that shipped an inert ComfyUI model cache, an
unreachable node-pack install UI and a flag read by nobody for 89 releases.

Delete it, or wire it up. Adding it to .github/deadcode-allow.txt is a last
resort and the commit message must say why the function has to stay.

EOF
  printf '%s\n' "$new" | sed 's/^/  /' >&2
fi

if [ -n "$stale" ]; then
  fail=1
  cat >&2 <<'EOF'

STALE ALLOWLIST ENTRIES: these are listed in .github/deadcode-allow.txt but are
no longer dead. Delete the lines and take the win. (If you did not expect this,
suspect the analysis itself — an entry cannot stop being dead by accident.)

EOF
  printf '%s\n' "$stale" | sed 's/^/  /' >&2
fi

if [ "$fail" -ne 0 ]; then
  printf '\ndeadcode gate FAILED\n' >&2
  exit 1
fi

printf 'deadcode gate OK — tier A empty; tier B matches %d allowlisted entries.\n' \
  "$(wc -l < "$WORK/allow.txt")"
