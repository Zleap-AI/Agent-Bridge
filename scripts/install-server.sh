#!/usr/bin/env bash

set -euo pipefail

listen_explicit=false
data_explicit=false
database_explicit=false
public_url_explicit=false
[[ -n "${AGENT_BRIDGE_LISTEN_ADDR+x}" ]] && listen_explicit=true
[[ -n "${AGENT_BRIDGE_DATA_DIR+x}" ]] && data_explicit=true
[[ -n "${AGENT_BRIDGE_DATABASE_PATH+x}" ]] && database_explicit=true
[[ -n "${AGENT_BRIDGE_PUBLIC_URL+x}" ]] && public_url_explicit=true

repo="${AGENT_BRIDGE_REPOSITORY:-Zleap-AI/Agent-Bridge}"
version="${AGENT_BRIDGE_VERSION:-latest}"
listen_addr="${AGENT_BRIDGE_LISTEN_ADDR:-0.0.0.0:9201}"
data_dir="${AGENT_BRIDGE_DATA_DIR:-/var/lib/agent-bridge}"
database_path="${AGENT_BRIDGE_DATABASE_PATH:-}"
public_url="${AGENT_BRIDGE_PUBLIC_URL:-}"
config_dir="${AGENT_BRIDGE_CONFIG_DIR:-/etc/agent-bridge}"
binary_path="${AGENT_BRIDGE_BINARY_PATH:-/usr/local/bin/agent-bridge-server}"
service_name="agent-bridge-server"
service_user="agent-bridge"
unit_file="/etc/systemd/system/${service_name}.service"
env_file="${config_dir}/server.env"

log() { printf '[Agent-Bridge] %s\n' "$*"; }
fail() { printf '[Agent-Bridge] ERROR: %s\n' "$*" >&2; exit 1; }

[[ "$(id -u)" -eq 0 ]] || fail "run this installer as root (for example: curl ... | sudo bash)"
command -v systemctl >/dev/null 2>&1 || fail "systemd is required"

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

read_env_value() {
  local key="$1" file="$2"
  [[ -f "$file" ]] || return 1
  awk -v prefix="${key}=" 'index($0, prefix) == 1 {value = substr($0, length(prefix) + 1)} END {if (value != "") print value}' "$file"
}

upsert_env_value() {
  local file="$1" key="$2" value="$3"
  local next="${file}.next"
  grep -v "^${key}=" "$file" >"$next" || true
  if [[ -n "$value" ]]; then
    printf '%s=%s\n' "$key" "$value" >>"$next"
  fi
  mv -f "$next" "$file"
}

validate_single_line() {
  local name="$1" value="$2"
  [[ "$value" != *$'\n'* && "$value" != *$'\r'* ]] || fail "${name} must be a single line"
}

format_url_host() {
  local host="$1"
  host="${host#[}"
  host="${host%]}"
  if [[ "$host" == *:* ]]; then
    printf '[%s]' "$host"
  else
    printf '%s' "$host"
  fi
}

is_valid_ipv4() {
  local ip="$1" octet
  local -a octets
  [[ "$ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1
  IFS=. read -r -a octets <<<"$ip"
  [[ "${#octets[@]}" -eq 4 ]] || return 1
  for octet in "${octets[@]}"; do
    [[ "$octet" == "0" || "$octet" != 0* ]] || return 1
    (( 10#$octet <= 255 )) || return 1
  done
}

is_public_ipv4() {
  local ip="$1" first second third
  is_valid_ipv4 "$ip" || return 1
  IFS=. read -r first second third _ <<<"$ip"
  (( first > 0 && first < 224 )) || return 1
  case "$first" in
    10|127) return 1 ;;
    100) (( second < 64 || second > 127 )) || return 1 ;;
    169) (( second != 254 )) || return 1 ;;
    172) (( second < 16 || second > 31 )) || return 1 ;;
    192)
      (( second != 168 )) || return 1
      (( second != 0 || (third != 0 && third != 2) )) || return 1
      (( second != 88 || third != 99 )) || return 1
      ;;
    198)
      (( second != 18 && second != 19 )) || return 1
      (( second != 51 || third != 100 )) || return 1
      ;;
    203) (( second != 0 || third != 113 )) || return 1 ;;
  esac
  return 0
}

