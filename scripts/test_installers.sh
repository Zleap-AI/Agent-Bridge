#!/usr/bin/env bash

set -euo pipefail

log() { printf '[installer-test] %s\n' "$*"; }
fail() { printf '[installer-test] ERROR: %s\n' "$*" >&2; exit 1; }

assert_file_contains() {
  local file="$1" expected="$2" message="$3"
  grep -Fq -- "$expected" "$file" || fail "${message}: ${expected} not found in ${file}"
}

assert_mode() {
  local expected="$1" path="$2"
  local actual
  actual="$(stat -c '%a' "$path")"
  [[ "$actual" == "$expected" ]] || fail "mode for ${path}: expected ${expected}, got ${actual}"
}

assert_hash() {
  local expected="$1" path="$2" message="$3"
  local actual
  actual="$(sha256sum "$path" | awk '{print $1}')"
  [[ "$actual" == "$expected" ]] || fail "${message}: ${path} changed"
}

file_hash() {
  sha256sum "$1" | awk '{print $1}'
}

write_mock_commands() {
  local mock_bin="$1"
  mkdir -p "$mock_bin"

  cat >"${mock_bin}/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ -n "${MOCK_CURL_CALLS:-}" ]]; then
  printf '%s\n' "$*" >>"${MOCK_CURL_CALLS}"
fi

output=""
url=""
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    -o)
      output="$2"
      shift 2
      ;;
    http://*|https://*)
      url="$1"
      shift
      ;;
    *) shift ;;
  esac
done
[[ -n "$url" ]] || { printf 'mock curl received no URL\n' >&2; exit 2; }

if [[ -n "$output" ]]; then
  asset="${url##*/}"
  if [[ "$asset" == "SHA256SUMS" ]]; then
    version="$(basename "$(dirname "$url")")"
    : >"$output"
    found=false
    for fixture in "${MOCK_RELEASE_DIR}"/*_"${version}"_*; do
      [[ -f "$fixture" ]] || continue
      found=true
      printf '%s  %s\n' "$(sha256sum "$fixture" | awk '{print $1}')" "$(basename "$fixture")" >>"$output"
    done
    [[ "$found" == true ]] || { printf 'no fixtures for %s\n' "$version" >&2; exit 22; }
  else
    [[ -f "${MOCK_RELEASE_DIR}/${asset}" ]] || { printf 'missing fixture %s\n' "$asset" >&2; exit 22; }
    cp "${MOCK_RELEASE_DIR}/${asset}" "$output"
  fi
  exit 0
fi

case "$url" in
  https://checkip.amazonaws.com/)
    [[ "${MOCK_PUBLIC_IP_PRIMARY_FAIL:-}" != "true" ]] || exit 22
    printf '%s\n' "${MOCK_PUBLIC_IP_PRIMARY:-8.8.4.4}"
    exit 0
    ;;
  https://api.ipify.org)
    [[ "${MOCK_PUBLIC_IP_FALLBACK_FAIL:-}" != "true" ]] || exit 22
    printf '%s\n' "${MOCK_PUBLIC_IP_FALLBACK:-1.1.1.1}"
    exit 0
    ;;
  */health)
    kind=local
    binary="${MOCK_LOCAL_BINARY}"
    ;;
  */api/v1/status)
    kind=server
    binary="${MOCK_SERVER_BINARY}"
    ;;
  *)
    printf 'unexpected mock curl URL: %s\n' "$url" >&2
    exit 22
    ;;
esac

version="$(sed -n 's/^VERSION=//p' "$binary" 2>/dev/null | head -1)"
[[ -n "$version" ]] || exit 22
if [[ "${MOCK_HEALTH_KIND:-}" == "$kind" && -n "${MOCK_HEALTH_VERSION:-}" ]]; then
  version="$MOCK_HEALTH_VERSION"
fi
printf '{"status":"ok","version":"%s","initialized":false}\n' "$version"
EOF

  cat >"${mock_bin}/wget" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >>"${MOCK_WGET_CALLS:?}"
