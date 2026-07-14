#!/usr/bin/env bash

set -euo pipefail

repo="${AGENT_BRIDGE_REPOSITORY:-Zleap-AI/Agent-Bridge}"
version="${AGENT_BRIDGE_VERSION:-latest}"
install_dir="${AGENT_BRIDGE_INSTALL_DIR:-${HOME}/.local/bin}"
binary_path="${install_dir}/agent-bridge"
local_url="${AGENT_BRIDGE_LOCAL_URL:-http://localhost:9202}"
local_url_explicit=false
[[ -n "${AGENT_BRIDGE_LOCAL_URL+x}" ]] && local_url_explicit=true
service_label="ai.agentbridge.local"
service_name="agent-bridge.service"

log() { printf '[Agent-Bridge] %s\n' "$*"; }
warn() { printf '[Agent-Bridge] WARNING: %s\n' "$*" >&2; }
fail() { printf '[Agent-Bridge] ERROR: %s\n' "$*" >&2; exit 1; }

download() {
  local url="$1" output="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --connect-timeout 15 "$url" -o "$output"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --tries=3 --timeout=15 -O "$output" "$url"
  else
    fail "curl or wget is required"
  fi
}

fetch() {
  local url="$1"
  if command -v curl >/dev/null 2>&1; then
    curl -fsS --connect-timeout 2 --max-time 4 "$url"
  else
    wget -q --timeout=4 -O - "$url"
  fi
}

latest_version() {
  local latest_url="https://github.com/${repo}/releases/latest"
  local resolved_url headers tag
  if command -v curl >/dev/null 2>&1; then
    resolved_url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$latest_url")" ||
      fail "could not resolve the latest release"
    resolved_url="${resolved_url%%\?*}"
    resolved_url="${resolved_url%/}"
    tag="${resolved_url##*/}"
  elif command -v wget >/dev/null 2>&1; then
    headers="$(wget -q --server-response --spider "$latest_url" 2>&1)" ||
      fail "could not resolve the latest release"
    tag="$(printf '%s\n' "$headers" | awk '
      tolower($1) == "location:" {
        location = $2
        sub(/\r$/, "", location)
      }
      END {
        sub(/\?.*$/, "", location)
        sub(/\/$/, "", location)
        count = split(location, parts, "/")
        if (count > 0) print parts[count]
      }
    ')"
  else
    fail "curl or wget is required"
  fi
  [[ -n "$tag" && "$tag" != "latest" ]] || fail "latest release did not provide a version tag"
  printf '%s\n' "$tag"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

xml_escape() {
  printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g' -e "s/'/\&apos;/g"
}

ensure_systemd_linger() {
  local current_user linger
  current_user="$(id -un)"
  if ! command -v loginctl >/dev/null 2>&1; then
    warn "systemd linger could not be enabled because loginctl is unavailable. The Local service may stop after logout. Run 'sudo loginctl enable-linger ${current_user}' when loginctl is available."
    return
  fi

  linger="$(loginctl show-user "$current_user" --property=Linger --value 2>/dev/null || true)"
  if [[ "$linger" == "yes" ]]; then
    return
  fi
  if loginctl --no-ask-password enable-linger "$current_user" </dev/null >/dev/null 2>&1; then
    log "Enabled systemd linger for ${current_user}"
    return
  fi

  warn "systemd linger could not be enabled without interaction. The Local service may stop after logout and will not start again until login. Run 'sudo loginctl enable-linger ${current_user}' to keep it running."
}

uninstall_local() {
  case "$(uname -s)" in
    Darwin)
      launchctl bootout "gui/$(id -u)/${service_label}" >/dev/null 2>&1 || true
      rm -f "${HOME}/Library/LaunchAgents/${service_label}.plist"
      ;;
    Linux)
      if command -v systemctl >/dev/null 2>&1; then
        systemctl --user disable --now "$service_name" >/dev/null 2>&1 || true
        rm -f "${HOME}/.config/systemd/user/${service_name}"
        systemctl --user daemon-reload >/dev/null 2>&1 || true
      fi
      ;;
  esac
  rm -f "$binary_path"
  if [[ "${1:-}" == "--purge" ]]; then
    rm -rf "${HOME}/.agent-bridge"
  fi
  log "Agent-Bridge Local removed"
}

if [[ "${1:-}" == "--uninstall" ]]; then
  uninstall_local "${2:-}"
  exit 0
