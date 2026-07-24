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
"$ROOT/bin/dualvpn-harness" -config "$CFG" -mode socks5 -insecure -timeout 30s
sock_rc=$?
echo "SOCKS5 exit=$sock_rc"

# Изоляция в SOCKS5-режиме обеспечивается сервером, не клиентским роутингом:
# SOCKS5-мост поднимает netstack с общим маршрутом 0.0.0.0/0 (Routes/split-include
# на клиенте здесь не гейтят доступность), поэтому недоступность сети B через
# порт туннеля A — следствие того, что у ocserv-A нет маршрута/интерфейса в сеть B
# (inner_b изолирована на уровне docker-сети сервера), а не того, что netstack
# клиента "не знает" про сеть B.
echo "==> проверка изоляции через curl --socks5"
if curl -s --max-time 5 --socks5 127.0.0.1:21080 http://192.168.91.10/ >/dev/null; then
  echo "FAIL: туннель A достаёт до сети B (изоляция нарушена)"; exit 1
else
  echo "OK: сеть B недоступна через туннель A (изоляция обеспечена сервером ocserv-A)"
fi

echo "==> TUN-прогон (через sudo)"
sudo -E "$ROOT/bin/dualvpn-harness" -config "$CFG" -mode tun -insecure -timeout 30s
tun_rc=$?
echo "TUN exit=$tun_rc"

echo "==> ИТОГ: socks=$sock_rc tun=$tun_rc"
exit $(( sock_rc + tun_rc ))
