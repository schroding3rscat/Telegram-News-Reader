#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-dev}"
OUT="${OUT:-${ROOT}/dist}"
LLAMA_SERVER="${LLAMA_SERVER:-}"

mkdir -p "$OUT/package"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
  -o "$OUT/package/telegram-news-reader" ./cmd/telegram-news-reader

if [[ -z "$LLAMA_SERVER" || ! -x "$LLAMA_SERVER" ]]; then
  echo "LLAMA_SERVER must point to a Linux amd64 llama-server binary" >&2
  exit 1
fi
cp "$LLAMA_SERVER" "$OUT/package/llama-server"
cp README.md LICENSE "$OUT/package/"
chmod 0755 "$OUT/package/telegram-news-reader" "$OUT/package/llama-server"

archive="$OUT/telegram-news-reader-linux-amd64.tar.gz"
tar -C "$OUT/package" -czf "$archive" .
(cd "$OUT" && sha256sum "$(basename "$archive")" >"$(basename "$archive").sha256")
echo "$archive"