fi

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  darwin)
    service_dir="${HOME}/Library/LaunchAgents"
    service_file="${service_dir}/${service_label}.plist"
    ;;
  linux)
    command -v systemctl >/dev/null 2>&1 || fail "systemd user services are required"
    service_dir="${HOME}/.config/systemd/user"
    service_file="${service_dir}/${service_name}"
    ;;
  *) fail "unsupported operating system: $os" ;;
esac

machine="$(uname -m)"
case "$machine" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) fail "unsupported architecture: $machine" ;;
esac

if [[ "$version" == "latest" ]]; then
  version="$(latest_version)"
fi
[[ "$version" == v* ]] || version="v${version}"
[[ "$version" =~ ^v[A-Za-z0-9._-]+$ ]] || fail "invalid AGENT_BRIDGE_VERSION"
expected_version="${version#v}"

config_file="${HOME}/.agent-bridge/tunnel/config.json"
if [[ "$local_url_explicit" != true && -r "$config_file" ]]; then
  configured_port="$(sed -n 's/.*"admin_port"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$config_file" | head -1)"
  if [[ "$configured_port" =~ ^[0-9]+$ ]] && (( configured_port > 0 && configured_port <= 65535 )); then
    local_url="http://localhost:${configured_port}"
  fi
fi

asset="agent-bridge_${version}_${os}_${arch}"
base_url="https://github.com/${repo}/releases/download/${version}"
tmp_dir="$(mktemp -d)"
transaction_started=false
install_succeeded=false
binary_existed=false
service_existed=false
was_running=false
was_enabled=false

rollback_installation() {
  log "Installation failed; restoring the previous Local service"
  set +e
  case "$os" in
    darwin)
      launchctl bootout "gui/$(id -u)/${service_label}" >/dev/null 2>&1 || true
      if launchctl print "gui/$(id -u)/${service_label}" >/dev/null 2>&1; then
        log "ERROR: Local service is still running; automatic rollback was not attempted"
        set -e
        return 1
      fi
      ;;
    linux)
      systemctl --user stop "$service_name" >/dev/null 2>&1 || true
      if systemctl --user is-active --quiet "$service_name"; then
        log "ERROR: Local service is still running; automatic rollback was not attempted"
        set -e
        return 1
      fi
      ;;
  esac

  if [[ "$binary_existed" == true ]]; then
    install -m 0755 "${tmp_dir}/previous-binary" "${binary_path}.rollback"
    mv -f "${binary_path}.rollback" "$binary_path"
  else
    rm -f "$binary_path" "${binary_path}.new"
  fi
  if [[ "$service_existed" == true ]]; then
    install -m 0644 "${tmp_dir}/previous-service" "$service_file"
  else
    rm -f "$service_file"
  fi

  case "$os" in
    darwin)
      if [[ "$service_existed" == true && "$was_running" == true ]]; then
        launchctl bootstrap "gui/$(id -u)" "$service_file" >/dev/null 2>&1 || true
        if ! launchctl print "gui/$(id -u)/${service_label}" >/dev/null 2>&1; then
          log "ERROR: previous Local service could not be restarted"
          set -e
          return 1
        fi
      fi
      ;;
    linux)
      systemctl --user daemon-reload >/dev/null 2>&1 || true
      if [[ "$service_existed" == true ]]; then
        if [[ "$was_enabled" == true ]]; then
          systemctl --user enable "$service_name" >/dev/null 2>&1 || true
        else
          systemctl --user disable "$service_name" >/dev/null 2>&1 || true
        fi
        if [[ "$was_running" == true ]]; then
          systemctl --user start "$service_name" >/dev/null 2>&1 || true
          if ! systemctl --user is-active --quiet "$service_name"; then
            log "ERROR: previous Local service could not be restarted"
            set -e
            return 1
          fi
        fi
      else
        systemctl --user disable "$service_name" >/dev/null 2>&1 || true
      fi
      ;;
  esac
  set -e
}

cleanup() {
  local status=$?
  local preserve_recovery=false
  trap - EXIT
  if [[ "$status" -ne 0 && "$transaction_started" == true && "$install_succeeded" != true ]]; then
    if ! rollback_installation; then
      preserve_recovery=true
      log "Recovery copies were kept in ${tmp_dir}"
    fi
  fi
  if [[ "$preserve_recovery" != true ]]; then
    rm -rf "$tmp_dir"
  fi
  exit "$status"
}
trap cleanup EXIT

