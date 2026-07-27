#!/usr/bin/env bash
# E2E внутри настоящей Windows 11 Pro VM: ocserv (docker) + QEMU/KVM(OVMF) гость.
# Загрузка с FAT32 install-media (как UEFI-флешка): OVMF грузит \EFI\BOOT\BOOTX64.EFI,
# bootmgfw находит boot.wim/install.swm на своём FAT-томе (обход El-Torito/BCD-блокера).
# Автоустановка через autounattend (обход TPM/SecureBoot), harness socks5+tun, GUI-smoke.
set -uo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../../../.. && pwd)"
OCS="$ROOT/test/e2e/backends/ocserv"
WORK="$(pwd)/work"
BOOT_TIMEOUT="${BOOT_TIMEOUT:-3600}"   # ~60 мин на установку+провижн
export PATH="/usr/local/go/bin:$PATH"

cleanup() {
  sudo pkill -f "file=$WORK/win.qcow2" 2>/dev/null || true
  "$OCS/down.sh" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> подготовка артефактов (data-ISO, result, qcow2, OVMF vars)"
./prepare-win.sh
echo "==> сборка загрузочного install-media (FAT32 + split WIM)"
./build-install-media.sh
echo "==> ocserv up"; "$OCS/up.sh" >/dev/null

echo "==> запуск QEMU(OVMF, q35, AHCI, e1000); установка Win11 + провижн (до ${BOOT_TIMEOUT}s)"
# win.qcow2 на ahci.0 = Disk 0 (цель autounattend), bootindex=1.
# wininstall.img на ahci.1 = boot-media, bootindex=0 (грузится первым).
sudo timeout "$BOOT_TIMEOUT" qemu-system-x86_64 \
  -enable-kvm -machine q35 -m 4096 -smp 4 -display none \
  -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
  -drive if=pflash,format=raw,file="$WORK/OVMF_VARS.fd" \
  -device ich9-ahci,id=ahci \
  -drive file="$WORK/win.qcow2",if=none,id=disk0,format=qcow2 \
  -device ide-hd,drive=disk0,bus=ahci.0,bootindex=1 \
  -drive file="$WORK/wininstall.img",if=none,id=inst,format=raw \
  -device ide-hd,drive=inst,bus=ahci.1,bootindex=0 \
  -drive file="$WORK/data.iso",if=none,id=datacd,format=raw,media=cdrom,readonly=on \
  -device ide-cd,drive=datacd,bus=ahci.2 \
  -drive file="$WORK/result.img",if=none,id=resdisk,format=raw \
  -device ide-hd,drive=resdisk,bus=ahci.3 \
  -netdev user,id=n0 -device e1000,netdev=n0 \
  -serial file:"$WORK/console.log" -monitor none || true

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
