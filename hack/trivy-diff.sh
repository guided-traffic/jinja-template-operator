#!/usr/bin/env bash
# Gate over Trivy JSON reports of the operator image.
#
#   hack/trivy-diff.sh <head.json>              fail on every finding (push to main)
#   hack/trivy-diff.sh <head.json> <base.json>  fail only on findings absent from
#                                               the base revision's image (pull requests)
#
# The pull-request form exists for the same reason as hack/govulncheck-diff.sh:
# an advisory published against the Go toolchain or a module already on main
# otherwise fails every open pull request, including each Renovate pull request
# that fixes only part of it. Both reports must come from the same scan settings
# (severity, ignore-unfixed, scanners) and the same Trivy DB.
set -euo pipefail

head_report="${1:?usage: $0 <head.json> [<base.json>]}"
base_report="${2:-}"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

# findings <report>: one sorted line per finding. Vulnerabilities are keyed by
# result type instead of target, because the OS target name contains the image
# tag, which differs between the two images.
findings() {
  jq -r '.Results[]? | .Target as $target | .Type as $type
    | ((.Vulnerabilities // [])[] | "vulnerability \($type) \(.PkgName) \(.VulnerabilityID) \(.Severity)"),
      ((.Secrets // [])[] | "secret \($target) \(.RuleID) \(.Severity)"),
      ((.Misconfigurations // [])[] | "misconfiguration \($target) \(.ID) \(.Severity)")' "$1" | sort -u
}

findings "$head_report" >"$workdir/head"
if [[ -n "$base_report" ]]; then
  findings "$base_report" >"$workdir/base"
else
  : >"$workdir/base"
fi
comm -23 "$workdir/head" "$workdir/base" >"$workdir/introduced"
comm -12 "$workdir/head" "$workdir/base" >"$workdir/preexisting"

if [[ -s "$workdir/preexisting" ]]; then
  echo "Findings also present in the base revision's image (not caused by this change;"
  echo "they fail this scan on main and block the release until fixed):"
  sed 's/^/  /' "$workdir/preexisting"
fi

if [[ -s "$workdir/introduced" ]]; then
  if [[ -n "$base_report" ]]; then
    echo "::error::Image findings introduced by this change: $(paste -sd, "$workdir/introduced")"
  else
    echo "::error::Image findings: $(paste -sd, "$workdir/introduced")"
  fi
  sed 's/^/  /' "$workdir/introduced"
  exit 1
fi

echo "No image findings${base_report:+ introduced by this change}."
