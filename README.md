# DualVPN

Приложение для одновременного подключения к двум Cisco AnyConnect VPN эндпоинтам.

## Архитектура

- **Язык**: Go
- **VPN-протокол**: Cisco AnyConnect (SSL/TLS + DTLS) — собственный клиент на Go
  (форк [sslcon](https://github.com/tlslink/sslcon)); внешний бинарь `openconnect`
  **не** используется
- **Два режима**: TUN (нужны админ-права) и SOCKS5 (без админа)
- **UI**: Wails v2 (Go backend + HTML/CSS/JS frontend, системный трей)

## Эндпоинты

1. **VPN-1**: `vpn1.example.com`
2. **VPN-2**: `vpn2.example.com`

Оба — Cisco ASA, AnyConnect SSL/TLS, без SAML/SSO.
При подключении: выбор группы → логин → пароль → 2FA-код (TOTP), если сервер
его запросит.

Имя группы должно **буквально** совпадать с алиасом на сервере. Список групп
не зашит в приложение — он запрашивается у самого сервера: кнопка
«↻ с сервера» рядом с полем группы, либо `dualvpn-harness -groups`. Пустая
группа означает «использовать группу сервера по умолчанию».

## Режимы работы

### SOCKS5 (без админских прав)
- gVisor netstack — userspace TCP/IP стек
- Каждый туннель поднимает локальный SOCKS5-прокси (1080, 1081)
- Приложения маршрутизируются через прокси вручную
- Не нужен TUN-драйвер
- Имена разрешаются DNS-серверами шлюза **внутри туннеля**; зоны из
  split-DNS не уходят в системный резолвер

### TUN (с админскими правами)
- wintun.dll (Windows) / /dev/net/tun (Linux)
- Каждый туннель создаёт свой TUN-адаптер
- Split-tunneling через route table
- Прозрачно для всех приложений

### Авто-детекция
- При запуске проверяет наличие админ-прав
- Если админ → TUN режим
- Если нет → SOCKS5 режим
- Режим можно сменить вручную в UI; смена останавливает все туннели.
  Автоматического отката TUN→SOCKS5 при ошибке нет

## Структура проекта

```
dualvpn/
├── main.go                 # Точка входа (встраивает frontend и config.example.toml)
├── go.mod
├── internal/
│   ├── config/             # TOML конфиг, загрузка/сохранение
│   ├── vpn/                # Менеджер туннелей
│   │   └── sslcon/         # Клиент AnyConnect: auth, 2FA, CSTP/DTLS
│   ├── socks5/             # SOCKS5-сервер + gVisor netstack + DNS туннеля
│   ├── tun/                # TUN-адаптеры (wintun/tun)
│   ├── routing/            # Маршруты split-tunnel (netsh/route)
│   ├── mockasa/            # Эмулятор Cisco ASA для тестов
│   └── ui/                 # Wails frontend bindings
├── cmd/
│   ├── dualvpn-harness/    # Headless-драйвер: подключение, DNS, группы
│   └── dualvpn-tuncheck/   # Самопроверка TUN-пути (нужны админ-права)
├── frontend/
│   ├── index.html          # UI (sidebar layout, тёмная тема)
│   ├── style.css
│   └── app.js
└── config.example.toml     # Шаблон конфигурации (встраивается в бинарь)
```

`wintun.dll` в репозиторий не коммитится: его скачивает `make wintun`
(`build/windows/fetch-wintun.sh`) и кладёт рядом с exe — драйвер грузится
только из каталога программы или System32.

## Сборка и запуск на Windows

```bash
go build -tags desktop,production -ldflags "-H=windowsgui -s -w" -o bin/DualVPN.exe .
```

`wintun.dll` нужен только для TUN-режима; SOCKS5 работает без него и без
прав администратора.

**Smart App Control.** На Windows 11 с включённым Smart App Control
неподписанные сборки блокируются по репутации файла («заблокирован политикой
Device Guard»). Для разработки помогает `go run ./cmd/...`, для раздачи
пользователям нужна подпись сертификатом.

### Подпись сборок

```powershell
build\windows\sign.ps1 -CreateSelfSigned -ExportCer bin\DualVPN-selfsigned.cer  # первый раз
build\windows\sign.ps1 -Thumbprint <отпечаток>                                  # дальше
```

signtool.exe из Windows SDK не нужен — подписывает встроенная
`Set-AuthenticodeSignature`. Метка времени ставится всегда: без неё подпись
перестаёт считаться действительной в день истечения сертификата.

Релизы подписаны **самоподписанным** сертификатом
(`CN=DualVPN (self-signed)`, отпечаток
`A97246B76B693EADE8DA3B99193C37F680FB53CB`). Windows не знает этот корень,
поэтому подпись считается действительной только там, где сертификат
установлен вручную:

```powershell
Import-Certificate -FilePath .\DualVPN-selfsigned.cer -CertStoreLocation Cert:\CurrentUser\Root
Import-Certificate -FilePath .\DualVPN-selfsigned.cer -CertStoreLocation Cert:\CurrentUser\TrustedPublisher
```

Это осмысленно для развёртывания внутри организации (в т.ч. через GPO) и
бесполезно для публичной раздачи: Smart App Control и SmartScreen смотрят на
репутацию издателя, а не на факт наличия подписи, и блокируют файл даже с
формально действительной подписью. Чтобы предупреждения исчезли у
посторонних, нужен коммерческий OV/EV-сертификат (с 2023 — только с
аппаратным токеном или облачным HSM) либо Azure Trusted Signing.

## Диагностика без GUI

```bash
go run ./cmd/dualvpn-harness -config config.toml -groups                 # какие группы предлагает сервер
go run ./cmd/dualvpn-harness -config config.toml -otp 123456 -hold 30s   # поднять туннели и подержать
go run ./cmd/dualvpn-harness -config config.toml -resolve host.corp.example   # проверить DNS внутри туннеля
go build -o bin/dualvpn-tuncheck.exe ./cmd/dualvpn-tuncheck              # TUN: адаптер + маршруты (от админа)
```
