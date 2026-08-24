# Техническое задание: DualVPN

## Цель
Приложение для Windows, позволяющее одновременно подключаться к двум Cisco AnyConnect VPN эндпоинтам. Два режима работы: TUN (с админ-правами) и SOCKS5 (без админ-прав). UI для конфигурации, выбора группы, ввода 2FA.

## Эндпоинты

Конкретные адреса, алиасы групп и внутренние зоны в репозитории не фиксируются:
они задаются пользователем в `config.toml`. Ниже — требования к любому
поддерживаемому эндпоинту; в примерах плейсхолдеры `vpn1.example.com` /
`vpn2.example.com`.

### Требования к эндпоинту
- Тип: Cisco ASA или ocserv, AnyConnect SSL/TLS
- SAML/SSO: не поддерживается
- Группы (tunnel-groups): список запрашивается у самого сервера при подключении
  (`dualvpn-harness -groups` или кнопка «↻ с сервера» в UI); в конфиге пишется
  алиас ровно в том виде, в каком его отдал сервер
- 2FA: TOTP (6 цифр), если сервер выдал challenge — по имени группы это не угадать
- CSRF token в форме логина
- Сервер может ответить редиректом на другой адрес — клиент за ним следует

## Архитектура

### Технологии
- **Язык**: Go 1.24
- **VPN-протокол**: OpenConnect v9.12 (AnyConnect SSL/TLS)
- **UI**: Wails v3 (Go + HTML/CSS/JS, системный трей)
- **TUN (Windows)**: wintun.dll (WireGuard TUN driver)
- **TUN (Linux)**: /dev/net/tun
- **SOCKS5**: gVisor netstack (userspace TCP/IP)
- **Конфиг**: TOML

### Два режима

#### SOCKS5-режим (без админ-прав)
- gVisor netstack — полноценный TCP/IP стек в userspace
- Каждый туннель поднимает SOCKS5-прокси на локальном порту
- Приложения настраивают SOCKS5-прокси вручную
- Не нужен TUN-драйвер, не нужны админ-права
- Ограничения: только TCP/UDP (нет ICMP/ping)

#### TUN-режим (с админ-правами)
- wintun.dll (Windows) / /dev/net/tun (Linux) — ядерный TUN-адаптер
- Каждый туннель создаёт свой TUN-адаптер
- Split-tunneling: route add для指定 подсетей через конкретный TUN
- Прозрачно для всех приложений
- Поддержка ICMP, произвольный L3-трафик

#### Авто-детекция
- При запуске проверяет наличие админ-прав
- admin → TUN, no-admin → SOCKS5
- Авто-fallback TUN→SOCKS5 при ошибке инициализации
- Ручное переключение в UI

## Протокол подключения (AnyConnect)

1. TLS handshake к VPN-серверу (порт 443)
2. HTTP POST form-data: username, password, group_list, csrf_token
3. Если группа требует 2FA — сервер запрашивает вторичный код
4. Получение cookie webvpn=... (сессия)
5. Установка VPN-туннеля (TLS tunnel, опционально DTLS)
6. Приём/отправка IP-пакетов через туннель

## UI (выбранный дизайн — Sidebar)

Тёмная тема, layout:
- **Слева**: сайдбар со списком туннелей (VPN-1, VPN-2) + кнопка "Добавить туннель" + раздел "Глобальные" (Настройки, Статистика, Логи)
- **Справа**: детали выбранного туннеля:
  - Карточка "Параметры подключения": VPN сервер, группа (dropdown), логин, пароль, 2FA код, SOCKS5 порт, TUN имя
  - Карточка "Маршруты": чипы с подсетями (split-tunnel), кнопка "+ добавить"
  - Карточка "Статистика": RX, TX, время подключения, режим (TUN/SOCKS5)
  - Карточка "Журнал": цветной лог (INFO/WARN/ERROR/OK)
- **Шапка**: лого "DualVPN", badge режима (TUN·Admin / SOCKS5·No-Admin), кнопки "Сменить режим", "Подключить все"
- **Подвал**: версия, путь к config, путь к log
- **Системный трей**: иконка, контекстное меню (Подключить/Отключить/Выход)

### Цветовая схема (тёмная)
- Background: #0f1117 / #131620 / #161922
- Border: #25282f
- Text: #e5e7eb / #9ca3af / #6b7280
- Accent: #6366f1 (indigo) / #818cf8
- Status: #22c55e (connected), #f59e0b (connecting), #6b7280 (disconnected)
- Danger: #dc2626 / #ef4444

## Конфигурация (TOML)

```toml
[mode]
preferred = "auto"  # auto | tun | socks5

[[tunnels]]
name = "VPN-1"
endpoint = "vpn1.example.com"
group = "Group-2FA"
socks_port = 1080
tun_name = "vpn1"
routes = ["192.168.10.0/24", "10.10.0.0/16"]

[[tunnels]]
name = "VPN-2"
endpoint = "vpn2.example.com"
group = "RA"
socks_port = 1081
tun_name = "vpn2"
routes = ["192.168.20.0/24", "10.20.0.0/16"]
```

## План реализации

### Этап 1: Прототип (Linux)
- OpenConnect + ocproxy — проверка, что оба сервера пускают без TUN
- Интерактивный режим: openconnect --usergroup=... --servercert=... vpn_endpoint
- Тест с реальными кредами (запрос 2FA)

### Этап 2: Go-каркас
- Структура проекта, go.mod
- Config loader (TOML)
- OpenConnect-обёртка на Go (через exec или CGO биндинги)
- Два параллельных туннеля

### Этап 3: SOCKS5-режим
- gVisor netstack integration
- SOCKS5-сервер (armon/go-socks5)
- Связь: SOCKS5 → gVisor → OpenConnect tunnel

### Этап 4: TUN-режим
- wintun.dll загрузка (Windows)
- /dev/net/tun (Linux)
- Route table management
- Split-tunneling per-tunnel

### Этап 5: UI (Wails v3)
- Sidebar layout (как в макете 002-sidebar-detail)
- Формы конфигурации
- 2FA модальный диалог
- Системный трей
- Статистика, логи

### Этап 6: Тесты
- Юнит-тесты config, socks5, routing
- Интеграционные тесты с реальными серверами
- Тест авто-детекции режима