output=""
url=""
spider=false
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    -O)
      output="$2"
      shift 2
      ;;
    --spider)
      spider=true
      shift
      ;;
    http://*|https://*)
      url="$1"
      shift
      ;;
    *) shift ;;
  esac
done
[[ -n "$url" ]] || { printf 'mock wget received no URL\n' >&2; exit 2; }

if [[ "$spider" == true && "$url" == */releases/latest ]]; then
  printf '  HTTP/1.1 302 Found\n  Location: https://github.com/example/Agent-Bridge/releases/tag/v0.4.2\r\n  HTTP/1.1 200 OK\n' >&2
  exit 0
fi

if [[ -n "$output" && "$output" != "-" ]]; then
  asset="${url##*/}"
  if [[ "$asset" == "SHA256SUMS" ]]; then
    version="$(basename "$(dirname "$url")")"
    : >"$output"
    found=false
    for fixture in "${MOCK_RELEASE_DIR}"/*_"${version}"_*; do
      [[ -f "$fixture" ]] || continue
      found=true
      printf '%s  %s\n' "$(sha256sum "$fixture" | awk '{print $1}')" "$(basename "$fixture")" >>"$output"
    done
    [[ "$found" == true ]] || { printf 'no fixtures for %s\n' "$version" >&2; exit 22; }
  else
    [[ -f "${MOCK_RELEASE_DIR}/${asset}" ]] || { printf 'missing fixture %s\n' "$asset" >&2; exit 22; }
    cp "${MOCK_RELEASE_DIR}/${asset}" "$output"
  fi
  exit 0
fi

case "$url" in
  https://checkip.amazonaws.com/)
    [[ "${MOCK_PUBLIC_IP_PRIMARY_FAIL:-}" != "true" ]] || exit 22
    printf '%s\n' "${MOCK_PUBLIC_IP_PRIMARY:-8.8.4.4}"
    exit 0
    ;;
  https://api.ipify.org)
    [[ "${MOCK_PUBLIC_IP_FALLBACK_FAIL:-}" != "true" ]] || exit 22
    printf '%s\n' "${MOCK_PUBLIC_IP_FALLBACK:-1.1.1.1}"
    exit 0
    ;;
  */health)
    kind=local
    binary="${MOCK_LOCAL_BINARY}"
    ;;
  */api/v1/status)
    kind=server
    binary="${MOCK_SERVER_BINARY}"
    ;;
  *)
    printf 'unexpected mock wget URL: %s\n' "$url" >&2
    exit 22
    ;;
esac

version="$(sed -n 's/^VERSION=//p' "$binary" 2>/dev/null | head -1)"
[[ -n "$version" ]] || exit 22
printf '{"status":"ok","version":"%s","initialized":false}\n' "$version"
EOF

  cat >"${mock_bin}/systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

state="${MOCK_SYSTEMCTL_STATE:?}"
mkdir -p "$state"
printf '%s\n' "$*" >>"${state}/calls.log"

if [[ "${1:-}" == "--user" ]]; then
  shift
fi
command="${1:-}"
[[ "$#" -gt 0 ]] && shift
service=""
for argument in "$@"; do
  [[ "$argument" == --* ]] || service="$argument"
done

case "$command" in
  is-active) [[ -f "${state}/active-${service}" ]] ;;
  is-enabled) [[ -f "${state}/enabled-${service}" ]] ;;
  start)
    touch "${state}/active-${service}"
    ;;
  restart)
    touch "${state}/active-${service}"
    ;;
  stop)
    rm -f "${state}/active-${service}"
    ;;
  enable)
    touch "${state}/enabled-${service}"
    ;;
  disable)
    rm -f "${state}/enabled-${service}"
    ;;
  daemon-reload) ;;
  *) printf 'unsupported mock systemctl command: %s\n' "$command" >&2; exit 2 ;;
esac
EOF

  cat >"${mock_bin}/journalctl" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

  cat >"${mock_bin}/loginctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

state="${MOCK_LOGINCTL_STATE:-${MOCK_SYSTEMCTL_STATE:?}}"
mkdir -p "$state"
printf '%s\n' "$*" >>"${state}/loginctl-calls.log"

