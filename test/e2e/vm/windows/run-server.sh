#!/usr/bin/env bash
# E2E внутри настоящей Windows Server 2022 VM (BIOS/SeaBIOS — обходит OVMF-блокер Win11).
# Раскладка: единственный CD = Server ISO (иначе Setup путается и не находит install.wim);
# autounattend.xml на floppy (надёжное место поиска Setup); harness/provision/config +
# результат — на FAT-диске (provision находит файлы сканом дисков).
set -uo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../../../.. && pwd)"
OCS="$ROOT/test/e2e/backends/ocserv"
WORK="$(pwd)/work"
WIN_ISO="${WIN_ISO:-/mnt/Data-2/Distr/SERVER_EVAL_x64FRE_en-us.iso}"
BOOT_TIMEOUT="${BOOT_TIMEOUT:-3600}"
MON="$WORK/mon-srv.sock"
FLOPPY="$WORK/autounattend.flp"
FATDISK="$WORK/result-srv.img"
export PATH="/usr/local/go/bin:$PATH"

cleanup() {
  sudo pkill -f "file=$WORK/srv.qcow2" 2>/dev/null || true
  "$OCS/down.sh" >/dev/null 2>&1 || true
}
trap cleanup EXIT
[[ -f "$WIN_ISO" ]] || { echo "нет Server ISO: $WIN_ISO" >&2; exit 1; }
mkdir -p "$WORK"

echo "==> сборка Windows-бинарей"
STAGE="$WORK/stage-srv"; mkdir -p "$STAGE"; rm -f "$STAGE"/* 2>/dev/null || true
( cd "$ROOT" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o "$STAGE/dualvpn-harness.exe" ./cmd/dualvpn-harness/ )
( cd "$ROOT" && GOFLAGS="-tags=webkit2_41" make build-windows >/dev/null && cp bin/DualVPN.exe "$STAGE/" ) || echo "WARN: DualVPN.exe не собран"
cp "$ROOT/build/windows/deps/wintun.dll" "$STAGE/"
cp provision.ps1 config.toml "$STAGE/"

echo "==> floppy с autounattend.xml"
rm -f "$FLOPPY"; mkfs.vfat -C "$FLOPPY" 1440 >/dev/null
MNTF="$WORK/fmnt"; mkdir -p "$MNTF"; if mountpoint -q "$MNTF"; then sudo umount "$MNTF"; fi
sudo mount -o loop "$FLOPPY" "$MNTF"; sudo cp autounattend-server.xml "$MNTF/autounattend.xml"; sudo umount "$MNTF"

echo "==> data-CD: provision + harness + config (всегда доступен с буквой)"
cp provision.ps1 "$STAGE/"
xorriso -as mkisofs -output "$WORK/data-srv.iso" -volid DUALVPN -joliet -rock "$STAGE"/ >/dev/null 2>&1

echo "==> FAT-диск результата (только маркер; provision включит его online)"
rm -f "$FATDISK"; truncate -s 128M "$FATDISK"; mkfs.vfat -n RESULT "$FATDISK" >/dev/null
MNT="$WORK/rmnt"; mkdir -p "$MNT"; if mountpoint -q "$MNT"; then sudo umount "$MNT"; fi
sudo mount -o loop "$FATDISK" "$MNT"
echo "result-disk" | sudo tee "$MNT/RESULTDISK.marker" >/dev/null
sudo umount "$MNT"

rm -f "$WORK/srv.qcow2"; qemu-img create -f qcow2 "$WORK/srv.qcow2" 32G >/dev/null
echo "==> ocserv up"; "$OCS/up.sh" >/dev/null

# SeaBIOS "Press any key to boot from CD": шлём Enter первые ~40с.
rm -f "$MON"
sendkeys() { for _ in $(seq 1 20); do [[ -S "$MON" ]] && printf 'sendkey ret\n' | socat - "UNIX-CONNECT:$MON" 2>/dev/null; sleep 2; done; }

echo "==> запуск QEMU (SeaBIOS, IDE-диск, единственный CD=Server, floppy=autounattend, FAT+e1000); до ${BOOT_TIMEOUT}s"
sendkeys & SK_PID=$!
sudo timeout "$BOOT_TIMEOUT" qemu-system-x86_64 \
  -enable-kvm -machine pc -m 4096 -smp 4 -display none \
  -drive file="$WORK/srv.qcow2",format=qcow2,if=ide,index=0,media=disk \
  -drive file="$FATDISK",format=raw,if=ide,index=1,media=disk \
  -drive file="$WIN_ISO",format=raw,if=ide,index=2,media=cdrom,readonly=on \
  -drive file="$WORK/data-srv.iso",format=raw,if=ide,index=3,media=cdrom,readonly=on \
  -drive file="$FLOPPY",format=raw,if=floppy \
  -netdev user,id=n0 -device e1000,netdev=n0 \
  -boot once=d,menu=off \
  -monitor "unix:$MON,server,nowait" -serial file:"$WORK/console-srv.log" || true
kill "$SK_PID" 2>/dev/null || true

echo "==> читаю результат с FAT-диска"
if mountpoint -q "$MNT"; then sudo umount "$MNT"; fi
sudo mount -o loop "$FATDISK" "$MNT" 2>/dev/null || true
if [[ -f "$MNT/result.txt" ]]; then
  cat "$MNT/result.txt"
  rc=$(sed -n 's/^OVERALL_EXIT=//p' "$MNT/result.txt" | tail -1)
  sudo umount "$MNT" 2>/dev/null || true
  echo "==> OVERALL_EXIT=${rc:-нет}"; exit "${rc:-1}"
else
  sudo umount "$MNT" 2>/dev/null || true
  echo "FAIL: гость не оставил result.txt (см. $WORK/console-srv.log)"; exit 1
fi
