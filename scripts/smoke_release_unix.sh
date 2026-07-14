#!/usr/bin/env bash

set -euo pipefail

dist_dir="${1:-}"
version="${2:-}"
expected_os="${3:-}"
expected_arch="${4:-}"
binary_version="${version#v}"

log() { printf '[release-smoke] %s\n' "$*"; }
fail() { printf '[release-smoke] ERROR: %s\n' "$*" >&2; exit 1; }

[[ -n "$dist_dir" && -d "$dist_dir" && -n "$expected_os" && -n "$expected_arch" ]] ||
  fail "usage: $0 DIST_DIR VERSION GOOS GOARCH"
[[ "$version" =~ ^v[A-Za-z0-9._-]+$ ]] || fail "invalid release version: ${version}"
[[ "$expected_os" == darwin || "$expected_os" == linux ]] || fail "unsupported GOOS: ${expected_os}"
[[ "$expected_arch" == amd64 || "$expected_arch" == arm64 ]] || fail "unsupported GOARCH: ${expected_arch}"
command -v curl >/dev/null 2>&1 || fail "curl is required"

case "$(uname -s)" in
  Darwin) actual_os=darwin ;;
  Linux) actual_os=linux ;;
  *) fail "unsupported runner OS: $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) actual_arch=amd64 ;;
  arm64|aarch64) actual_arch=arm64 ;;
  *) fail "unsupported runner architecture: $(uname -m)" ;;
esac
[[ "$actual_os" == "$expected_os" ]] || fail "runner OS is ${actual_os}, expected ${expected_os}"
[[ "$actual_arch" == "$expected_arch" ]] || fail "runner architecture is ${actual_arch}, expected ${expected_arch}"

dist_dir="$(cd "$dist_dir" && pwd)"
temp_dir="$(mktemp -d)"
local_pid=""
server_pid=""

stop_process() {
  local pid="$1"
  [[ -n "$pid" ]] || return 0
  if kill -0 "$pid" >/dev/null 2>&1; then
    kill -TERM "$pid" >/dev/null 2>&1 || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      kill -0 "$pid" >/dev/null 2>&1 || break
      sleep 0.2
    done
  fi
  if kill -0 "$pid" >/dev/null 2>&1; then
    kill -KILL "$pid" >/dev/null 2>&1 || true
  fi
  wait "$pid" >/dev/null 2>&1 || true
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  set +e
  stop_process "$local_pid"
  stop_process "$server_pid"
  if [[ "$status" -ne 0 ]]; then
    for log_file in "${temp_dir}"/*.log; do
      [[ -f "$log_file" ]] || continue
      printf '\n--- %s ---\n' "$(basename "$log_file")" >&2
      cat "$log_file" >&2
    done
  fi
  rm -rf "$temp_dir"
  exit "$status"
}
trap cleanup EXIT INT TERM

assert_version() {
  local name="$1" actual="$2"
  [[ "$actual" == "$binary_version" ]] ||
    fail "${name} reported version '${actual}', expected '${binary_version}'"
}

wait_for_health() {
  local name="$1" pid="$2" url="$3" log_file="$4"
  local response compact attempt
  for ((attempt = 0; attempt < 60; attempt++)); do
    if ! kill -0 "$pid" >/dev/null 2>&1; then
      fail "${name} exited before becoming healthy; see ${log_file}"
    fi
    if response="$(curl -fsS --connect-timeout 1 --max-time 2 "$url" 2>/dev/null)"; then
      compact="$(printf '%s' "$response" | tr -d '[:space:]')"
      if [[ "$compact" == *'"status":"ok"'* && "$compact" == *"\"version\":\"${binary_version}\""* ]]; then
        return 0
      fi
      fail "${name} returned unexpected health payload: ${response}"
    fi
    sleep 0.5
  done
  fail "${name} did not become healthy at ${url}"
}

assert_openapi_version() {
  local url="$1" response compact
  response="$(curl -fsS --connect-timeout 1 --max-time 2 "$url")" ||
    fail "could not read OpenAPI document at ${url}"
  compact="$(printf '%s' "$response" | tr -d '[:space:]')"
  [[ "$compact" == *"\"version\":\"${binary_version}\""* ]] ||
    fail "OpenAPI document did not report version ${binary_version}"
}

export HOME="${temp_dir}/home"
export XDG_CONFIG_HOME="${HOME}/.config"
mkdir -p "$HOME" "$XDG_CONFIG_HOME"

local_binary="${dist_dir}/agent-bridge_${version}_${expected_os}_${expected_arch}"
[[ -f "$local_binary" ]] || fail "missing Local artifact: ${local_binary}"
chmod +x "$local_binary"
assert_version "Agent-Bridge Local" "$("$local_binary" --version)"

local_port="${AGENT_BRIDGE_SMOKE_LOCAL_PORT:-39202}"
"$local_binary" --background --listen 127.0.0.1 --port "$local_port" \
  >"${temp_dir}/local.log" 2>&1 &
local_pid=$!
wait_for_health "Agent-Bridge Local" "$local_pid" \
  "http://127.0.0.1:${local_port}/health" "${temp_dir}/local.log"
log "Local ${expected_os}/${expected_arch} reports ${binary_version} and starts successfully"
stop_process "$local_pid"
local_pid=""

if [[ "$expected_os" == linux ]]; then
  server_binary="${dist_dir}/agent-bridge-server_${version}_linux_${expected_arch}"
  [[ -f "$server_binary" ]] || fail "missing Server artifact: ${server_binary}"
  chmod +x "$server_binary"
  assert_version "Agent-Bridge Server" "$("$server_binary" version)"

  server_port="${AGENT_BRIDGE_SMOKE_SERVER_PORT:-39201}"
  mkdir -p "${temp_dir}/server"
  "$server_binary" serve --listen "127.0.0.1:${server_port}" \
    --data-dir "${temp_dir}/server" --database "${temp_dir}/server/agent-bridge.db" \
    >"${temp_dir}/server.log" 2>&1 &
  server_pid=$!
  wait_for_health "Agent-Bridge Server" "$server_pid" \
    "http://127.0.0.1:${server_port}/api/v1/status" "${temp_dir}/server.log"
  assert_openapi_version "http://127.0.0.1:${server_port}/openapi.json"
  log "Server linux/${expected_arch} reports ${binary_version} and starts successfully"
  stop_process "$server_pid"
  server_pid=""
fi
