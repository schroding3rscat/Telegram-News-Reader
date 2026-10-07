#!/usr/bin/env bash
set -Eeuo pipefail

DOMAIN="${DOMAIN:-${1:-}}"
QUERY_TOKEN="${QUERY_TOKEN:-${2:-}}"
[[ -n "$DOMAIN" && -n "$QUERY_TOKEN" ]] || {
  echo "Usage: DOMAIN=news.example.com QUERY_TOKEN=... sudo ./deploy/smoke-test.sh" >&2
  exit 2
}

for service in telegram-news-reader-llm telegram-news-reader caddy; do
  systemctl is-active --quiet "$service" || {
    systemctl status "$service" --no-pager >&2
    exit 1
  }
done

[[ "$(stat -c '%a' /etc/telegram-news-reader/config.yaml)" == "640" ]] ||
  { echo "Unexpected config permissions" >&2; exit 1; }
[[ "$(stat -c '%a' /var/lib/telegram-news-reader)" == "700" ]] ||
  { echo "Unexpected data directory permissions" >&2; exit 1; }

curl -fsS http://127.0.0.1:8081/health >/dev/null
local_status="$(curl -sS -o /dev/null -w '%{http_code}' \
  "http://127.0.0.1:8080/healthz?token=${QUERY_TOKEN}")"
[[ "$local_status" == "401" ]] || { echo "Unexpected local status: $local_status" >&2; exit 1; }

https_status="$(curl -sS -o /dev/null -w '%{http_code}' \
  "https://${DOMAIN}/healthz?token=${QUERY_TOKEN}")"
[[ "$https_status" == "401" ]] || { echo "Unexpected HTTPS status: $https_status" >&2; exit 1; }

expiry="$(echo | openssl s_client -servername "$DOMAIN" -connect "${DOMAIN}:443" 2>/dev/null |
  openssl x509 -noout -enddate)"
echo "Certificate ${expiry}"

llm_memory="$(systemctl show telegram-news-reader-llm -p MemoryCurrent --value)"
app_memory="$(systemctl show telegram-news-reader -p MemoryCurrent --value)"
echo "RSS/cgroup usage: app=${app_memory} bytes llm=${llm_memory} bytes"
echo "Smoke test passed"
