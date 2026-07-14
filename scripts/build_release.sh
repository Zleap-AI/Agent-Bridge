#!/usr/bin/env bash

set -euo pipefail

version="${1:-v0.4.0}"
binary_version="${version#v}"
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist_dir="${root_dir}/dist"

rm -rf "${dist_dir}"
mkdir -p "${dist_dir}"
cd "${root_dir}"
artifacts=()

if [[ -f "${root_dir}/web/package.json" ]]; then
  "${root_dir}/scripts/build_web.sh"
fi

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
  binary_name="agent-bridge_${version}_${goos}_${goarch}"

  if [[ "${goos}" == "windows" ]]; then
    binary_name="${binary_name}.exe"
  fi

  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
    go build -trimpath -ldflags="-s -w -X main.version=${binary_version}" \
    -o "${dist_dir}/${binary_name}" ./cmd/bridge

  artifacts+=("${binary_name}")
done

server_targets=(
  "linux amd64"
  "linux arm64"
)

for target in "${server_targets[@]}"; do
  read -r goos goarch <<<"${target}"
  binary_name="agent-bridge-server_${version}_${goos}_${goarch}"

  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
    go build -trimpath -ldflags="-s -w -X main.version=${binary_version}" \
    -o "${dist_dir}/${binary_name}" ./cmd/server

  artifacts+=("${binary_name}")
done

install -m 0755 "${root_dir}/scripts/install-local.sh" "${dist_dir}/install-local.sh"
install -m 0755 "${root_dir}/scripts/install-server.sh" "${dist_dir}/install-server.sh"
artifacts+=("install-local.sh" "install-server.sh")

if command -v sha256sum >/dev/null 2>&1; then
  (cd "${dist_dir}" && sha256sum "${artifacts[@]}" > SHA256SUMS)
else
  (cd "${dist_dir}" && shasum -a 256 "${artifacts[@]}" > SHA256SUMS)
fi
bash "${root_dir}/scripts/verify_release_artifacts.sh" "${dist_dir}" "${version}"
printf 'Release binaries written to %s\n' "${dist_dir}"
