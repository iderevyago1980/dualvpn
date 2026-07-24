#!/usr/bin/env bash
# Хостовая подготовка VM: базовый образ (скачать один раз) → overlay →
# seed ISO (cloud-init) → наполнить 9p-share артефактами.
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../../../.. && pwd)"
WORK="$(pwd)/work"
SHARE="$WORK/share"
IMG_URL="https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
BASE="$WORK/noble.img"
OVERLAY="$WORK/overlay.qcow2"
SEED="$WORK/seed.iso"

mkdir -p "$WORK" "$SHARE"

if [[ ! -f "$BASE" ]]; then
  echo "==> скачиваю Ubuntu 24.04 cloud image (~700 МБ, один раз)"
  curl -fL --retry 3 -o "$BASE.tmp" "$IMG_URL"
  mv "$BASE.tmp" "$BASE"
fi

echo "==> overlay-диск (backing = базовый образ, +8G под apt)"
rm -f "$OVERLAY"
qemu-img create -f qcow2 -F qcow2 -b "$BASE" "$OVERLAY" 12G >/dev/null

echo "==> seed ISO (cloud-init NoCloud, метка CIDATA)"
xorriso -as mkisofs -output "$SEED" -volid CIDATA -joliet -rock \
  cloud-init/user-data cloud-init/meta-data >/dev/null 2>&1

echo "==> наполняю 9p-share"
rm -f "$SHARE"/* 2>/dev/null || true
cp provision-guest.sh config.toml "$SHARE/"
cp "$ROOT/bin/dualvpn-harness" "$SHARE/"
DEB=$(ls "$ROOT"/bin/dualvpn_*.deb 2>/dev/null | head -1)
[[ -n "$DEB" ]] || { echo "нет bin/dualvpn_*.deb — сначала make deb" >&2; exit 1; }
cp "$DEB" "$SHARE/"

echo "==> готово: $WORK"
