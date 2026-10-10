#!/usr/bin/env bash
# Pull-request gate for govulncheck: fails only on called vulnerabilities that
# the change introduces, i.e. that are absent at the base revision.
#
# A new advisory makes every revision fail at the same moment, including the
# Renovate pull requests that each fix one half of it (e.g. a Go toolchain
# release and a golang.org/x/net release for the same HTTP/2 issue). A plain
# `govulncheck ./...` gate then blocks all of them until someone combines the
# fixes by hand. Vulnerabilities already present on the base revision still
# fail the full `make vuln` run on pushes to main, which blocks the release.
#
# Each revision is scanned with the Go toolchain its go.mod declares, so a
# change of the Go version is judged by its standard-library vulnerabilities.
#
# Usage: hack/govulncheck-diff.sh <base-ref>
#   GOVULNCHECK  govulncheck binary (default: govulncheck on PATH)
set -euo pipefail

base_ref="${1:?usage: $0 <base-ref>}"
govulncheck="${GOVULNCHECK:-govulncheck}"

workdir="$(mktemp -d)"
cleanup() {
  git worktree remove --force "$workdir/base" >/dev/null 2>&1 || true
  rm -rf "$workdir"
}
trap cleanup EXIT

# toolchain_of <dir>: the toolchain name for the go directive of <dir>/go.mod.
toolchain_of() {
  local version
  # Parsed directly: `go mod edit` refuses to run when go.mod requires a newer
  # Go than the installed one and GOTOOLCHAIN=local.
  version="$(awk '$1 == "go" { print $2; exit }' "$1/go.mod")"
  [[ "$version" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || { echo "unexpected go directive '$version' in $1/go.mod" >&2; return 1; }
  # "go 1.27" names the language version; its first release is go1.27.0.
  [[ "$version" =~ ^[0-9]+\.[0-9]+$ ]] && version="${version}.0"
  echo "go${version}"
}

# scan <dir> <json-out>: govulncheck JSON stream for <dir>. Runs outside of a
# command substitution so that `set -e` aborts on a failed scan instead of
# treating it as "no findings".
scan() {
  local dir="$1" out="$2" toolchain
  toolchain="$(toolchain_of "$dir")"
  echo "Scanning $dir with $toolchain..."
  (cd "$dir" && GOTOOLCHAIN="$toolchain" GOFLAGS=-buildvcs=false "$govulncheck" -format json ./...) >"$out"
}

# called <json>: sorted "<OSV ID> <module>" pairs for every vulnerable symbol
# the code reaches. The module (stdlib, golang.org/x/net, ...) is part of the
# key because one advisory can cover several modules: downgrading x/net must
# fail even while the Go toolchain is still affected by the same advisory.
# Package- and module-level findings (imported or required, but not called)
# are left out, matching what fails `govulncheck` in text mode.
called() {
  jq -r 'select(.finding != null and .finding.trace[0].function != null)
    | "\(.finding.osv) \(.finding.trace[0].module)"' "$1" | sort -u
}

git worktree add --detach "$workdir/base" "$base_ref" >/dev/null

scan . "$workdir/head.json"
scan "$workdir/base" "$workdir/base.json"
called "$workdir/head.json" >"$workdir/head.called"
called "$workdir/base.json" >"$workdir/base.called"
comm -23 "$workdir/head.called" "$workdir/base.called" >"$workdir/introduced"
comm -12 "$workdir/head.called" "$workdir/base.called" >"$workdir/preexisting"

if [[ -s "$workdir/preexisting" ]]; then
  echo "Called vulnerabilities also present at $base_ref (not caused by this change;"
  echo "they fail 'make vuln' on main and block the release until fixed):"
  sed 's/^/  /' "$workdir/preexisting"
fi

if [[ -s "$workdir/introduced" ]]; then
  echo "::error::Called vulnerabilities introduced by this change: $(paste -sd, "$workdir/introduced")"
  sed 's/^/  /' "$workdir/introduced"
  echo
  # Human-readable traces for the head revision.
  GOTOOLCHAIN="$(toolchain_of .)" GOFLAGS=-buildvcs=false "$govulncheck" ./... || true
  exit 1
fi

echo "No called vulnerabilities introduced relative to $base_ref."
