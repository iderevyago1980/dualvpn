#!/usr/bin/env bash
# Хостовая подготовка Windows 11 VM: exe, data-ISO (autounattend в корне),
# FAT-диск результата, install-qcow2, записываемая копия OVMF_VARS.
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../../../.. && pwd)"
WORK="$(pwd)/work"
STAGE="$WORK/stage"
export PATH="/usr/local/go/bin:$PATH"

mkdir -p "$WORK" "$STAGE"
rm -f "$STAGE"/* 2>/dev/null || true

echo "==> сборка Windows-бинарей"
( cd "$ROOT" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o "$STAGE/dualvpn-harness.exe" ./cmd/dualvpn-harness/ )
( cd "$ROOT" && GOFLAGS="-tags=webkit2_41" make build-windows >/dev/null && cp bin/DualVPN.exe "$STAGE/" ) || echo "WARN: DualVPN.exe не собран (GUI-smoke пропустится)"
[[ -f "$ROOT/build/windows/deps/wintun.dll" ]] || "$ROOT/build/windows/fetch-wintun.sh"
cp "$ROOT/build/windows/deps/wintun.dll" "$STAGE/"
cp autounattend.xml provision.ps1 config.toml "$STAGE/"

echo "==> data-ISO (autounattend в корне)"
xorriso -as mkisofs -output "$WORK/data.iso" -volid DUALVPN -joliet -rock "$STAGE"/ >/dev/null 2>&1

echo "==> FAT-диск результата (том RESULT + маркер)"
rm -f "$WORK/result.img"; truncate -s 64M "$WORK/result.img"; mkfs.vfat -n RESULT "$WORK/result.img" >/dev/null
MNT="$WORK/rmnt"; mkdir -p "$MNT"
if mountpoint -q "$MNT"; then sudo umount "$MNT"; fi   # снять залипший mount от прерванного прогона
sudo mount -o loop "$WORK/result.img" "$MNT"
echo "result-disk" | sudo tee "$MNT/RESULTDISK.marker" >/dev/null
# startup.nsh: OVMF при отсутствии загрузочной записи уходит в UEFI Shell и
# автозапускает startup.nsh с FAT-тома — он chainload'ит cdboot_noprompt.efi
# установочного CD (без блокера "Press any key"). Надёжнее, чем bootindex/sendkey.
sudo cp startup.nsh "$MNT/startup.nsh"
sudo umount "$MNT" || true; rmdir "$MNT" 2>/dev/null || true

echo "==> install-диск (пустой qcow2, до 32G)"
rm -f "$WORK/win.qcow2"; qemu-img create -f qcow2 "$WORK/win.qcow2" 32G >/dev/null

echo "==> записываемая копия OVMF_VARS"
cp /usr/share/OVMF/OVMF_VARS_4M.fd "$WORK/OVMF_VARS.fd"

echo "==> готово: $WORK"