is_public_ip() {
  local ip="${1,,}"
  ip="${ip#[}"
  ip="${ip%]}"
  if [[ "$ip" == *:* ]]; then
    [[ "$ip" =~ ^[0-9a-f:]+$ ]] || return 1
    case "$ip" in
      ::|::1|fc*|fd*|fe8*|fe9*|fea*|feb*|2001:db8:*) return 1 ;;
      *) return 0 ;;
    esac
  fi
  is_public_ipv4 "$ip"
}

fetch_public_ipv4() {
  local url="$1"
  if command -v curl >/dev/null 2>&1; then
    curl -4fsS --proto '=https' --proto-redir '=https' \
      --connect-timeout 2 --max-time 4 "$url"
  else
    wget -4q --https-only --tries=1 --timeout=4 -O - "$url"
  fi
}

detect_public_host() {
  local listen_host="$1" candidate response url
  local -a candidates=("$listen_host") interface_addresses=()
  if command -v hostname >/dev/null 2>&1; then
    read -r -a interface_addresses <<<"$(hostname -I 2>/dev/null || true)"
    candidates+=("${interface_addresses[@]}")
  fi
  for candidate in "${candidates[@]}"; do
    if is_public_ip "$candidate"; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done

  for url in \
    "https://checkip.amazonaws.com/" \
    "https://api.ipify.org"; do
    response="$(fetch_public_ipv4 "$url" 2>/dev/null || true)"
    if is_public_ipv4 "$response"; then
      printf '%s\n' "$response"
      return 0
    fi
  done
  return 1
}

fetch_health() {
  local url="$1" use_insecure_tls="$2"
  if command -v curl >/dev/null 2>&1; then
    local curl_args=(-fsS --connect-timeout 2 --max-time 4)
    [[ "$use_insecure_tls" == true ]] && curl_args+=(-k)
    curl "${curl_args[@]}" "$url"
  else
    local wget_args=(-q --timeout=4 -O -)
    [[ "$use_insecure_tls" == true ]] && wget_args+=(--no-check-certificate)
    wget "${wget_args[@]}" "$url"
  fi
}

if [[ -r /etc/os-release ]]; then
  # shellcheck disable=SC1091
  . /etc/os-release
else
  fail "cannot identify Linux distribution"
fi
case "${ID:-}" in
  ubuntu|debian|centos|rhel|rocky|almalinux|fedora) ;;
  *) fail "unsupported Linux distribution: ${ID:-unknown}" ;;
esac

if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
  log "Installing download prerequisites"
  case "${ID:-}" in
    ubuntu|debian)
      apt-get update -y
      DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl
      ;;
    fedora|rhel|rocky|almalinux)
      if command -v dnf >/dev/null 2>&1; then
        dnf install -y ca-certificates curl
      else
        yum install -y ca-certificates curl
      fi
      ;;
    centos)
      yum install -y ca-certificates curl
      ;;
  esac
fi
command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 || fail "could not install curl or wget"

existing_listen="$(read_env_value AGENT_BRIDGE_LISTEN_ADDR "$env_file" || true)"
existing_data="$(read_env_value AGENT_BRIDGE_DATA_DIR "$env_file" || true)"
existing_database="$(read_env_value AGENT_BRIDGE_DATABASE_PATH "$env_file" || true)"
existing_public_url="$(read_env_value AGENT_BRIDGE_PUBLIC_URL "$env_file" || true)"
if [[ "$listen_explicit" != true && -n "$existing_listen" ]]; then
  listen_addr="$existing_listen"
fi
if [[ "$data_explicit" != true && -n "$existing_data" ]]; then
  data_dir="$existing_data"
elif [[ "$data_explicit" == true && -n "$existing_data" && "$data_dir" != "$existing_data" ]]; then
  fail "changing AGENT_BRIDGE_DATA_DIR during an upgrade is not automatic; move the data first"
fi
if [[ -z "$database_path" ]]; then
  database_path="${data_dir}/agent-bridge.db"
fi
if [[ "$database_explicit" != true && -n "$existing_database" ]]; then
  database_path="$existing_database"
elif [[ "$database_explicit" == true && -n "$existing_database" && "$database_path" != "$existing_database" ]]; then
  fail "changing AGENT_BRIDGE_DATABASE_PATH during an upgrade is not automatic; move the database first"
fi
if [[ "$public_url_explicit" != true && -n "$existing_public_url" ]]; then
  public_url="$existing_public_url"
fi

