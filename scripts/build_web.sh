#!/usr/bin/env bash

set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root_dir}/web"

if [[ -f package-lock.json ]]; then
  npm ci
else
  npm install
fi
npm test
npm run build

install -d "${root_dir}/cmd/bridge/html" "${root_dir}/cmd/server/html"
install -m 0644 "${root_dir}/web/dist/local/index.html" "${root_dir}/cmd/bridge/html/index.html"
install -m 0644 "${root_dir}/web/dist/remote/index.html" "${root_dir}/cmd/server/html/index.html"

printf 'Embedded Console assets updated in cmd/bridge/html and cmd/server/html\n'
