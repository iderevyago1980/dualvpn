# DualVPN: Native Go (sslcon) Integration Plan

> **For Hermes:** Execute via Claude Code CLI with specialized agents. TDD discipline.

**Goal:** Заменить openconnect-subprocess на нативный Go-клиент (sslcon) — один бинарник без внешних зависимостей.

## Контекст

- **Библиотека**: tlslink/sslcon (MIT, 71⭐) — Go-реализация OpenConnect VPN Protocol
- **Spike**: VALIDATED — оба Cisco ASA приняли XML POST auth (InitAuth OK)
- **Коммит**: `164ea92d` (2024-11-11) — совместим с Go 1.24
- **Module path**: `sslcon` (не github.com/tlslink/sslcon) — нужен replace в go.mod
- **Ключевая проблема**: `auth.Prof`, `auth.Conn`, `auth.WebVpnCookie` — глобальные. Для 2 туннелей нужен форк или изоляция.

## Архитектура

### Вариант A: Форк sslcon с per-tunnel state (выбран)

Проблема: sslcon использует package-level глобалы. Решение: форкнуть 3 файла (auth.go, session.go, tunnel.go) в `internal/vpn/sslcon/`, заменив глобалы на поля структуры `Client`.

Преимущества:
- Один бинарник, без openconnect
- Multi-tunnel из коробки (каждый Client = свой туннель)
- Полный контроль над 2FA, событиями, ошибками

Недостатки:
- Нужно поддерживать форк (но sslcon стабилен, ~30 коммитов/год)
- SOCKS5-режим без форка tunnel.go — сложно

### Вариант B: sslcon для TUN + openconnect для SOCKS5

Оставить openconnect только для SOCKS5-режима (где нужен ocproxy). TUN-режим через sslcon.

Преимущества:
- Минимальный форк (только auth)
- ocproxy работает из коробки

Недостатки:
- Два движка (openconnect + sslcon)
- openconnect всё ещё нужен как внешняя зависимость

**Решение: Вариант A** — полный нативный клиент. Для SOCKS5-режима: интегрировать packet flow с нашим socks5.Server (gVisor) или оставить openconnect как fallback.

## Tasks

### Task 1: Форк sslcon auth — per-tunnel state

**Files:**
- Create: `internal/vpn/sslcon/auth.go` (форк из sslcon/auth/auth.go)
- Create: `internal/vpn/sslcon/profile.go` (Profile без глобалов)
- Create: `internal/vpn/sslcon/auth_test.go`
- Modify: `internal/vpn/sslcon/go.mod` (replace sslcon → наш форк)

**Ключевые изменения:**
- `type Client struct { Prof *Profile; Conn *tls.Conn; BufR *bufio.Reader; WebVpnCookie string }`
- `func NewClient(cfg ClientConfig) *Client` — конструктор
- `func (c *Client) InitAuth() error` — метод вместо функции
- `func (c *Client) PasswordAuth() error` — метод
- `func (c *Client) Close() error` — закрытие TLS
- Убрать все package-level переменные (Prof, Conn, BufR, WebVpnCookie, reqHeaders)
- reqHeaders → поле Client или константа

**Тесты:**
- `TestNewClient` — создание клиента с конфигом
- `TestClientInitAuth` — мок TLS server, проверка XML init
- `TestClientPasswordAuth` — мок auth reply, проверка cookie

### Task 2: Форк sslcon session — per-tunnel state

**Files:**
- Create: `internal/vpn/sslcon/session.go` (форк из sslcon/session/session.go)

**Ключевые изменения:**
- `type Session struct { CSess *ConnSession; ActiveClose bool }`
- `func (s *Session) Close()` — корректное закрытие
- Убрать package-level `Sess`

### Task 3: Форк sslcon vpn/tunnel — packet flow

**Files:**
- Create: `internal/vpn/sslcon/tunnel.go` (форк из sslcon/vpn/tunnel.go)
- Create: `internal/vpn/sslcon/tunnel_test.go`

**Ключевые изменения:**
- `func (c *Client) SetupTunnel() error` — метод Client
- `func (c *Client) ReadPacket() ([]byte, error)` — чтение IP-пакетов
- `func (c *Client) WritePacket([]byte) error` — запись IP-пакетов
- Для TUN: `func (c *Client) SetupTUN(name string) (*tun.Device, error)`
- Для SOCKS5: `func (c *Client) PacketFlow() (<-chan []byte, chan<- []byte)` — каналы для интеграции с socks5.Server

### Task 4: Интеграция с manager.go

**Files:**
- Modify: `internal/vpn/manager.go` — заменить openconnect.Client на sslcon.Client
- Modify: `internal/vpn/manager_test.go`

**Ключевые изменения:**
- `type TunnelConfig struct { ID string; Opts sslcon.ClientConfig; Routes []string }`
- Manager создаёт `sslcon.NewClient(opts)` вместо `openconnect.NewClient(opts)`
- События: `sslcon.Client` должен эмитить те же типы (connected/disconnected/error/2fa_required)
- 2FA: `sslcon.Client` должен иметь канал для 2FA-запросов

### Task 5: UI интеграция — 2FA, статусы

**Files:**
- Modify: `internal/ui/app.go` — обновить для sslcon.Client
- Modify: `frontend/app.js` — 2FA modal должен работать с sslcon

**Ключевые изменения:**
- `Submit2FA` → передача кода в sslcon.Client
- События `2fa_required` → показать модалку
- Статусы: connected/disconnected/error → обновить UI

### Task 6: Сборка и тесты

**Files:**
- Modify: `go.mod` — replace sslcon, remove openconnect
- Modify: `Makefile` — убрать openconnect check
- Modify: `config.example.toml` — обновить комментарии

**Проверки:**
- `go build ./...` — без openconnect
- `go test ./internal/...` — все тесты PASS
- `make build-windows` — .exe собирается
- `make build-linux` — .bin собирается

## Порядок выполнения

1. Task 1 (auth форк) — критично, базовый слой
2. Task 2 (session форк) — зависит от Task 1
3. Task 3 (tunnel форк) — зависит от Task 1-2
4. Task 4 (manager интеграция) — зависит от Task 1-3
5. Task 5 (UI интеграция) — зависит от Task 4
6. Task 6 (сборка) — финальная проверка

## Риски

| Риск | Вероятность | Митигация |
|---|---|---|
| sslcon не работает с реальным PasswordAuth | Средняя | Spike показал InitAuth OK; PasswordAuth — следующий тест |
| 2FA не маппится на наши события | Средняя | Обёртка вокруг auth.PasswordAuth() с каналом событий |
| Форк ломается при обновлении sslcon | Низкая | sslcon стабилен, коммиты редки; pinned commit |
| SOCKS5-режим без ocproxy не работает | Высокая | Оставить openconnect fallback или интегрировать gVisor |

## Success Criteria

- `go build ./...` без openconnect — PASS
- `go test ./internal/...` — все тесты PASS
- `make build-windows` — DualVPN.exe собирается
- `make build-linux` — dualvpn-linux собирается
- `go run . -connect` — подключение к тестовому серверу (нужны креды)