case "${1:-}" in
  show-user)
    user="${2:?}"
    if [[ -f "${state}/linger-${user}" ]]; then
      printf 'yes\n'
    else
      printf 'no\n'
    fi
    ;;
  --no-ask-password)
    [[ "${2:-}" == "enable-linger" ]] || exit 2
    user="${3:?}"
    [[ "${MOCK_LOGINCTL_ENABLE_FAIL:-}" != "true" ]] || exit 1
    touch "${state}/linger-${user}"
    ;;
  *)
    printf 'unsupported mock loginctl command: %s\n' "$*" >&2
    exit 2
    ;;
esac
EOF

  cat >"${mock_bin}/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

  cat >"${mock_bin}/ufw" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >>"${MOCK_UFW_CALLS:?}"
case "${1:-}" in
  status)
    if [[ "${MOCK_UFW_ACTIVE:-}" == "true" ]]; then
      printf 'Status: active\n'
    else
      printf 'Status: inactive\n'
    fi
    ;;
  allow) [[ "${MOCK_UFW_ALLOW_FAIL:-}" != "true" ]] ;;
  *) exit 2 ;;
esac
EOF

  cat >"${mock_bin}/firewall-cmd" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >>"${MOCK_FIREWALLD_CALLS:?}"
case "${1:-}" in
  --state) [[ "${MOCK_FIREWALLD_ACTIVE:-}" == "true" ]] ;;
  --permanent) [[ "${MOCK_FIREWALLD_CONFIGURE_FAIL:-}" != "true" ]] ;;
  --reload) [[ "${MOCK_FIREWALLD_RELOAD_FAIL:-}" != "true" ]] ;;
  *) exit 2 ;;
esac
EOF

  chmod 0755 "${mock_bin}/curl" "${mock_bin}/wget" "${mock_bin}/systemctl" \
    "${mock_bin}/journalctl" "${mock_bin}/loginctl" "${mock_bin}/sleep" \
    "${mock_bin}/ufw" "${mock_bin}/firewall-cmd"
}

write_release_fixtures() {
  local fixture_dir="$1" arch="$2"
  local version kind asset
  mkdir -p "$fixture_dir"
  for version in 0.4.0 0.4.1 0.4.2; do
    for kind in local server; do
      if [[ "$kind" == local ]]; then
        asset="agent-bridge_v${version}_linux_${arch}"
      else
        asset="agent-bridge-server_v${version}_linux_${arch}"
      fi
      cat >"${fixture_dir}/${asset}" <<EOF
#!/usr/bin/env bash
VERSION=${version}
if [[ "\${1:-}" == "setup-url" ]]; then
  printf 'Setup URL: http://127.0.0.1/setup?version=%s\\n' "\$VERSION"
elif [[ "\${1:-}" == "--version" ]]; then
  printf '%s\\n' "\$VERSION"
fi
EOF
      chmod 0755 "${fixture_dir}/${asset}"
    done
  done
}

