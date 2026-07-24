#!/usr/bin/env bash
# E2E host-прогон: ocserv-бэкенд + харнесс (SOCKS5, затем TUN).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BACKEND="${E2E_BACKEND:-ocserv}"
OCS="$ROOT/test/e2e/backends/ocserv"
CFG="$OCS/config.toml"
export PATH="/usr/local/go/bin:$PATH"

cleanup() { "$OCS/down.sh" || true; }
trap cleanup EXIT

echo "==> сборка харнесса"
go build -o "$ROOT/bin/dualvpn-harness" "$ROOT/cmd/dualvpn-harness/"

echo "==> подъём бэкенда $BACKEND"
"$OCS/up.sh"

echo "==> SOCKS5-прогон (без root)"
sock_rc=0
"$ROOT/bin/dualvpn-harness" -config "$CFG" -mode socks5 -insecure -timeout 30s || sock_rc=$?
echo "SOCKS5 exit=$sock_rc"

# Изоляция (SOCKS5-порт одного туннеля не должен доставать до сети другого)
# проверяется внутри самого харнесса, пока мосты ещё живы — см.
# cmd/dualvpn-harness/main.go:checkIsolation(). Постфактум-curl отсюда убран:
# он бил по уже закрытому после выхода процесса порту и ничего не проверял.

echo "==> TUN-прогон (через sudo)"
tun_rc=0
sudo -E "$ROOT/bin/dualvpn-harness" -config "$CFG" -mode tun -insecure -timeout 30s || tun_rc=$?
echo "TUN exit=$tun_rc"

echo "==> ИТОГ: socks=$sock_rc tun=$tun_rc"
exit $(( sock_rc + tun_rc ))
