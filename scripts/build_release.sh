#!/usr/bin/env bash

set -euo pipefail

version="${1:-v0.3.0}"
binary_version="${version#v}"
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist_dir="${root_dir}/dist"
staging_dir="$(mktemp -d)"

trap 'rm -rf "${staging_dir}"' EXIT
rm -rf "${dist_dir}"
mkdir -p "${dist_dir}"
cd "${root_dir}"

targets=(
  "darwin amd64"
  "darwin arm64"
  "linux amd64"
  "linux arm64"
  "windows amd64"
  "windows arm64"
)

for target in "${targets[@]}"; do
  read -r goos goarch <<<"${target}"
  package_name="agent-bridge_${version}_${goos}_${goarch}"
  package_dir="${staging_dir}/${package_name}"
  binary_name="agent-bridge"
  archive_path="${dist_dir}/${package_name}.tar.gz"

  if [[ "${goos}" == "windows" ]]; then
    binary_name="agent-bridge.exe"
    archive_path="${dist_dir}/${package_name}.zip"
  fi

  mkdir -p "${package_dir}"
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
    go build -trimpath -ldflags="-s -w -X main.version=${binary_version}" \
    -o "${package_dir}/${binary_name}" ./cmd/bridge

  cp "${root_dir}/README.md" "${root_dir}/LICENSE" "${package_dir}/"

  if [[ "${goos}" == "windows" ]]; then
    (cd "${staging_dir}" && zip -qr "${archive_path}" "${package_name}")
  else
    tar -C "${staging_dir}" -czf "${archive_path}" "${package_name}"
  fi
done

if command -v sha256sum >/dev/null 2>&1; then
  (cd "${dist_dir}" && sha256sum agent-bridge_* > SHA256SUMS)
else
  (cd "${dist_dir}" && shasum -a 256 agent-bridge_* > SHA256SUMS)
fi
printf 'Release packages written to %s\n' "${dist_dir}"