run_local_tests() {
  local root="$1"
  local home="${root}/local-home"
  local state="${root}/local-systemctl"
  local binary="${home}/.local/bin/agent-bridge"
  local unit="${home}/.config/systemd/user/agent-bridge.service"
  local installer="/repo/scripts/install-local.sh"
  local restart_count binary_before unit_before status current_user linger_output

  mkdir -p "$home" "$state"
  export HOME="$home"
  export MOCK_SYSTEMCTL_STATE="$state"
  export MOCK_LOCAL_BINARY="$binary"
  export MOCK_SERVER_BINARY="${root}/unused-server"

  log "${TEST_IMAGE}: Local first install"
  AGENT_BRIDGE_VERSION=v0.4.0 SSH_CONNECTION=test bash "$installer"
  assert_file_contains "$binary" 'VERSION=0.4.0' 'Local first install did not install requested version'
  assert_file_contains "$unit" 'ExecStart=' 'Local systemd unit was not installed'
  assert_file_contains "${state}/calls.log" '--user enable agent-bridge.service' 'Local service was not enabled'
  assert_file_contains "${state}/calls.log" '--user restart agent-bridge.service' 'Local service was not started with restart'
  [[ -f "${state}/active-agent-bridge.service" ]] || fail 'Local service is not active after first install'
  current_user="$(id -un)"
  [[ -f "${state}/linger-${current_user}" ]] || fail 'Local install did not enable systemd linger'
  assert_file_contains "${state}/loginctl-calls.log" "--no-ask-password enable-linger ${current_user}" \
    'Local install did not request linger non-interactively'

  log "${TEST_IMAGE}: Local successful upgrade restarts service"
  AGENT_BRIDGE_VERSION=v0.4.1 SSH_CONNECTION=test bash "$installer"
  assert_file_contains "$binary" 'VERSION=0.4.1' 'Local upgrade did not install requested version'
  restart_count="$(grep -Fc -- '--user restart agent-bridge.service' "${state}/calls.log")"
  [[ "$restart_count" -eq 2 ]] || fail "Local install and upgrade should each restart; got ${restart_count} restarts"

  log "${TEST_IMAGE}: Local warns clearly when systemd linger cannot be enabled"
  rm -f "${state}/linger-${current_user}"
  linger_output="$(MOCK_LOGINCTL_ENABLE_FAIL=true AGENT_BRIDGE_VERSION=v0.4.1 \
    SSH_CONNECTION=test bash "$installer" 2>&1)"
  grep -Fq 'WARNING: systemd linger could not be enabled without interaction' <<<"$linger_output" ||
    fail 'Local install did not warn when systemd linger could not be enabled'
  grep -Fq "sudo loginctl enable-linger ${current_user}" <<<"$linger_output" ||
    fail 'Local linger warning did not include the recovery command'
  [[ -f "${state}/active-agent-bridge.service" ]] || fail 'Local service stopped when linger could not be enabled'

  printf '\n# preserved-on-rollback\n' >>"$unit"
  binary_before="$(file_hash "$binary")"
  unit_before="$(file_hash "$unit")"

  log "${TEST_IMAGE}: Local wrong health version rolls back binary and unit"
  set +e
  MOCK_HEALTH_KIND=local MOCK_HEALTH_VERSION=0.0.0 \
    AGENT_BRIDGE_VERSION=v0.4.2 SSH_CONNECTION=test bash "$installer" >/dev/null 2>&1
  status=$?
  set -e
  [[ "$status" -ne 0 ]] || fail 'Local upgrade unexpectedly accepted the wrong health version'
  assert_hash "$binary_before" "$binary" 'Local rollback binary'
  assert_hash "$unit_before" "$unit" 'Local rollback unit'
  [[ -f "${state}/active-agent-bridge.service" ]] || fail 'Local rollback did not restart the previous service'
  [[ -f "${state}/enabled-agent-bridge.service" ]] || fail 'Local rollback did not preserve enabled state'
}

run_macos_plist_test() {
  local root="$1" arch="$2"
  local home="${root}/mac-home"
  local install_dir="${home}/Agent Bridge & <local> \"quoted\" 'single"
  local binary="${install_dir}/agent-bridge"
  local plist="${home}/Library/LaunchAgents/ai.agentbridge.local.plist"
  local state="${root}/mac-launchctl"
  local mock_bin="${root}/mac-mock-bin"
  local installer="/repo/scripts/install-local.sh"
  local expected

  mkdir -p "$home" "$state" "$mock_bin"
  cp "${MOCK_RELEASE_DIR}/agent-bridge_v0.4.0_linux_${arch}" \
    "${MOCK_RELEASE_DIR}/agent-bridge_v0.4.0_darwin_${arch}"

  cat >"${mock_bin}/uname" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  -s) printf 'Darwin\n' ;;
  -m) printf '%s\n' "${MOCK_MAC_MACHINE:-amd64}" ;;
  *) printf 'Darwin\n' ;;
esac
EOF
  cat >"${mock_bin}/launchctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
