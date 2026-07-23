#!/usr/bin/env bash

set -euo pipefail

dist_dir="${1:-}"
version="${2:-}"

log() { printf '[release-artifacts] %s\n' "$*"; }
fail() { printf '[release-artifacts] ERROR: %s\n' "$*" >&2; exit 1; }

[[ -n "$dist_dir" && -d "$dist_dir" ]] || fail "usage: $0 DIST_DIR VERSION"
[[ "$version" =~ ^v[A-Za-z0-9._-]+$ ]] || fail "version must be a release tag such as v0.5.0"
command -v go >/dev/null 2>&1 || fail "go is required to inspect cross-platform binaries"

dist_dir="$(cd "$dist_dir" && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

binary_specs=(
  "agent-bridge_${version}_darwin_amd64|github.com/Zleap-AI/Agent-Bridge/cmd/bridge|darwin|amd64"
  "agent-bridge_${version}_darwin_arm64|github.com/Zleap-AI/Agent-Bridge/cmd/bridge|darwin|arm64"
  "agent-bridge_${version}_linux_amd64|github.com/Zleap-AI/Agent-Bridge/cmd/bridge|linux|amd64"
  "agent-bridge_${version}_linux_arm64|github.com/Zleap-AI/Agent-Bridge/cmd/bridge|linux|arm64"
  "agent-bridge_${version}_windows_amd64.exe|github.com/Zleap-AI/Agent-Bridge/cmd/bridge|windows|amd64"
  "agent-bridge_${version}_windows_arm64.exe|github.com/Zleap-AI/Agent-Bridge/cmd/bridge|windows|arm64"
  "agent-bridge-server_${version}_linux_amd64|github.com/Zleap-AI/Agent-Bridge/cmd/server|linux|amd64"
  "agent-bridge-server_${version}_linux_arm64|github.com/Zleap-AI/Agent-Bridge/cmd/server|linux|arm64"
)

: >"${work_dir}/expected-files"
: >"${work_dir}/expected-checksums"
for spec in "${binary_specs[@]}"; do
  IFS='|' read -r name _ _ _ <<<"$spec"
  printf '%s\n' "$name" >>"${work_dir}/expected-files"
  printf '%s\n' "$name" >>"${work_dir}/expected-checksums"
done
printf '%s\n' install-local.sh install-local.ps1 install-server.sh >>"${work_dir}/expected-files"
printf '%s\n' install-local.sh install-local.ps1 install-server.sh >>"${work_dir}/expected-checksums"
printf '%s\n' SHA256SUMS >>"${work_dir}/expected-files"

: >"${work_dir}/actual-files"
while IFS= read -r path; do
  [[ "$(dirname "$path")" == "$dist_dir" ]] || fail "unexpected nested release file: $path"
  basename "$path" >>"${work_dir}/actual-files"
done < <(find "$dist_dir" -type f -print)

LC_ALL=C sort -o "${work_dir}/expected-files" "${work_dir}/expected-files"
LC_ALL=C sort -o "${work_dir}/actual-files" "${work_dir}/actual-files"
if ! diff -u "${work_dir}/expected-files" "${work_dir}/actual-files"; then
  fail "release directory does not contain the exact expected artifact set"
fi

for spec in "${binary_specs[@]}"; do
  IFS='|' read -r name package_path goos goarch <<<"$spec"
  metadata="$(go version -m "${dist_dir}/${name}" 2>&1)" || fail "${name} is not a readable Go executable"
  printf '%s\n' "$metadata" | awk -v expected="$package_path" \
    '$1 == "path" && $2 == expected { found = 1 } END { exit !found }' ||
    fail "${name} has the wrong main package"
  printf '%s\n' "$metadata" | awk -v expected="GOOS=${goos}" \
    '$1 == "build" && $2 == expected { found = 1 } END { exit !found }' ||
    fail "${name} has the wrong GOOS (expected ${goos})"
  printf '%s\n' "$metadata" | awk -v expected="GOARCH=${goarch}" \
    '$1 == "build" && $2 == expected { found = 1 } END { exit !found }' ||
    fail "${name} has the wrong GOARCH (expected ${goarch})"
done

for installer in install-local.sh install-server.sh; do
  [[ -x "${dist_dir}/${installer}" ]] || fail "${installer} is not executable before upload"
  bash -n "${dist_dir}/${installer}" || fail "${installer} has invalid shell syntax"
done
[[ -f "${dist_dir}/install-local.ps1" ]] || fail "install-local.ps1 is missing"

awk 'NF >= 2 { name = $2; sub(/^\*/, "", name); print name }' \
  "${dist_dir}/SHA256SUMS" >"${work_dir}/actual-checksums"
LC_ALL=C sort -o "${work_dir}/expected-checksums" "${work_dir}/expected-checksums"
LC_ALL=C sort -o "${work_dir}/actual-checksums" "${work_dir}/actual-checksums"
if ! diff -u "${work_dir}/expected-checksums" "${work_dir}/actual-checksums"; then
  fail "SHA256SUMS does not cover the exact published files"
fi

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$dist_dir" && sha256sum --check SHA256SUMS)
else
  (cd "$dist_dir" && shasum -a 256 --check SHA256SUMS)
fi

log "verified 8 executable targets, 3 installers, and SHA256SUMS for ${version}"
