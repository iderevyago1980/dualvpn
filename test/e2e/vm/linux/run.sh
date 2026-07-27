#!/usr/bin/env bash
# E2E внутри настоящей Linux-VM: ocserv (docker) + QEMU/KVM гость через user-net.
# Гость ставит .deb, гоняет harness (socks5+tun) и GUI-smoke, пишет result.txt.
set -uo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../../../.. && pwd)"
OCS="$ROOT/test/e2e/backends/ocserv"
WORK="$(pwd)/work"
SHARE="$WORK/share"
BOOT_TIMEOUT="${BOOT_TIMEOUT:-360}"
export PATH="/usr/local/go/bin:$PATH"

cleanup() {
  sudo pkill -f "file=$WORK/overlay.qcow2" 2>/dev/null || true
  "$OCS/down.sh" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> сборка харнесса и .deb"
GOFLAGS="-tags=webkit2_41" make -C "$ROOT" deb >/dev/null
go build -o "$ROOT/bin/dualvpn-harness" "$ROOT/cmd/dualvpn-harness/"

echo "==> ocserv up"
"$OCS/up.sh" >/dev/null

echo "==> подготовка VM-артефактов"
./prepare.sh

rm -f "$SHARE/result.txt"
echo "==> запуск QEMU (user-net, 9p, cloud-init); ждём poweroff (до ${BOOT_TIMEOUT}s)"
sudo timeout "$BOOT_TIMEOUT" qemu-system-x86_64 \
  -enable-kvm -m 2048 -smp 2 -nographic \
  -drive file="$WORK/overlay.qcow2",if=virtio,format=qcow2 \
  -drive file="$WORK/seed.iso",if=virtio,format=raw,media=cdrom \
  -netdev user,id=n0 -device virtio-net-pci,netdev=n0 \
  -virtfs local,path="$SHARE",mount_tag=share,security_model=none,id=share \
  -serial file:"$WORK/console.log" -monitor none || true

echo "==> результат из гостя:"
if [[ -f "$SHARE/result.txt" ]]; then
  cat "$SHARE/result.txt"
  rc=$(sed -n 's/^OVERALL_EXIT=//p' "$SHARE/result.txt" | tail -1)
  echo "==> OVERALL_EXIT=${rc:-нет}"
  exit "${rc:-1}"
else
  echo "FAIL: гость не оставил result.txt (см. $WORK/console.log)"
  tail -30 "$WORK/console.log" 2>/dev/null || true
  exit 1
fi