state="${MOCK_LAUNCHCTL_STATE:?}"
mkdir -p "$state"
printf '%s\n' "$*" >>"${state}/calls.log"
case "${1:-}" in
  print) [[ -f "${state}/active" ]] ;;
  bootout) rm -f "${state}/active" ;;
  bootstrap) touch "${state}/active" ;;
  *) exit 2 ;;
esac
EOF
  chmod 0755 "${mock_bin}/uname" "${mock_bin}/launchctl"

  log "${TEST_IMAGE}: macOS plist escapes a special-character install path"
  PATH="${mock_bin}:${PATH}" HOME="$home" MOCK_MAC_MACHINE="$arch" \
    MOCK_LAUNCHCTL_STATE="$state" MOCK_LOCAL_BINARY="$binary" \
    AGENT_BRIDGE_INSTALL_DIR="$install_dir" AGENT_BRIDGE_VERSION=v0.4.0 \
    SSH_CONNECTION=test bash "$installer"

  assert_file_contains "$binary" 'VERSION=0.4.0' 'macOS install did not install the Local binary'
  expected="<array><string>${home}/Agent Bridge &amp; &lt;local&gt; &quot;quoted&quot; &apos;single/agent-bridge</string><string>--background</string></array>"
  assert_file_contains "$plist" "$expected" 'macOS plist did not XML-escape the install path'
  if grep -Fq "\\'apos;" "$plist"; then
    fail 'macOS plist retained the broken single-quote escape'
  fi
  [[ -f "${state}/active" ]] || fail 'macOS LaunchAgent was not bootstrapped'
}