log "Downloading ${asset}"
download "${base_url}/${asset}" "${tmp_dir}/${asset}"
download "${base_url}/SHA256SUMS" "${tmp_dir}/SHA256SUMS"

expected="$(awk -v asset="$asset" '$2 == asset || $2 == "*" asset {print $1}' "${tmp_dir}/SHA256SUMS" | head -1)"
[[ -n "$expected" ]] || fail "checksum entry not found for ${asset}"
actual="$(sha256_file "${tmp_dir}/${asset}")"
[[ "$actual" == "$expected" ]] || fail "checksum verification failed"

mkdir -p "$install_dir" "$service_dir"
install -d -m 0700 "${HOME}/.agent-bridge"
if [[ -f "$binary_path" ]]; then
  binary_existed=true
  cp -p "$binary_path" "${tmp_dir}/previous-binary"
fi
if [[ -f "$service_file" ]]; then
  service_existed=true
  cp -p "$service_file" "${tmp_dir}/previous-service"
fi

case "$os" in
  darwin)
    launchctl print "gui/$(id -u)/${service_label}" >/dev/null 2>&1 && was_running=true
    ;;
  linux)
    systemctl --user is-active --quiet "$service_name" && was_running=true
    systemctl --user is-enabled --quiet "$service_name" && was_enabled=true
    ;;
esac

transaction_started=true
case "$os" in
  darwin)
    launchctl bootout "gui/$(id -u)/${service_label}" >/dev/null 2>&1 || true
    if launchctl print "gui/$(id -u)/${service_label}" >/dev/null 2>&1; then
      fail "could not stop the existing Local service"
    fi
    ;;
  linux)
    systemctl --user stop "$service_name" >/dev/null 2>&1 || true
    if systemctl --user is-active --quiet "$service_name"; then
      fail "could not stop the existing Local service"
    fi
    ;;
esac

install -m 0755 "${tmp_dir}/${asset}" "${binary_path}.new"
mv -f "${binary_path}.new" "$binary_path"

case "$os" in
  darwin)
    escaped_binary="$(xml_escape "$binary_path")"
    local_log="${HOME}/.agent-bridge/agent-bridge.log"
    touch "$local_log"
    chmod 0600 "$local_log"
    escaped_log="$(xml_escape "$local_log")"
    cat >"${tmp_dir}/service" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>${service_label}</string>
  <key>ProgramArguments</key>
  <array><string>${escaped_binary}</string><string>--background</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>${escaped_log}</string>
  <key>StandardErrorPath</key><string>${escaped_log}</string>
</dict>
</plist>
EOF
    install -m 0644 "${tmp_dir}/service" "$service_file"
    launchctl bootstrap "gui/$(id -u)" "$service_file"
    ;;
  linux)
    cat >"${tmp_dir}/service" <<EOF
[Unit]
Description=Agent-Bridge Local
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart="${binary_path}" --background
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
EOF
    install -m 0644 "${tmp_dir}/service" "$service_file"
    systemctl --user daemon-reload
    systemctl --user enable "$service_name" >/dev/null
    systemctl --user restart "$service_name"
    ensure_systemd_linger
    ;;
esac

healthy=false
health_url="${local_url%/}/health"
for _ in $(seq 1 120); do
  health_body="$(fetch "$health_url" 2>/dev/null || true)"
  reported_version="$(printf '%s' "$health_body" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  if grep -Eq '"status"[[:space:]]*:[[:space:]]*"ok"' <<<"$health_body" &&
    [[ "$reported_version" == "$expected_version" ]]; then
    healthy=true
    break
  fi
  sleep 1
done

if [[ "$healthy" != true ]]; then
  case "$os" in
    darwin)
      tail -n 60 "${HOME}/.agent-bridge/agent-bridge.log" >&2 2>/dev/null || true
      ;;
    linux)
      journalctl --user -u "$service_name" -n 60 --no-pager >&2 2>/dev/null || true
      ;;
  esac
  fail "Local service did not become healthy with version ${expected_version}"
fi

install_succeeded=true
log "Installed Agent-Bridge Local ${version}"
log "Local Console: ${local_url}"
if [[ -z "${SSH_CONNECTION:-}" ]]; then
  if [[ "$os" == "darwin" ]]; then
    open "$local_url" >/dev/null 2>&1 || true
  elif command -v xdg-open >/dev/null 2>&1; then
    xdg-open "$local_url" >/dev/null 2>&1 || true
  fi
fi
