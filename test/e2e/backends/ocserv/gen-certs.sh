#!/usr/bin/env bash
# Генерирует локальный CA и серверные сертификаты для ocserv-A/B (SAN=127.0.0.1).
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p certs && cd certs

if [[ -f ca.pem ]]; then echo "certs уже есть, пропускаю"; exit 0; fi

# CA
openssl req -x509 -newkey rsa:2048 -nodes -keyout ca-key.pem -out ca.pem \
  -days 3650 -subj "/CN=DualVPN-LAB-CA"

for n in a b; do
  openssl req -newkey rsa:2048 -nodes -keyout "server-$n-key.pem" \
    -out "server-$n.csr" -subj "/CN=ocserv-$n"
  openssl x509 -req -in "server-$n.csr" -CA ca.pem -CAkey ca-key.pem \
    -CAcreateserial -out "server-$n.pem" -days 3650 \
    -extfile <(printf "subjectAltName=IP:127.0.0.1,DNS:localhost")
  rm -f "server-$n.csr"
done
echo "сертификаты готовы в $(pwd)"