run_server_tests() {
  local root="$1"
  local state="${root}/server-systemctl"
  local binary="/usr/local/bin/agent-bridge-server-installer-test"
  local config="/etc/agent-bridge-installer-test"
  local env_file="${config}/server.env"
  local data="/var/lib/agent-bridge-installer-test"
  local database="${data}/agent-bridge.db"
  local unit="/etc/systemd/system/agent-bridge-server.service"
  local installer="/repo/scripts/install-server.sh"
  local common status backup_count install_output
  local binary_before env_before unit_before db_before wal_before shm_before

  mkdir -p "$state" /etc/systemd/system
  export MOCK_SYSTEMCTL_STATE="$state"
  export MOCK_SERVER_BINARY="$binary"
  export MOCK_LOCAL_BINARY="${root}/unused-local"
  common=(
    "AGENT_BRIDGE_REPOSITORY=example/Agent-Bridge"
    "AGENT_BRIDGE_CONFIG_DIR=${config}"
    "AGENT_BRIDGE_DATA_DIR=${data}"
    "AGENT_BRIDGE_DATABASE_PATH=${database}"
    "AGENT_BRIDGE_BINARY_PATH=${binary}"
  )

  if id agent-bridge >/dev/null 2>&1; then
    fail 'Server installer test requires no pre-existing agent-bridge user'
  fi
  getent group agent-bridge >/dev/null 2>&1 || groupadd --system agent-bridge

  log "${TEST_IMAGE}: Server recovers from an existing group, then installs with private permissions and UMask"
  env "${common[@]}" MOCK_UFW_ACTIVE=true AGENT_BRIDGE_LISTEN_ADDR=127.0.0.1:19301 \
    AGENT_BRIDGE_VERSION=v0.4.0 bash "$installer"
  [[ "$(id -gn agent-bridge)" == "agent-bridge" ]] || fail 'Server user did not reuse the existing service group'
  assert_file_contains "$binary" 'VERSION=0.4.0' 'Server first install did not install requested version'
  assert_file_contains "$env_file" 'AGENT_BRIDGE_LISTEN_ADDR=127.0.0.1:19301' 'Server listen address missing'
  assert_file_contains "$env_file" 'AGENT_BRIDGE_PUBLIC_URL=http://8.8.4.4:19301' \
    'Server did not persist the automatically detected public IP'
  assert_file_contains "$unit" 'UMask=0077' 'Server unit does not protect service-created files'
  assert_mode 700 "$data"
  assert_mode 700 "$config"
  assert_mode 600 "$env_file"
  assert_mode 644 "$unit"
  [[ -f "${state}/active-agent-bridge-server" ]] || fail 'Server service is not active after first install'
  [[ -f "${state}/enabled-agent-bridge-server" ]] || fail 'Server service is not enabled after first install'
  if grep -Fq 'allow 19301/tcp' "$MOCK_UFW_CALLS"; then
    fail 'Server installer opened a firewall port for a loopback listener'
  fi

  log "${TEST_IMAGE}: Server upgrade preserves existing custom listen address"
  env "${common[@]}" AGENT_BRIDGE_VERSION=v0.4.1 bash "$installer"
  assert_file_contains "$binary" 'VERSION=0.4.1' 'Server upgrade did not install requested version'
  assert_file_contains "$env_file" 'AGENT_BRIDGE_LISTEN_ADDR=127.0.0.1:19301' 'Server upgrade replaced custom listen address'
  [[ "$(grep -c '^AGENT_BRIDGE_LISTEN_ADDR=' "$env_file")" -eq 1 ]] || fail 'Server env contains duplicate listen addresses'
  assert_file_contains "$env_file" 'AGENT_BRIDGE_PUBLIC_URL=http://8.8.4.4:19301' \
    'Server upgrade did not preserve the detected public URL'

  log "${TEST_IMAGE}: Server falls back when the first public IP service fails"
  sed -i '/^AGENT_BRIDGE_PUBLIC_URL=/d' "$env_file"
  env "${common[@]}" MOCK_PUBLIC_IP_PRIMARY_FAIL=true MOCK_PUBLIC_IP_FALLBACK=9.9.9.9 \
    AGENT_BRIDGE_VERSION=v0.4.1 bash "$installer"
  assert_file_contains "$env_file" 'AGENT_BRIDGE_PUBLIC_URL=http://9.9.9.9:19301' \
    'Server did not use the fallback public IP service'

  log "${TEST_IMAGE}: Server rejects invalid public IP responses and keeps a manual fallback"
  sed -i '/^AGENT_BRIDGE_PUBLIC_URL=/d' "$env_file"
  install_output="$(env "${common[@]}" MOCK_PUBLIC_IP_PRIMARY=10.0.0.8 \
    MOCK_PUBLIC_IP_FALLBACK='not-an-ip' AGENT_BRIDGE_VERSION=v0.4.1 \
    bash "$installer" 2>&1)"
  if grep -q '^AGENT_BRIDGE_PUBLIC_URL=' "$env_file"; then
    fail 'Server persisted an invalid or private public IP response'
  fi
  grep -Fq 'Replace SERVER_PUBLIC_IP' <<<"$install_output" ||
    fail 'Server did not explain the manual public IP fallback'

  log "${TEST_IMAGE}: Explicit Server public URL bypasses automatic detection"
  : >"${MOCK_CURL_CALLS}"
  env "${common[@]}" AGENT_BRIDGE_PUBLIC_URL=https://bridge.example.com \
    AGENT_BRIDGE_VERSION=v0.4.1 bash "$installer"
  assert_file_contains "$env_file" 'AGENT_BRIDGE_PUBLIC_URL=https://bridge.example.com' \
    'Server did not prefer the explicitly configured public URL'
  if grep -Eq 'checkip\.amazonaws\.com|api\.ipify\.org' "$MOCK_CURL_CALLS"; then
    fail 'Server queried public IP services despite an explicit public URL'
  fi

  log "${TEST_IMAGE}: Server opens a public listener through active UFW"
  : >"$MOCK_UFW_CALLS"
  env "${common[@]}" MOCK_UFW_ACTIVE=true AGENT_BRIDGE_LISTEN_ADDR=0.0.0.0:19301 \
    AGENT_BRIDGE_VERSION=v0.4.1 bash "$installer"
  assert_file_contains "$MOCK_UFW_CALLS" 'allow 19301/tcp' \
    'Server installer did not allow the public port through UFW'

  log "${TEST_IMAGE}: Server opens a public listener through active firewalld"
  : >"$MOCK_UFW_CALLS"
  : >"$MOCK_FIREWALLD_CALLS"
  env "${common[@]}" MOCK_FIREWALLD_ACTIVE=true AGENT_BRIDGE_LISTEN_ADDR=0.0.0.0:19301 \
    AGENT_BRIDGE_VERSION=v0.4.1 bash "$installer"
  assert_file_contains "$MOCK_FIREWALLD_CALLS" '--permanent --add-port=19301/tcp' \
    'Server installer did not persist the public port through firewalld'
  assert_file_contains "$MOCK_FIREWALLD_CALLS" '--reload' \
    'Server installer did not reload firewalld'

  printf 'sqlite-before-failed-upgrade\n' >"$database"
  printf 'wal-before-failed-upgrade\n' >"${database}-wal"
  printf 'shm-before-failed-upgrade\n' >"${database}-shm"
  chown agent-bridge:agent-bridge "$database" "${database}-wal" "${database}-shm"
  chmod 0600 "$database" "${database}-wal" "${database}-shm"
  printf 'AGENT_BRIDGE_TEST_SENTINEL=preserve\n' >>"$env_file"
  printf '\n# preserved-on-rollback\n' >>"$unit"

  binary_before="$(file_hash "$binary")"
  env_before="$(file_hash "$env_file")"
  unit_before="$(file_hash "$unit")"
  db_before="$(file_hash "$database")"
  wal_before="$(file_hash "${database}-wal")"
  shm_before="$(file_hash "${database}-shm")"

  log "${TEST_IMAGE}: Server wrong health version restores binary, env, unit, and SQLite"
  set +e
  env "${common[@]}" MOCK_HEALTH_KIND=server MOCK_HEALTH_VERSION=0.0.0 \
    AGENT_BRIDGE_VERSION=v0.4.2 bash "$installer" >/dev/null 2>&1
  status=$?
  set -e
  [[ "$status" -ne 0 ]] || fail 'Server upgrade unexpectedly accepted the wrong health version'
  assert_hash "$binary_before" "$binary" 'Server rollback binary'
  assert_hash "$env_before" "$env_file" 'Server rollback env'
  assert_hash "$unit_before" "$unit" 'Server rollback unit'
  assert_hash "$db_before" "$database" 'Server rollback database'
  assert_hash "$wal_before" "${database}-wal" 'Server rollback WAL'
  assert_hash "$shm_before" "${database}-shm" 'Server rollback SHM'
  assert_mode 600 "$database"
  assert_mode 600 "${database}-wal"
  assert_mode 600 "${database}-shm"
  [[ -f "${state}/active-agent-bridge-server" ]] || fail 'Server rollback did not restart the previous service'
  [[ -f "${state}/enabled-agent-bridge-server" ]] || fail 'Server rollback did not preserve enabled state'
  backup_count="$(find "$data" -maxdepth 1 -type f -name 'agent-bridge.db.backup.*' | wc -l | tr -d ' ')"
  [[ "$backup_count" -ge 1 ]] || fail 'Server upgrade did not leave a SQLite backup'
}

