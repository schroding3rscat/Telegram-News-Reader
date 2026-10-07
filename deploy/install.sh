#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT="Telegram-News-Reader"
REPO="${REPO:-schroding3rscat/Telegram-News-Reader}"
SERVICE="telegram-news-reader"
USER_NAME="telegram-news-reader"
INSTALL_DIR="/opt/telegram-news-reader"
DATA_DIR="/var/lib/telegram-news-reader"
CONFIG_DIR="/etc/telegram-news-reader"
CONFIG_FILE="${CONFIG_DIR}/config.yaml"
MODEL_FILE="${DATA_DIR}/models/qwen3-0.6b-q4_k_m.gguf"
MODEL_URL="${MODEL_URL:-https://huggingface.co/bartowski/Qwen_Qwen3-0.6B-GGUF/resolve/main/Qwen_Qwen3-0.6B-Q4_K_M.gguf}"
RELEASE_BASE="${RELEASE_BASE:-https://github.com/${REPO}/releases/latest/download}"
NON_INTERACTIVE=0
UPDATE_ONLY=0
UNINSTALL=0
PURGE_DATA=0

log() { printf '\033[1;34m[telegram-news-reader]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Usage: install.sh [--non-interactive] [--update] [--uninstall] [--purge-data]

For non-interactive installation set:
  DOMAIN, ACME_EMAIL, ADMIN_USER, ADMIN_PASSWORD
Optional:
  QUERY_TOKEN, RELEASE_BASE, MODEL_URL
EOF
}

while (($#)); do
  case "$1" in
    --non-interactive) NON_INTERACTIVE=1 ;;
    --update) UPDATE_ONLY=1 ;;
    --uninstall) UNINSTALL=1 ;;
    --purge-data) PURGE_DATA=1 ;;
    -h|--help) usage; exit 0 ;;
    *) die "Unknown argument: $1" ;;
  esac
  shift
done

[[ "${EUID}" -eq 0 ]] || die "Run as root: curl ... | sudo bash"
[[ "$(uname -s)" == "Linux" ]] || die "Only Linux is supported"
[[ "$(uname -m)" == "x86_64" ]] || die "Only Linux x86_64 is supported by this installer"

if [[ "$UNINSTALL" -eq 1 ]]; then
  log "Stopping services"
  systemctl disable --now telegram-news-reader.service telegram-news-reader-llm.service 2>/dev/null || true
  rm -f /etc/systemd/system/telegram-news-reader.service /etc/systemd/system/telegram-news-reader-llm.service
  rm -rf "$INSTALL_DIR"
  rm -f /etc/caddy/Caddyfile.d/telegram-news-reader.caddy 2>/dev/null || true
  if [[ -f /etc/caddy/Caddyfile ]] && grep -q "Telegram News Reader" /etc/caddy/Caddyfile; then
    rm -f /etc/caddy/Caddyfile
  fi
  systemctl daemon-reload
  systemctl reload caddy 2>/dev/null || true
  if [[ "$PURGE_DATA" -eq 1 ]]; then
    rm -rf "$DATA_DIR" "$CONFIG_DIR"
    userdel "$USER_NAME" 2>/dev/null || true
  else
    log "Data and configuration preserved in ${DATA_DIR} and ${CONFIG_DIR}"
  fi
  exit 0
fi

command -v curl >/dev/null || die "curl is required"
command -v sha256sum >/dev/null || die "sha256sum is required"
command -v systemctl >/dev/null || die "systemd is required"
source /etc/os-release
case "${ID:-}" in
  ubuntu|debian) ;;
  *) die "Supported distributions: Debian and Ubuntu" ;;
esac
apt-get update -qq
apt-get install -y -qq ca-certificates openssl iproute2

available_kb="$(awk '/MemAvailable/ {print $2}' /proc/meminfo)"
((available_kb >= 1500000)) || die "At least 1.5 GB available RAM is required"
available_mb="$(df -Pm /var | awk 'NR==2 {print $4}')"
((available_mb >= 2500)) || die "At least 2.5 GB free disk space under /var is required"

