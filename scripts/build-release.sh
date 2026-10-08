#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-dev}"
OUT="${OUT:-${ROOT}/dist}"
LLAMA_TAG="${LLAMA_TAG:-b11476}"
LLAMA_ARCHIVE="llama-${LLAMA_TAG}-bin-ubuntu-x64.tar.gz"
LLAMA_URL="${LLAMA_URL:-https://github.com/ggml-org/llama.cpp/releases/download/${LLAMA_TAG}/${LLAMA_ARCHIVE}}"
LLAMA_SHA256="${LLAMA_SHA256:-2cda5ff9363967f1aba5b5b096032e1b7d9eb568b011769282bf34e4f1cf4b5e}"

mkdir -p "$OUT/package"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
  -o "$OUT/package/telegram-news-reader" ./cmd/telegram-news-reader

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -fL --retry 3 -o "${tmp}/${LLAMA_ARCHIVE}" "$LLAMA_URL"
echo "${LLAMA_SHA256}  ${tmp}/${LLAMA_ARCHIVE}" | sha256sum -c -
tar -xzf "${tmp}/${LLAMA_ARCHIVE}" -C "$tmp"
server="$(find "$tmp" -type f -name llama-server | head -n 1)"
[[ -n "$server" && -x "$server" ]] || {
  echo "official llama.cpp archive has no llama-server" >&2
  exit 1
}
mkdir -p "$OUT/package/llama"
cp -a "$(dirname "$server")/." "$OUT/package/llama/"
cp README.md LICENSE "$OUT/package/"
chmod 0755 "$OUT/package/telegram-news-reader" "$OUT/package/llama/llama-server"

archive="$OUT/telegram-news-reader-linux-amd64.tar.gz"
tar -C "$OUT/package" -czf "$archive" .
(cd "$OUT" && sha256sum "$(basename "$archive")" >"$(basename "$archive").sha256")
echo "$archive"
