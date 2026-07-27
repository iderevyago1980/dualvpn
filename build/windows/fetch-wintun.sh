#!/usr/bin/env bash
# Скачивает Wintun (WireGuard, https://www.wintun.net) и раскладывает wintun.dll.
# Бинарь НЕ коммитится — он тянется этим скриптом из исходного релиза.
# Вызывается автоматически из `make build-windows` / `make installer` и из
# e2e-скриптов Windows-стенда. Идемпотентен (skip, если уже скачан).
set -euo pipefail
cd "$(dirname "$0")"
DEPS="deps"
VER="${WINTUN_VERSION:-0.14.1}"
URL="${WINTUN_URL:-https://www.wintun.net/builds/wintun-${VER}.zip}"
DLL="$DEPS/wintun/bin/amd64/wintun.dll"

if [[ -f "$DLL" ]]; then
  echo "wintun уже на месте ($DLL)"
else
  echo "==> скачиваю Wintun $VER: $URL"
  mkdir -p "$DEPS"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  curl -fL --retry 3 -o "$tmp/wintun.zip" "$URL"
  unzip -q -o "$tmp/wintun.zip" -d "$DEPS"
  [[ -f "$DLL" ]] || { echo "не нашёл $DLL после распаковки" >&2; exit 1; }
fi
# Копия рядом с deps/ — её используют e2e-скрипты и ручная сборка.
cp -f "$DLL" "$DEPS/wintun.dll"
echo "==> wintun.dll готов: $DEPS/wintun.dll"
