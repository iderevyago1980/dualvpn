#!/usr/bin/env bash
# Собрать загрузочный образ установки Windows как UEFI-флешку: GPT-диск с ESP
# (FAT32) — файлы ISO + split install.wim (>4GB не влезает в FAT32) + autounattend.
# ESP даёт нормальный HD()-device-path, поэтому OVMF грузит \EFI\BOOT\BOOTX64.EFI
# как removable-media (в отличие от superfloppy, где boot-запись не ремапится).
set -euo pipefail
cd "$(dirname "$0")"
WORK="$(pwd)/work"
WIN_ISO="${WIN_ISO:-/mnt/Data-2/Distr/Microsoft Windows 11 [10.0.26100.8457], Version 24H2 (Updated May 2026) - Оригинальные образы от Microsoft MSDN [Ru]/ru-ru_windows_11_consumer_editions_version_24h2_updated_may_2026_x64_dvd_d061a709.iso}"
IMG="$WORK/wininstall.img"
ISOMNT="$WORK/isomnt"
IMGMNT="$WORK/imgmnt"

[[ -f "$WIN_ISO" ]] || { echo "нет ISO: $WIN_ISO" >&2; exit 1; }
mkdir -p "$WORK" "$ISOMNT" "$IMGMNT"

LOOP=""
cleanup() {
  if mountpoint -q "$IMGMNT"; then sudo umount "$IMGMNT"; fi
  if mountpoint -q "$ISOMNT"; then sudo umount "$ISOMNT"; fi
  [[ -n "$LOOP" ]] && sudo losetup -d "$LOOP" 2>/dev/null || true
}
trap cleanup EXIT
cleanup

echo "==> GPT-диск с ESP FAT32 (9 ГБ)"
rm -f "$IMG"; truncate -s 9G "$IMG"
sgdisk -Z "$IMG" >/dev/null 2>&1 || true
sgdisk -n 1:0:0 -t 1:EF00 -c 1:WININSTALL "$IMG" >/dev/null
LOOP=$(sudo losetup -Pf --show "$IMG")
sudo mkfs.vfat -F 32 -n WININSTALL "${LOOP}p1" >/dev/null

sudo mount -o loop,ro "$WIN_ISO" "$ISOMNT"
sudo mount "${LOOP}p1" "$IMGMNT"

echo "==> копирую файлы ISO (кроме sources/install.wim)"
sudo rsync -a --no-owner --no-group --exclude 'sources/install.wim' "$ISOMNT"/ "$IMGMNT"/

echo "==> split install.wim -> install.swm (<4GB чанки)"
sudo wimlib-imagex split "$ISOMNT/sources/install.wim" "$IMGMNT/sources/install.swm" 3800

echo "==> autounattend + startup.nsh (fallback) в корень"
sudo cp autounattend.xml "$IMGMNT/autounattend.xml"
sudo cp startup.nsh "$IMGMNT/startup.nsh"

sync
echo "==> готово: $IMG"
