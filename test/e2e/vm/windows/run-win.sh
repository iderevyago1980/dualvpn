#!/usr/bin/env bash
# E2E внутри настоящей Windows 11 Pro VM: ocserv (docker) + QEMU/KVM(OVMF) гость.
# Автоустановка через autounattend (обход TPM/SecureBoot), harness socks5+tun, GUI-smoke.
set -uo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../../../.. && pwd)"
OCS="$ROOT/test/e2e/backends/ocserv"
WORK="$(pwd)/work"
WIN_ISO="${WIN_ISO:-/mnt/Data-2/Distr/Microsoft Windows 11 [10.0.26100.8457], Version 24H2 (Updated May 2026) - Оригинальные образы от Microsoft MSDN [Ru]/ru-ru_windows_11_consumer_editions_version_24h2_updated_may_2026_x64_dvd_d061a709.iso}"
BOOT_TIMEOUT="${BOOT_TIMEOUT:-3300}"   # ~55 мин на установку+провижн
MON="$WORK/mon.sock"
export PATH="/usr/local/go/bin:$PATH"

cleanup() {
  sudo pkill -f "file=$WORK/win.qcow2" 2>/dev/null || true
  "$OCS/down.sh" >/dev/null 2>&1 || true
}
trap cleanup EXIT

[[ -f "$WIN_ISO" ]] || { echo "нет Windows ISO: $WIN_ISO (задай WIN_ISO=...)" >&2; exit 1; }

echo "==> подготовка артефактов Windows-VM"
./prepare-win.sh

# Симлинк на ISO с простым именем: в оригинальном пути есть запятая/пробелы,
# а QEMU -drive file= разбивает опции по запятым — путь бы обрезался.
WIN_LINK="$WORK/win-install.iso"
ln -sf "$WIN_ISO" "$WIN_LINK"
echo "==> ocserv up"; "$OCS/up.sh" >/dev/null

# Снятие блокера UEFI "Press any key to boot from CD": шлём Enter в монитор
# первые ~150с, пока не начнётся автоустановка. socat подключается к unix-сокету.
rm -f "$MON"
sendkeys() {
  for _ in $(seq 1 75); do
    [[ -S "$MON" ]] && printf 'sendkey ret\n' | socat - "UNIX-CONNECT:$MON" 2>/dev/null
    sleep 2
  done
}

echo "==> запуск QEMU(OVMF, q35, AHCI, e1000); установка Win11 + провижн (до ${BOOT_TIMEOUT}s)"
sendkeys &
SK_PID=$!
sudo timeout "$BOOT_TIMEOUT" qemu-system-x86_64 \
  -enable-kvm -machine q35 -m 4096 -smp 4 -display none \
  -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
  -drive if=pflash,format=raw,file="$WORK/OVMF_VARS.fd" \
  -cdrom "$WIN_LINK" \
  -drive file="$WORK/win.qcow2",if=none,id=disk0,format=qcow2 \
  -device ich9-ahci,id=ahci -device ide-hd,drive=disk0,bus=ahci.0 \
  -drive file="$WORK/data.iso",if=none,id=datacd,format=raw,media=cdrom,readonly=on \
  -device ide-cd,drive=datacd,bus=ahci.1 \
  -drive file="$WORK/result.img",if=none,id=resdisk,format=raw \
  -device ide-hd,drive=resdisk,bus=ahci.2 \
  -netdev user,id=n0 -device e1000,netdev=n0 \
  -monitor "unix:$MON,server,nowait" \
  -serial file:"$WORK/console.log" || true
kill "$SK_PID" 2>/dev/null || true

echo "==> читаю результат с FAT-диска"
MNT="$WORK/rmnt"; mkdir -p "$MNT"
if mountpoint -q "$MNT"; then sudo umount "$MNT"; fi
sudo mount -o loop "$WORK/result.img" "$MNT" 2>/dev/null || true
if [[ -f "$MNT/result.txt" ]]; then
  cat "$MNT/result.txt"
  rc=$(sed -n 's/^OVERALL_EXIT=//p' "$MNT/result.txt" | tail -1)
  sudo umount "$MNT" 2>/dev/null || true; rmdir "$MNT" 2>/dev/null || true
  echo "==> OVERALL_EXIT=${rc:-нет}"; exit "${rc:-1}"
else
  sudo umount "$MNT" 2>/dev/null || true; rmdir "$MNT" 2>/dev/null || true
  echo "FAIL: гость не оставил result.txt (см. $WORK/console.log — установка могла не дойти)"; exit 1
fi
