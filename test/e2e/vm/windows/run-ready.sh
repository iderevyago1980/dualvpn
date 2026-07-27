#!/usr/bin/env bash
# Подтверждение DualVPN на Windows через ГОТОВЫЙ образ (без переустановки).
# Берёт уже установленный Windows-образ (work/srv.qcow2, ставится один раз через
# run-server.sh), офлайн-инъектит harness + автозапуск и загружает: harness гоняет
# оба режима (SOCKS5 + Wintun-TUN) против ocserv и пишет result в C:\dvlab образа.
#
# Метод инъекции (обходит хрупкий FirstLogonCommand): qemu-nbd + ntfs-3g монтируют
# NTFS образа; harness кладётся в C:\dvlab; python-hivex правит SOFTWARE-hive —
# autologon (AutoAdminLogon=1 + DefaultPassword) + DisableCAD=1 (иначе Server ждёт
# Ctrl+Alt+Del и autologon не срабатывает) + RunOnce на run-harness.ps1.
#
# Требует: work/srv.qcow2 (готовый Server 2022), qemu-nbd, ntfs-3g, python3-hivex.
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../../../.. && pwd)"
OCS="$ROOT/test/e2e/backends/ocserv"
WORK="$(pwd)/work"
IMG="$WORK/srv.qcow2"
MNT="$WORK/winmnt"
NBD=/dev/nbd0
BOOT_TIMEOUT="${BOOT_TIMEOUT:-1200}"
export PATH="/usr/local/go/bin:$PATH"

[[ -f "$IMG" ]] || { echo "нет готового образа $IMG (сначала run-server.sh до установки)" >&2; exit 1; }

cleanup() {
  sudo pkill -f "file=$IMG" 2>/dev/null || true
  if mountpoint -q "$MNT"; then sudo umount "$MNT"; fi
  sudo qemu-nbd -d "$NBD" >/dev/null 2>&1 || true
  "$OCS/down.sh" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> сборка Windows-harness"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o "$WORK/dualvpn-harness.exe" "$ROOT/cmd/dualvpn-harness/"

echo "==> подключаю образ (qemu-nbd) и монтирую NTFS"
sudo modprobe nbd max_part=8
sudo qemu-nbd -d "$NBD" >/dev/null 2>&1 || true
sudo qemu-nbd -c "$NBD" "$IMG"; sleep 2
sudo ntfsfix -d "${NBD}p1" >/dev/null 2>&1 || true
mkdir -p "$MNT"
if mountpoint -q "$MNT"; then sudo umount "$MNT"; fi
sudo mount -t ntfs-3g -o rw,remove_hiberfile "${NBD}p1" "$MNT"

echo "==> кладу harness + провижн в C:\\dvlab"
sudo mkdir -p "$MNT/dvlab"
[[ -f "$ROOT/build/windows/deps/wintun.dll" ]] || "$ROOT/build/windows/fetch-wintun.sh"
sudo cp "$WORK/dualvpn-harness.exe" "$ROOT/build/windows/deps/wintun.dll" config.toml run-harness.ps1 "$MNT/dvlab/"

echo "==> правлю реестр: autologon + DisableCAD + RunOnce (python-hivex)"
sudo python3 - "$MNT/Windows/System32/config/SOFTWARE" <<'PY'
import hivex, sys, struct
h=hivex.Hivex(sys.argv[1], write=True)
def nav(node,*names):
    for n in names:
        c=None
        for ch in h.node_children(node):
            if h.node_name(ch).lower()==n.lower(): c=ch;break
        node = c if c is not None else h.node_add_child(node,n)
    return node
def sz(s): return (s+'\x00').encode('utf-16-le')
r=h.root()
wl=nav(r,"Microsoft","Windows NT","CurrentVersion","Winlogon")
for k,v in [("AutoAdminLogon","1"),("DefaultUserName","Administrator"),("DefaultPassword","Passw0rd!"),("DefaultDomainName","DUALVPN-WIN")]:
    h.node_set_value(wl,{"key":k,"t":1,"value":sz(v)})
sysp=nav(r,"Microsoft","Windows","CurrentVersion","Policies","System")
h.node_set_value(sysp,{"key":"DisableCAD","t":4,"value":struct.pack("<I",1)})
ro=nav(r,"Microsoft","Windows","CurrentVersion","RunOnce")
h.node_set_value(ro,{"key":"DualVPN","t":1,"value":sz(r"powershell -ExecutionPolicy Bypass -File C:\dvlab\run-harness.ps1")})
h.commit(None); print("реестр: autologon+DisableCAD+RunOnce записаны")
PY
sync
sudo umount "$MNT"; sudo qemu-nbd -d "$NBD" >/dev/null 2>&1

echo "==> ocserv up"; "$OCS/up.sh" >/dev/null

echo "==> boot образа (autologon → RunOnce → harness → Stop-Computer); до ${BOOT_TIMEOUT}s"
sudo timeout "$BOOT_TIMEOUT" qemu-system-x86_64 -enable-kvm -machine pc -m 4096 -smp 4 -display none \
  -drive file="$IMG",format=qcow2,if=ide,index=0,media=disk \
  -netdev user,id=n0 -device e1000,netdev=n0 \
  -serial file:"$WORK/console-ready.log" -monitor none || true

echo "==> читаю результат из C:\\dvlab образа"
sudo qemu-nbd -c "$NBD" "$IMG"; sleep 2
sudo ntfsfix -d "${NBD}p1" >/dev/null 2>&1 || true
sudo mount -t ntfs-3g -o ro "${NBD}p1" "$MNT"
if sudo test -f "$MNT/dvlab/result.txt"; then
  echo "===== result.txt ====="; sudo cat "$MNT/dvlab/result.txt" | tr -d '\r'
  rc=$(sudo cat "$MNT/dvlab/result.txt" | tr -d '\r\0' | sed -n 's/^OVERALL_EXIT=//p' | tail -1)
  echo "===== SOCKS5 PASS/изоляция ====="; sudo cat "$MNT/dvlab/hs.err" 2>/dev/null | tr -d '\r' | grep -E 'PASS|FAIL|готовы' | head
  echo "===== TUN PASS ====="; sudo cat "$MNT/dvlab/ht.err" 2>/dev/null | tr -d '\r' | grep -E 'PASS|FAIL|готовы' | head
  sudo umount "$MNT"; echo "==> OVERALL_EXIT=${rc:-нет}"; exit "${rc:-1}"
else
  sudo umount "$MNT"; echo "FAIL: нет result.txt (harness не отработал; см. $WORK/console-ready.log)"; exit 1
fi
