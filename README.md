# DualVPN

Приложение для одновременного подключения к двум Cisco AnyConnect VPN эндпоинтам.

## Архитектура

- **Язык**: Go
- **VPN-протокол**: OpenConnect (AnyConnect SSL/TLS)
- **Два режима**: TUN (нужны админ-права) и SOCKS5 (без админа)
- **UI**: Wails v3 (Go backend + HTML/CSS/JS frontend, системный трей)

## Эндпоинты

1. **VPN-1**: `vpn1.example.com` — группы: Group-2FA, Group-Partners-2FA, VPN-1, Partners, Group-Ext
2. **VPN-2**: `vpn2.example.com` — группы: RA, RA-MFA, RA-Full

Оба — Cisco ASA, AnyConnect SSL/TLS, без SAML/SSO.
При подключении: выбор группы → логин → пароль → 2FA код (TOTP).

## Режимы работы

### SOCKS5 (без админских прав)
- gVisor netstack — userspace TCP/IP стек
- Каждый туннель поднимает локальный SOCKS5-прокси (1080, 1081)
- Приложения маршрутизируются через прокси вручную
- Не нужен TUN-драйвер

### TUN (с админскими правами)
- wintun.dll (Windows) / /dev/net/tun (Linux)
- Каждый туннель создаёт свой TUN-адаптер
- Split-tunneling через route table
- Прозрачно для всех приложений

### Авто-детекция
- При запуске проверяет наличие админ-прав
- Если админ → TUN режим
- Если нет → SOCKS5 режим
- Авто-fallback TUN→SOCKS5 при ошибке

## Структура проекта

```
dualvpn/
├── main.go                 # Точка входа
├── go.mod
├── internal/
│   ├── config/             # TOML конфиг, загрузка/сохранение
│   ├── vpn/                # OpenConnect протокол, handshake, 2FA
│   ├── socks5/             # SOCKS5-сервер + gVisor netstack
│   ├── tun/                # TUN-адаптеры (wintun/tun)
│   ├── routing/            # Route table management
│   └── ui/                 # Wails frontend bindings
├── frontend/
│   ├── index.html          # UI (sidebar layout, тёмная тема)
│   ├── style.css
│   └── app.js
├── wintun.dll              # Windows TUN-драйвер (в комплекте)
└── config.example.toml     # Пример конфигурации
```
