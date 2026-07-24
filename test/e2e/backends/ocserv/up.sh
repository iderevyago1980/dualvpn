#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
./gen-certs.sh

# Собираем собственный образ ocserv (tiredofit/ocserv не существует на Docker Hub,
# см. docker-compose.yml / task-5-report.md) заранее, чтобы использовать его же
# для генерации ocpasswd-хэша ниже.
docker compose build

# passwd для логина user/pass: штатный ocpasswd из пакета Debian ocserv,
# запущенный в одноразовом контейнере на нашем же образе (entrypoint переопределён,
# т.к. в образе ENTRYPOINT = ocserv).
if [[ ! -f passwd ]]; then
  docker run --rm --entrypoint sh dualvpn-e2e/ocserv:latest \
    -c 'printf "pass\npass\n" | ocpasswd -c /tmp/passwd user; cat /tmp/passwd' > passwd
fi

docker compose up -d
echo "ждём готовности ocserv..."
for i in $(seq 1 30); do
  if openssl s_client -connect 127.0.0.1:4443 -servername localhost </dev/null 2>/dev/null | grep -q CONNECTED; then
    echo "ocserv-a отвечает на :4443"; break
  fi
  sleep 1
done
for i in $(seq 1 30); do
  if openssl s_client -connect 127.0.0.1:4444 -servername localhost </dev/null 2>/dev/null | grep -q CONNECTED; then
    echo "ocserv-b отвечает на :4444"; break
  fi
  sleep 1
done

# NAT: VPN-пул → внутренняя сеть. Без MASQUERADE inner-host (whoami) получает
# пакеты с адреса пула 10.9x.0.x, но обратного маршрута к пулу не имеет →
# ответы теряются, и data-path «висит» при полностью рабочих auth/CSTP.
# ocserv сам NAT не настраивает, поэтому добавляем правило идемпотентно.
docker compose exec -T ocserv-a sh -c \
  'iptables -t nat -C POSTROUTING -s 10.90.0.0/24 -j MASQUERADE 2>/dev/null \
   || iptables -t nat -A POSTROUTING -s 10.90.0.0/24 -j MASQUERADE'
docker compose exec -T ocserv-b sh -c \
  'iptables -t nat -C POSTROUTING -s 10.91.0.0/24 -j MASQUERADE 2>/dev/null \
   || iptables -t nat -A POSTROUTING -s 10.91.0.0/24 -j MASQUERADE'
echo "MASQUERADE для VPN-пулов настроен"