prompt() {
  local variable="$1" label="$2" secret="${3:-0}" value="${!variable:-}"
  if [[ -n "$value" ]]; then return; fi
  [[ "$NON_INTERACTIVE" -eq 0 ]] || die "$variable is required in non-interactive mode"
  if [[ "$secret" -eq 1 ]]; then
    read -r -s -p "${label}: " value </dev/tty; printf '\n'
  else
    read -r -p "${label}: " value </dev/tty
  fi
  [[ -n "$value" ]] || die "${label} cannot be empty"
  printf -v "$variable" '%s' "$value"
}

if [[ "$UPDATE_ONLY" -eq 0 || ! -f "$CONFIG_FILE" ]]; then
  prompt DOMAIN "Domain (for example news.example.com)"
  prompt ACME_EMAIL "Email for Let's Encrypt"
  prompt ADMIN_USER "Admin login"
  prompt ADMIN_PASSWORD "Admin password" 1
  QUERY_TOKEN="${QUERY_TOKEN:-$(openssl rand -hex 24)}"
  [[ "$DOMAIN" =~ ^[A-Za-z0-9][A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]] || die "Invalid FQDN"
  [[ "$ACME_EMAIL" == *@*.* ]] || die "Invalid ACME email"
else
  DOMAIN="$(awk '/public_url:/ {gsub(/["'\'']/, "", $2); sub("https://", "", $2); print $2}' "$CONFIG_FILE")"
  ACME_EMAIL="${ACME_EMAIL:-$(awk '/^[[:space:]]*email / {print $2; exit}' /etc/caddy/Caddyfile 2>/dev/null || true)}"
  ACME_EMAIL="${ACME_EMAIL:-admin@${DOMAIN}}"
fi

public_ipv4="$(curl -4fsS --max-time 10 https://api.ipify.org || true)"
resolved_ipv4="$(getent ahostsv4 "$DOMAIN" | awk 'NR==1 {print $1}')"
if [[ -z "$resolved_ipv4" || ( -n "$public_ipv4" && "$resolved_ipv4" != "$public_ipv4" ) ]]; then
  die "DNS check failed: ${DOMAIN} resolves to '${resolved_ipv4:-nothing}', VM public IPv4 is '${public_ipv4:-unknown}'. Update the A record and retry."
fi

for port in 80 443; do
  if ss -H -ltn "sport = :${port}" 2>/dev/null | grep -q . && ! systemctl is-active --quiet caddy; then
    die "Port ${port} is already occupied"
  fi
done

install_caddy() {
  if command -v caddy >/dev/null; then return; fi
  log "Installing Caddy from its official repository"
  apt-get update -qq
  apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl gpg
  curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key |
    gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt |
    tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null
  apt-get update -qq
  apt-get install -y -qq caddy
}

install_release() {
  local tmp archive checksum
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  archive="${tmp}/telegram-news-reader-linux-amd64.tar.gz"
  checksum="${archive}.sha256"
  log "Downloading signed release artifacts"
  curl -fL --retry 3 "${RELEASE_BASE}/telegram-news-reader-linux-amd64.tar.gz" -o "$archive"
  curl -fL --retry 3 "${RELEASE_BASE}/telegram-news-reader-linux-amd64.tar.gz.sha256" -o "$checksum"
  (cd "$tmp" && sha256sum -c "$(basename "$checksum")")
  mkdir -p "$INSTALL_DIR"
  tar -xzf "$archive" -C "$INSTALL_DIR"
  chmod 0755 "$INSTALL_DIR/telegram-news-reader" "$INSTALL_DIR/llama-server"
}

install_caddy
install_release

if ! id "$USER_NAME" >/dev/null 2>&1; then
  useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$USER_NAME"
fi
install -d -o "$USER_NAME" -g "$USER_NAME" -m 0700 "$DATA_DIR" "$DATA_DIR/models" "$DATA_DIR/tmp"
install -d -o root -g "$USER_NAME" -m 0750 "$CONFIG_DIR"

if [[ ! -s "$MODEL_FILE" ]]; then
  log "Downloading Qwen3-0.6B Q4_K_M model (~0.5 GB)"
  curl -fL --retry 3 "$MODEL_URL" -o "${MODEL_FILE}.part"
  mv "${MODEL_FILE}.part" "$MODEL_FILE"
  chown "$USER_NAME:$USER_NAME" "$MODEL_FILE"
  chmod 0600 "$MODEL_FILE"
fi

if [[ ! -f "$CONFIG_FILE" ]]; then
  password_hash="$("$INSTALL_DIR/telegram-news-reader" hash-password "$ADMIN_PASSWORD")"
  umask 077
  cat >"$CONFIG_FILE" <<EOF
