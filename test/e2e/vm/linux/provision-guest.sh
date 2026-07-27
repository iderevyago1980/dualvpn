#!/usr/bin/env bash
# Гостевой провижн: ставит .deb, гоняет harness (socks5 + tun) и GUI-smoke,
# пишет сводку в /mnt/share/result.txt и гасит VM. Запускается cloud-init как root.
set -uo pipefail
SHARE=/mnt/share
RESULT="$SHARE/result.txt"
: > "$RESULT"
log() { echo "[$(date +%H:%M:%S)] $*" | tee -a "$RESULT"; }

log "=== установка .deb (проверка зависимостей на чистой системе) ==="
export DEBIAN_FRONTEND=noninteractive
apt-get update -y >/dev/null 2>&1 || log "WARN: apt-get update не прошёл"
DEB=$(ls "$SHARE"/dualvpn_*.deb 2>/dev/null | head -1)
if [[ -z "$DEB" ]]; then log "FAIL: .deb не найден в шаре"; deb_rc=1; else
  if apt-get install -y "$DEB" >>"$SHARE/apt.log" 2>&1; then
    log "PASS: .deb установлен, зависимости разрешены"; deb_rc=0
  else
    log "FAIL: apt install .deb (см. apt.log)"; deb_rc=1
  fi
fi

install -m0755 "$SHARE/dualvpn-harness" /usr/local/bin/dualvpn-harness
CFG="$SHARE/config.toml"

log "=== harness SOCKS5 (без root-специфики) ==="
/usr/local/bin/dualvpn-harness -config "$CFG" -mode socks5 -insecure -timeout 40s >>"$SHARE/harness-socks5.log" 2>&1
socks_rc=$?
log "SOCKS5 exit=$socks_rc"

log "=== harness TUN (root, реальные маршруты) ==="
/usr/local/bin/dualvpn-harness -config "$CFG" -mode tun -insecure -timeout 40s >>"$SHARE/harness-tun.log" 2>&1
tun_rc=$?
log "TUN exit=$tun_rc"

log "=== GUI-smoke (xvfb-run dualvpn N сек) ==="
gui_rc=0
if command -v xvfb-run >/dev/null 2>&1 && command -v dualvpn >/dev/null 2>&1; then
  xvfb-run -a dualvpn >>"$SHARE/gui.log" 2>&1 &
  gpid=$!
  sleep 8
  if kill -0 "$gpid" 2>/dev/null; then
    log "PASS: GUI прожил 8с без краша (зависимости на месте)"
    kill "$gpid" 2>/dev/null; wait "$gpid" 2>/dev/null
  else
    wait "$gpid" 2>/dev/null; gui_rc=$?
    log "FAIL: GUI упал за 8с (exit=$gui_rc, см. gui.log)"
    gui_rc=1
  fi
else
  # xvfb ставится в user-data через apt? ставим здесь при отсутствии
  apt-get install -y xvfb >>"$SHARE/apt.log" 2>&1 || true
  log "WARN: xvfb/dualvpn не найдены сразу; см. gui.log"
  gui_rc=1
fi

TOTAL=$(( deb_rc + (socks_rc!=0) + (tun_rc!=0) + gui_rc ))
log "=== ИТОГ: deb=$deb_rc socks=$socks_rc tun=$tun_rc gui=$gui_rc → провалов=$TOTAL ==="
echo "OVERALL_EXIT=$TOTAL" >> "$RESULT"
sync
log "гашу VM"
poweroff
