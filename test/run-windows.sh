#!/usr/bin/env bash
# Прогон тестов на Windows в обход Smart App Control.
#
# `go test` собирает тестовый бинарь во временный каталог и запускает его
# оттуда. Smart App Control (Windows 11) блокирует неподписанные файлы из
# %TEMP% по репутации, из-за чего прогон случайно падает с
# «An Application Control policy has blocked this file» — при полностью
# рабочем коде. Здесь тесты компилируются в bin/tests и запускаются оттуда:
# из обычного каталога политика их пропускает.
#
# Использование: test/run-windows.sh [пакеты...]   (по умолчанию — все)
set -uo pipefail
cd "$(dirname "$0")/.."

export PATH="/c/Program Files/Go/bin:$PATH"
OUT="bin/tests"
mkdir -p "$OUT"

packages=("$@")
if [[ ${#packages[@]} -eq 0 ]]; then
  mapfile -t packages < <(go list ./internal/... ./test/...)
fi

root="$PWD"
failed=0
for pkg in "${packages[@]}"; do
  name="${pkg//\//_}"
  bin="$root/$OUT/${name}.test.exe"

  # Пакеты без тестов go test -c пропускает, бинарь не создаётся.
  if ! go test -c -o "$bin" "$pkg" 2>/dev/null || [[ ! -f "$bin" ]]; then
    continue
  fi

  # go test запускает тесты из каталога пакета — тесты вправе рассчитывать
  # на относительные пути к файлам репозитория (например, к примеру конфига).
  dir="$(go list -f '{{.Dir}}' "$pkg")"

  if (cd "$dir" && "$bin" -test.timeout=300s >/dev/null 2>&1); then
    echo "ok    $pkg"
  else
    echo "FAIL  $pkg"
    (cd "$dir" && "$bin" -test.timeout=300s 2>&1) | grep -E "^(---|\s+---)|FAIL|panic" | head -20
    failed=$((failed + 1))
  fi
done

if [[ $failed -gt 0 ]]; then
  echo "провалено пакетов: $failed"
  exit 1
fi
echo "все пакеты прошли"
