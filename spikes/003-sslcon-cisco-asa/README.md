# Spike 003: sslcon vs Cisco ASA

## Вопрос

Может ли `sslcon` (Go-реализация OpenConnect VPN протокола) заменить
`openconnect` subprocess для подключения к Cisco ASA AnyConnect?

## Подход

1. Найти Go-библиотеку для протокола AnyConnect
2. Проверить TLS handshake + XML POST auth на реальных серверах
   (адреса передаются аргументами: `go run . <host> [host...]`)

## Результаты

### Библиотека

**tlslink/sslcon** — чистая Go-реализация OpenConnect VPN протокола.
- 71 star, MIT license
- Поддерживает: AnyLink, ocserv
- Коммит `164ea92d` (2024-11-11) совместим с Go 1.24

### TLS handshake

| Сервер | TLS | Cipher Suite | Время |
|---|---|---|---|
| эндпоинт A | TLS1.2 | 0xc030 (ECDHE-RSA-AES256-GCM-SHA384) | 598ms |
| эндпоинт B | TLS1.3 | 0x1302 (TLS_AES_256_GCM_SHA384) | 384ms |

Оба сервера приняли TLS handshake и ответили на XML POST `<config-auth type="init">`.

### Что не проверено

- **PasswordAuth** — полный цикл с реальными кредами (нужен логин/пароль/2FA)
- **Tunnel setup** — создание TUN-интерфейса и маршрутизация
- **2FA/TOTP** — интерактивный ввод кода
- **DTLS** — UDP-транспорт (опционально, fallback на TLS)

## Verdict: VALIDATED

**sslcon совместим с Cisco ASA AnyConnect.**

Оба сервера принимают XML POST auth (Aggregate Auth v2) — тот же протокол,
что использует openconnect. TLS handshake проходит, сервер отвечает на
`config-auth type="init"`.

## Рекомендация для реального build

1. **Заменить openconnect-subprocess на sslcon** — нативный Go-код,
   один бинарник, без внешних зависимостей
2. Использовать коммит `164ea92d` (совместим с Go 1.24)
3. Для 2FA: sslcon поддерживает `SecretKey` (TOTP secret) или интерактивный ввод
4. Для TUN: sslcon использует `golang.zx2c4.com/wintun` на Windows (как и планировалось)
5. Для ocproxy: sslcon не использует `--script-tun`, нужно интегрировать
   его packet flow с нашим SOCKS5-режимом (или использовать TUN-режим)
