# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Проект
DualVPN — Go-приложение для **одновременного** подключения к двум Cisco AnyConnect (SSL/TLS) VPN-эндпоинтам. Два режима: TUN (нужны админ-права) и SOCKS5 (без прав). Desktop-UI на Wails v2 + системный трей.

## Команды

Go **не** в PATH по умолчанию и требует тег сборки — всегда экспортируй окружение:

```bash
export PATH="/usr/local/go/bin:$PATH" GOFLAGS="-tags=webkit2_41"
go build ./...              # сборка
go vet ./...                # статанализ
go test ./... -race -count=3 # тесты (см. ниже про -race)
```

- **Тег `webkit2_41` обязателен на Linux**: в системе только `libwebkit2gtk-4.1`, без тега Wails ищет несуществующий 4.0 и сборка падает.
- **Один тест**: `go test ./internal/mockasa/ -run TestDualTunnelsSimultaneous -v -timeout 45s`. Всегда ставь `-timeout` на тесты `mockasa` — при зависании они иначе висят до общего таймаута.
- **`-race -count=3`**: гонки в многотуннельном коде проявляются только под нагрузкой (два туннеля + параллельные пакеты). Одиночный прогон их прячет. Прогоняй ≥3 итераций перед коммитом.
- Лог-уровень форкнутого sslcon по умолчанию `Debug` — тесты сыплют XML в stderr; фильтруй вывод при чтении.

Сборка бинарников (Makefile):
```bash
make build-linux     # bin/dualvpn-linux (нужны libwebkit2gtk-4.1-dev, libayatana-appindicator3-dev)
make build-windows   # bin/DualVPN.exe — кросс-компиляция, CGO_ENABLED=0, -H=windowsgui
make installer       # bin/DualVPN-Setup-<VERSION>.exe — NSIS-инсталлятор (makensis, кросс-сборка на Linux)
make build           # полноценная Wails-сборка под Windows (wails build)
```
**Не запускай `wails dev` / `make dev`** — на сервере нет GUI. Инсталлятор — per-user (`%LOCALAPPDATA%`), без прав администратора; кладёт `wintun.dll` рядом с exe. Версия задаётся `make installer VERSION=x.y.z`.

- Go 1.26.5 в `/usr/local/go` (в go.mod — `go 1.26.3`).

## Архитектура

Поток управления сверху вниз: **`ui.App` (Wails-биндинги) → `vpn.Manager` (координатор туннелей) → `sslcon.Client` (один туннель: auth + CSTP) → `sslcon.Tunnel` (packet flow)**.

### Ключевое решение: форк sslcon с per-tunnel состоянием
`internal/vpn/sslcon/` — форк `github.com/tlslink/sslcon`, где package-level **глобалы заменены на поля структуры `Client`** (`Prof`, `Conn`, `WebVpnCookie`, сессия, каналы пакетов). Это то, что вообще делает возможными два одновременных туннеля — оригинал с глобалами их не поддерживает. `session.go`/`tunnel.go`/`auth.go` — форки соответствующих файлов sslcon с той же логикой, но на состоянии экземпляра.

⚠️ **Остаточные процесс-глобалы**: пакеты `sslcon/base` и `sslcon/utils` (vendored, не форкнуты) держат общий `base.Cfg` / `base.LocalInterface`. `ensureBase()` (`auth.go`) инициализирует их один раз под `sync.Once` и гасит `CiscoCompat`, иначе `utils.SetCommonHeader` переписывает глобал на каждый запрос → гонка при нескольких туннелях. Любой новый код, трогающий `base.*`, должен помнить: это разделяемое состояние на весь процесс.

### Два режима — расходятся в `sslcon.Client.run()`
- **TUN** (нужны админ-права): `Tunnel.SetupTUN()` создаёт адаптер через `internal/tun` (Linux `/dev/net/tun` + `ip addr/link`; Windows — драйвер Wintun + `netsh`, требует `wintun.dll` рядом с exe), назначает адрес/MTU и применяет маршруты split-include через `internal/routing`. `internal/tun/Device` прячет платформенный ввод-вывод за `io.ReadWriteCloser`. Имя интерфейса приходит из `ClientConfig.TunName` (пустое → `dualvpnN`).
- **SOCKS5**: `Manager.Start` подставляет хук `Client.TunnelSetup`, который поднимает `socks5.Bridge` — **gVisor netstack (userspace TCP/IP) поверх `Tunnel.PacketFlow()`**. SOCKS5-клиенты коннектятся на локальный порт, `dialVPN` открывает соединения внутри netstack → трафик уходит в туннель. Без TUN-драйвера и админ-прав. `internal/socks5/server.go` — тонкий фронтенд `armon/go-socks5`.

Режим выбирает `internal/mode.Detect()` (админ → TUN, иначе SOCKS5); `SwitchMode` останавливает все туннели — сменить режим «на лету» нельзя.

### Аутентификация и 2FA (`sslcon/auth.go`)
`InitAuth` (TLS + список групп) → `PasswordAuth`. При запросе второго фактора `PasswordAuth` возвращает `ErrNeeds2FA`; `run()` эмитит `Event2FARequired` и **блокируется на канале `twoFAOK`**, пока `Submit2FA(code)` (вызванная из UI/менеджера) не пройдёт. Код 2FA (TOTP) идёт в `<password>` challenge-формы, как у настоящего AnyConnect.

### События
`Client.Events()` (создаётся в `NewClient`, единственный потребитель — `Manager.forwardEvents`) → `Manager` агрегирует в `ManagerEvent{TunnelID, Event}` → `ui.App.forwardEvents` → Wails `EventsEmit("tunnel:event" / "tunnel:2fa" / "log")` → фронтенд `window.runtime.EventsOn`. Методы `App` доступны JS как `window.go.ui.App.<Метод>`.

### Тестовый эмулятор (`internal/mockasa`)
Мок Cisco ASA/AnyConnect для интеграционных тестов **без реальных серверов**: aggregate auth + 2FA-challenge + CSTP-туннель (STF-фреймы, DPD) + «внутренняя сеть» за шлюзом на gVisor netstack (echo/HTTP-сервисы). Тесты гоняют весь путь `auth → 2FA → CSTP → SOCKS5-мост → хост внутренней сети → эхо`, в т.ч. два туннеля одновременно с проверкой изоляции. Это основной способ проверять логику подключения локально.

### Легаси
`internal/vpn/openconnect.go` — **мёртвый код**: обёртка над бинарём `openconnect` + `ocproxy`, оставшаяся от первой итерации. Активный путь целиком нативный (sslcon), внешний `openconnect` не запускается. README.md/SPEC.md местами устарели (пишут «OpenConnect», «Wails v3») — верно: нативный Go-клиент, Wails **v2**.

## Эндпоинты (проверенные)
1. `vpn2.astralinux.ru` — Cisco ASA. Группы: `Basic 2FA` (Astra2FA), `Astra2FA Partners`, `Basic` (AstraLinux), `Partners Astralinux`, `AstraLinuxExt`. 2FA (TOTP) — для групп с «2FA» в названии.
2. `vpn.mt-integration.ru` — Cisco ASA. Группы: `MT-I_RA`, `MT-I_RA_MFA` (2FA), `MT-I_RA_no_split`.

Оба без SAML/SSO, CSRF-токен в форме логина.

## Конвенции
- Комментарии и сообщения — на русском (как во всём коде).
- Коммиты `sslcon`-форка сохраняют логику оригинала; при апдейте зависимости сверять с upstream.
- Осторожно с коммитами, у которых автор `Hermes Agent` — среди них был сгенерированный нерабочий код; проверяй, что сборка проходит.