run_wget_latest_tests() {
  local root="$1"
  local home="${root}/local-home"
  local local_binary="${home}/.local/bin/agent-bridge"
  local server_binary="/usr/local/bin/agent-bridge-server-installer-wget-test"
  local config="/etc/agent-bridge-installer-wget-test"
  local env_file="${config}/server.env"
  local data="/var/lib/agent-bridge-installer-wget-test"
  local database="${data}/agent-bridge.db"
  local wget_calls="${root}/wget-calls.log"
  local common

  : >"$wget_calls"
  export MOCK_WGET_CALLS="$wget_calls"

  # Exporting this narrow wrapper makes command -v curl report unavailable in
  # child Bash processes without modifying the container's system binaries.
  # ShellCheck cannot see the invocation in the exported child environment.
  # shellcheck disable=SC2329
  command() {
    if [[ "${1:-}" == "-v" && "${2:-}" == "curl" ]]; then
      return 1
    fi
    builtin command "$@"
  }
  export -f command

  log "${TEST_IMAGE}: Local wget-only latest release install"
  HOME="$home" MOCK_SYSTEMCTL_STATE="${root}/local-systemctl" \
    MOCK_LOCAL_BINARY="$local_binary" MOCK_SERVER_BINARY="${root}/unused-server" \
    AGENT_BRIDGE_REPOSITORY=example/Agent-Bridge SSH_CONNECTION=test \
    /bin/bash /repo/scripts/install-local.sh
  assert_file_contains "$local_binary" 'VERSION=0.4.2' 'Local wget-only latest did not resolve v0.4.2'

  common=(
    "AGENT_BRIDGE_REPOSITORY=example/Agent-Bridge"
    "AGENT_BRIDGE_CONFIG_DIR=${config}"
    "AGENT_BRIDGE_DATA_DIR=${data}"
    "AGENT_BRIDGE_DATABASE_PATH=${database}"
    "AGENT_BRIDGE_BINARY_PATH=${server_binary}"
  )
  log "${TEST_IMAGE}: Server wget-only latest release install"
  MOCK_SYSTEMCTL_STATE="${root}/server-systemctl" MOCK_SERVER_BINARY="$server_binary" \
    MOCK_LOCAL_BINARY="${root}/unused-local" \
    env "${common[@]}" /bin/bash /repo/scripts/install-server.sh
  assert_file_contains "$server_binary" 'VERSION=0.4.2' 'Server wget-only latest did not resolve v0.4.2'
  assert_file_contains "$env_file" 'AGENT_BRIDGE_PUBLIC_URL=http://8.8.4.4:9201' \
    'Server wget-only install did not persist the detected public IP'
  assert_file_contains "$wget_calls" '--server-response --spider https://github.com/example/Agent-Bridge/releases/latest' \
    'wget-only latest resolution was not exercised'
  assert_file_contains "$wget_calls" 'https://checkip.amazonaws.com/' \
    'wget-only public IP detection was not exercised'

  unset -f command
}