for pair in \
  "AGENT_BRIDGE_LISTEN_ADDR:${listen_addr}" \
  "AGENT_BRIDGE_DATA_DIR:${data_dir}" \
  "AGENT_BRIDGE_DATABASE_PATH:${database_path}" \
  "AGENT_BRIDGE_PUBLIC_URL:${public_url}" \
  "AGENT_BRIDGE_CONFIG_DIR:${config_dir}" \
  "AGENT_BRIDGE_BINARY_PATH:${binary_path}"; do
  validate_single_line "${pair%%:*}" "${pair#*:}"
done
[[ "$data_dir" == /* ]] || fail "AGENT_BRIDGE_DATA_DIR must be an absolute path"
[[ "$database_path" == /* ]] || fail "AGENT_BRIDGE_DATABASE_PATH must be an absolute path"
[[ "$config_dir" == /* ]] || fail "AGENT_BRIDGE_CONFIG_DIR must be an absolute path"
[[ "$binary_path" == /* ]] || fail "AGENT_BRIDGE_BINARY_PATH must be an absolute path"
[[ "$listen_addr" != *[[:space:]]* ]] || fail "AGENT_BRIDGE_LISTEN_ADDR must not contain whitespace"
for path_value in "$data_dir" "$database_path" "$config_dir" "$binary_path"; do
  [[ "$path_value" != *[[:space:]]* ]] || fail "installation paths must not contain whitespace"
done
[[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "invalid AGENT_BRIDGE_REPOSITORY"

listen_port="${listen_addr##*:}"
listen_host="${listen_addr%:*}"
listen_host="${listen_host#[}"
listen_host="${listen_host%]}"
[[ "$listen_port" =~ ^[0-9]+$ ]] || fail "could not determine port from ${listen_addr}"

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

asset="agent-bridge-server_${version}_linux_${arch}"
base_url="https://github.com/${repo}/releases/download/${version}"
tmp_dir="$(mktemp -d)"
transaction_started=false
install_succeeded=false
binary_existed=false
env_existed=false
unit_existed=false
database_existed=false
database_rollback_ready=false
was_active=false
was_enabled=false
backup_db=""

rollback_installation() {
  log "Installation failed; restoring the previous Server installation"
  set +e
  systemctl stop "$service_name" >/dev/null 2>&1 || true
  if systemctl is-active --quiet "$service_name"; then
    log "ERROR: Server is still running; files and database were left untouched"
    set -e
    return 1
  fi

  if [[ "$binary_existed" == true ]]; then
    install -m 0755 "${tmp_dir}/previous-binary" "${binary_path}.rollback"
    mv -f "${binary_path}.rollback" "$binary_path"
  else
    rm -f "$binary_path" "${binary_path}.new"
  fi
  if [[ "$env_existed" == true ]]; then
    install -m 0600 "${tmp_dir}/previous-env" "$env_file"
  else
    rm -f "$env_file"
  fi
  if [[ "$unit_existed" == true ]]; then
    install -m 0644 "${tmp_dir}/previous-unit" "$unit_file"
  else
    rm -f "$unit_file"
  fi

  if [[ "$database_rollback_ready" == true ]]; then
    rm -f "$database_path" "${database_path}-wal" "${database_path}-shm"
    if [[ "$database_existed" == true ]]; then
      for suffix in "" -wal -shm; do
        if [[ -f "${tmp_dir}/previous-db${suffix}" ]]; then
          install -m 0600 -o "$service_user" -g "$service_user" \
            "${tmp_dir}/previous-db${suffix}" "${database_path}${suffix}"
        fi
      done
    fi
  fi

  systemctl daemon-reload >/dev/null 2>&1 || true
  if [[ "$unit_existed" == true ]]; then
    if [[ "$was_enabled" == true ]]; then
      systemctl enable "$service_name" >/dev/null 2>&1 || true
    else
      systemctl disable "$service_name" >/dev/null 2>&1 || true
    fi
    if [[ "$was_active" == true ]]; then
      systemctl start "$service_name" >/dev/null 2>&1 || true
      if ! systemctl is-active --quiet "$service_name"; then
        log "ERROR: previous Server service could not be restarted"
        set -e
        return 1
      fi
    fi
  else
    systemctl disable "$service_name" >/dev/null 2>&1 || true
  fi
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

if ! id "$service_user" >/dev/null 2>&1; then
  nologin_shell="$(command -v nologin 2>/dev/null || true)"
  if [[ -z "$nologin_shell" ]]; then
    if [[ -x /usr/sbin/nologin ]]; then
      nologin_shell=/usr/sbin/nologin
    elif [[ -x /sbin/nologin ]]; then
      nologin_shell=/sbin/nologin
    else
      nologin_shell=/bin/false
    fi
  fi
  if getent group "$service_user" >/dev/null 2>&1; then
    useradd --system --gid "$service_user" --home-dir "$data_dir" --shell "$nologin_shell" "$service_user"
  else
    useradd --system --user-group --home-dir "$data_dir" --shell "$nologin_shell" "$service_user"
  fi
elif ! getent group "$service_user" >/dev/null 2>&1; then
  groupadd --system "$service_user"
  usermod --gid "$service_user" "$service_user"
fi

database_dir="$(dirname "$database_path")"
install -d -m 0700 -o "$service_user" -g "$service_user" "$data_dir"
if [[ "$database_dir" == "$data_dir" ]]; then
  :
elif [[ ! -d "$database_dir" ]]; then
  install -d -m 0700 -o "$service_user" -g "$service_user" "$database_dir"
elif command -v runuser >/dev/null 2>&1 && ! runuser -u "$service_user" -- test -w "$database_dir"; then
  fail "existing database directory is not writable by ${service_user}: ${database_dir}"
fi
install -d -m 0700 "$config_dir"

if [[ -f "$binary_path" ]]; then
  binary_existed=true
  cp -p "$binary_path" "${tmp_dir}/previous-binary"
fi
if [[ -f "$env_file" ]]; then
  env_existed=true
  cp -p "$env_file" "${tmp_dir}/previous-env"
fi
if [[ -f "$unit_file" ]]; then
  unit_existed=true
  cp -p "$unit_file" "${tmp_dir}/previous-unit"
fi
systemctl is-active --quiet "$service_name" && was_active=true
systemctl is-enabled --quiet "$service_name" && was_enabled=true

transaction_started=true
systemctl stop "$service_name" >/dev/null 2>&1 || true
if systemctl is-active --quiet "$service_name"; then
  fail "could not stop the existing Server service"
fi

if [[ -f "$database_path" ]]; then
  database_existed=true
  backup_db="${database_path}.backup.$(date +%Y%m%d%H%M%S)"
  counter=0
  while [[ -e "$backup_db" ]]; do
    counter=$((counter + 1))
    backup_db="${database_path}.backup.$(date +%Y%m%d%H%M%S).${counter}"
  done
  for suffix in "" -wal -shm; do
    if [[ -f "${database_path}${suffix}" ]]; then
      cp -p "${database_path}${suffix}" "${tmp_dir}/previous-db${suffix}"
      install -m 0600 -o "$service_user" -g "$service_user" \
        "${database_path}${suffix}" "${backup_db}${suffix}"
    fi
  done
  log "Database backup: ${backup_db}"
fi
database_rollback_ready=true

install -m 0755 "${tmp_dir}/${asset}" "${binary_path}.new"
mv -f "${binary_path}.new" "$binary_path"

if [[ "$env_existed" == true ]]; then
  cp -p "${tmp_dir}/previous-env" "${tmp_dir}/server.env"
else
  : >"${tmp_dir}/server.env"
fi

public_url_is_placeholder=false
if [[ -z "$public_url" ]]; then
  log "Detecting public IP"
  advertised_host="$(detect_public_host "$listen_host" || true)"
  if [[ -n "$advertised_host" ]]; then
    public_scheme="http"
    existing_tls_cert="$(read_env_value AGENT_BRIDGE_TLS_CERT_FILE "${tmp_dir}/server.env" || true)"
    existing_tls_key="$(read_env_value AGENT_BRIDGE_TLS_KEY_FILE "${tmp_dir}/server.env" || true)"
    if [[ -n "$existing_tls_cert" && -n "$existing_tls_key" ]]; then
      public_scheme="https"
    fi
    public_url="${public_scheme}://$(format_url_host "$advertised_host"):${listen_port}"
    log "Detected public address: ${public_url}"
  fi
fi

upsert_env_value "${tmp_dir}/server.env" AGENT_BRIDGE_LISTEN_ADDR "$listen_addr"
upsert_env_value "${tmp_dir}/server.env" AGENT_BRIDGE_DATA_DIR "$data_dir"
upsert_env_value "${tmp_dir}/server.env" AGENT_BRIDGE_DATABASE_PATH "$database_path"
upsert_env_value "${tmp_dir}/server.env" AGENT_BRIDGE_PUBLIC_URL "$public_url"
install -m 0600 "${tmp_dir}/server.env" "$env_file"

read_write_database=""
if [[ "$database_dir" != "$data_dir" ]]; then
  read_write_database="ReadWritePaths=${database_dir}"
fi
cat >"${tmp_dir}/service" <<EOF
[Unit]
Description=Agent-Bridge Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${service_user}
Group=${service_user}
UMask=0077
EnvironmentFile=-${env_file}
ExecStart=${binary_path}
WorkingDirectory=${data_dir}
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${data_dir}
${read_write_database}

[Install]
WantedBy=multi-user.target
EOF
install -m 0644 "${tmp_dir}/service" "$unit_file"

for suffix in "" -wal -shm; do
  if [[ -f "${database_path}${suffix}" ]]; then
    chown "$service_user:$service_user" "${database_path}${suffix}"
    chmod 0600 "${database_path}${suffix}"
  fi
done

systemctl daemon-reload
systemctl enable "$service_name" >/dev/null
systemctl restart "$service_name"

case "$listen_host" in
  ""|0.0.0.0|::) health_host="127.0.0.1" ;;
  *) health_host="$(format_url_host "$listen_host")" ;;
esac
tls_cert="$(read_env_value AGENT_BRIDGE_TLS_CERT_FILE "$env_file" || true)"
tls_key="$(read_env_value AGENT_BRIDGE_TLS_KEY_FILE "$env_file" || true)"
health_scheme="http"
insecure_health_tls=false
if [[ -n "$tls_cert" && -n "$tls_key" ]]; then
  health_scheme="https"
  insecure_health_tls=true
fi
health_url="${health_scheme}://${health_host}:${listen_port}/api/v1/status"

healthy=false
status_body=""
for _ in $(seq 1 60); do
  status_body="$(fetch_health "$health_url" "$insecure_health_tls" 2>/dev/null || true)"
  reported_version="$(printf '%s' "$status_body" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  if systemctl is-active --quiet "$service_name" &&
    grep -Eq '"status"[[:space:]]*:[[:space:]]*"ok"' <<<"$status_body" &&
    [[ "$reported_version" == "$expected_version" ]]; then
    healthy=true
    break
  fi
  sleep 1
done

if [[ "$healthy" != true ]]; then
  journalctl -u "$service_name" -n 60 --no-pager >&2 2>/dev/null || true
  fail "Server did not become healthy with version ${expected_version} at ${health_url}"
fi

install_succeeded=true

if [[ -z "$public_url" ]]; then
  public_url="${health_scheme}://SERVER_PUBLIC_IP:${listen_port}"
  public_url_is_placeholder=true
fi

log "Installed Agent-Bridge Server ${version}"
log "Service status: systemctl status ${service_name}"
log "Open TCP port ${listen_port} in your host firewall or cloud security group if needed."
if [[ "$public_url_is_placeholder" == true ]]; then
  log "No public IP was detected. Replace SERVER_PUBLIC_IP in the Setup URL with this server's public IP or domain."
fi

setup_env=(
  "AGENT_BRIDGE_PUBLIC_URL=${public_url}"
  "AGENT_BRIDGE_LISTEN_ADDR=${listen_addr}"
  "AGENT_BRIDGE_DATA_DIR=${data_dir}"
  "AGENT_BRIDGE_DATABASE_PATH=${database_path}"
)
[[ -n "$tls_cert" ]] && setup_env+=("AGENT_BRIDGE_TLS_CERT_FILE=${tls_cert}")
[[ -n "$tls_key" ]] && setup_env+=("AGENT_BRIDGE_TLS_KEY_FILE=${tls_key}")
if command -v runuser >/dev/null 2>&1; then
  setup_output="$(runuser -u "$service_user" -- env "${setup_env[@]}" "$binary_path" setup-url 2>/dev/null || true)"
else
  setup_output="$(env "${setup_env[@]}" "$binary_path" setup-url 2>/dev/null || true)"
fi
if [[ -n "$setup_output" ]]; then
  printf '%s\n' "$setup_output"
else
  log "Remote Console: ${public_url}"
  if ! grep -Eq '"initialized"[[:space:]]*:[[:space:]]*true' <<<"$status_body"; then
    log "Setup URL: sudo -u ${service_user} env AGENT_BRIDGE_PUBLIC_URL=${public_url} ${binary_path} setup-url"
  fi
fi