listen: 127.0.0.1:8080
data_dir: ${DATA_DIR}
database_path: ${DATA_DIR}/reader.db
admin:
  username: "${ADMIN_USER}"
  password_bcrypt: "${password_hash}"
  query_token: "${QUERY_TOKEN}"
  public_url: "https://${DOMAIN}"
telegram:
  session_path: ${DATA_DIR}/telegram.session
  bot_api_base_url: https://api.telegram.org
llm:
  base_url: http://127.0.0.1:8081
  model: qwen3-0.6b
  timeout: 45s
  max_examples: 6
  ad_threshold: 0.78
  review_threshold: 0.52
pipeline:
  workers: 1
  poll_interval: 2s
  retry_base: 10s
  retention: 168h
  album_settle_delay: 2s
  text_hamming_distance: 7
  image_hamming_distance: 8
  max_queue_depth: 100000
  classification_batch: 1
log:
  level: info
  json: true
EOF
  chown root:"$USER_NAME" "$CONFIG_FILE"
  chmod 0640 "$CONFIG_FILE"
else
  QUERY_TOKEN="$(awk '/query_token:/ {gsub(/["'\'']/, "", $2); print $2}' "$CONFIG_FILE")"
  ADMIN_USER="$(awk '/username:/ {gsub(/["'\'']/, "", $2); print $2; exit}' "$CONFIG_FILE")"
fi

cat >/etc/systemd/system/telegram-news-reader-llm.service <<EOF
[Unit]
Description=Telegram News Reader local LLM
After=network-online.target
Wants=network-online.target

[Service]
User=${USER_NAME}
Group=${USER_NAME}
ExecStart=${INSTALL_DIR}/llama-server -m ${MODEL_FILE} --host 127.0.0.1 --port 8081 -c 2048 -np 1 -ngl 0 --jinja
Restart=on-failure
RestartSec=5
MemoryMax=1500M
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${DATA_DIR}
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6

[Install]
WantedBy=multi-user.target
EOF

cat >/etc/systemd/system/telegram-news-reader.service <<EOF
[Unit]
Description=Telegram News Reader
After=network-online.target telegram-news-reader-llm.service
Wants=network-online.target telegram-news-reader-llm.service

[Service]
User=${USER_NAME}
Group=${USER_NAME}
ExecStart=${INSTALL_DIR}/telegram-news-reader -config ${CONFIG_FILE}
Restart=on-failure
RestartSec=5
MemoryMax=384M
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadOnlyPaths=${CONFIG_FILE}
ReadWritePaths=${DATA_DIR}
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6

[Install]
WantedBy=multi-user.target
EOF

cat >/etc/caddy/Caddyfile <<EOF
# Telegram News Reader — generated by install.sh
{
  email ${ACME_EMAIL}
  admin off
}

${DOMAIN} {
  encode zstd gzip
  header {
    Strict-Transport-Security "max-age=31536000; includeSubDomains"
    X-Content-Type-Options "nosniff"
    X-Frame-Options "DENY"
    Referrer-Policy "no-referrer"
    -Server
  }
  reverse_proxy 127.0.0.1:8080
}
EOF

caddy validate --config /etc/caddy/Caddyfile
systemctl daemon-reload
systemctl enable --now telegram-news-reader-llm.service telegram-news-reader.service caddy.service
systemctl restart telegram-news-reader-llm.service telegram-news-reader.service caddy.service

log "Waiting for local services"
for _ in $(seq 1 60); do
  if curl -fsS --max-time 2 "http://127.0.0.1:8081/health" >/dev/null &&
     [[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 \
       "http://127.0.0.1:8080/healthz?token=${QUERY_TOKEN}")" == "401" ]]; then
    break
  fi
  sleep 2
done

log "Waiting for Let's Encrypt certificate"
for _ in $(seq 1 30); do
  if [[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 \
      "https://${DOMAIN}/healthz?token=${QUERY_TOKEN}")" == "401" ]]; then
    log "Installation complete"
    printf 'Admin URL: https://%s/?token=%s\n' "$DOMAIN" "$QUERY_TOKEN"
    exit 0
  fi
  sleep 3
done

die "Services were installed, but HTTPS health check failed. Inspect: journalctl -u caddy -u telegram-news-reader"