run_inside_container() {
  local root arch fixture_dir mock_bin
  root="$(mktemp -d)"
  export INSTALLER_TEST_ROOT="$root"
  trap 'rm -rf "${INSTALLER_TEST_ROOT}"' EXIT
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) fail "unsupported test architecture: ${arch}" ;;
  esac
  fixture_dir="${root}/releases"
  mock_bin="${root}/mock-bin"
  write_release_fixtures "$fixture_dir" "$arch"
  write_mock_commands "$mock_bin"
  export PATH="${mock_bin}:${PATH}"
  export MOCK_RELEASE_DIR="$fixture_dir"
  export MOCK_CURL_CALLS="${root}/curl-calls.log"
  export MOCK_WGET_CALLS="${root}/wget-calls.log"
  export MOCK_UFW_CALLS="${root}/ufw-calls.log"
  export MOCK_FIREWALLD_CALLS="${root}/firewalld-calls.log"
  : >"$MOCK_UFW_CALLS"
  : >"$MOCK_FIREWALLD_CALLS"

  run_local_tests "$root"
  run_server_tests "$root"
  run_wget_latest_tests "$root"
  run_macos_plist_test "$root" "$arch"
  log "${TEST_IMAGE}: all installer integration tests passed"
}

run_in_docker() {
  local repo_root images image
  command -v docker >/dev/null 2>&1 || fail 'Docker is required'
  docker info >/dev/null 2>&1 || fail 'Docker daemon is not available'
  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  images="${INSTALLER_TEST_IMAGES:-ubuntu:24.04 rockylinux:9}"
  for image in $images; do
    if ! docker image inspect "$image" >/dev/null 2>&1; then
      log "pulling ${image}"
      docker pull "$image" >/dev/null
    fi
    log "running isolated tests in ${image}"
    docker run --rm --network none \
      -e "TEST_IMAGE=${image}" \
      -v "${repo_root}:/repo:ro" \
      "$image" bash /repo/scripts/test_installers.sh --inside
  done
  log 'all container installer tests passed'
}

if [[ "${1:-}" == "--inside" ]]; then
  run_inside_container
else
  run_in_docker
fi
