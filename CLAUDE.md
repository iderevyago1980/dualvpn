# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Проект
DualVPN — Go-приложение для **одновременного** подключения к двум Cisco AnyConnect (SSL/TLS) VPN-эндпоинтам. Два режима: TUN (нужны админ-права) и SOCKS5 (без прав). Desktop-UI на Wails v2 + системный трей.

## Команды

### Windows (нативно)

Go ставится в `C:\Program Files\Go`; в уже открытой сессии bash его нужно добавить в PATH вручную:

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build -tags desktop,production -ldflags "-H=windowsgui -s -w -X dualvpn/internal/ui.version=$VERSION" -o bin/DualVPN.exe .
go test ./internal/... ./test/... -count=3
```

- Тег `webkit2_41` — **только** для Linux; на Windows он не нужен.
- **`-race` на Windows недоступен** без C-компилятора (`cgo: C compiler "gcc" not found`). Гонки ловятся на Linux; здесь ограничивайся `-count=3`.
- `wintun.dll` должен лежать **рядом с exe**: он грузится через `LoadLibraryEx` с флагами `SEARCH_APPLICATION_DIR|SEARCH_SYSTEM32`, поэтому ни PATH, ни рабочий каталог не помогут. Отсюда же вывод: `go test` не может проверить TUN — тестовый бинарь лежит во временном каталоге. Для проверки есть `cmd/dualvpn-tuncheck` (собирается в `bin/`, запускать от администратора).
- **Smart App Control** (`HKLM:\SYSTEM\CurrentControlSet\Control\CI\Policy\VerifiedAndReputablePolicyState = 1`) блокирует неподписанные exe по репутации: запуск падает с «заблокирован политикой Device Guard», в журнале `Microsoft-Windows-CodeIntegrity/Operational` появляется событие 3077. Особенно достаётся файлам из `%TEMP%`, поэтому **`go test` падает случайным образом** с `fork/exec ...: An Application Control policy has blocked this file` при полностью рабочем коде — не принимай это за дефект. Надёжный прогон: `test/run-windows.sh` (компилирует тесты в `bin/tests` и запускает из каталога пакета). Для раздачи пользователям нужна подпись сертификатом; отключение Smart App Control необратимо (обратно — только переустановкой Windows).
- **Подпись**: `build/windows/sign.ps1` (`Set-AuthenticodeSignature`, signtool из SDK не нужен). Релизы подписаны самоподписанным сертификатом — он даёт доверенную подпись только там, где установлен вручную или через GPO, и **не снимает блокировку Smart App Control/SmartScreen**: те смотрят на репутацию издателя. Публичная раздача без предупреждений требует OV/EV-сертификата (с 2023 — аппаратный токен или облачный HSM) либо Azure Trusted Signing.

### Linux

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
make deb             # bin/dualvpn_<VERSION>_amd64.deb — пакет для Debian/Ubuntu
make build-windows   # bin/DualVPN.exe — кросс-компиляция, CGO_ENABLED=0, -H=windowsgui
make installer       # bin/DualVPN-Setup-<VERSION>.exe — NSIS-инсталлятор (makensis, кросс-сборка на Linux)
make build           # полноценная Wails-сборка под Windows (wails build)
```
**Не запускай `wails dev` / `make dev`** — на сервере нет GUI. Инсталлятор Windows — per-user (`%LOCALAPPDATA%`), без прав администратора; кладёт `wintun.dll` рядом с exe. Версия задаётся `make installer VERSION=x.y.z` (та же переменная у `make deb`).

**`.deb` (Debian/Ubuntu)**: `make deb` собирает `build-linux` и пакует бинарь в `/usr/bin/dualvpn` + `.desktop`-ярлык (`build/linux/dualvpn.desktop`) + иконку (`build/linux/dualvpn.svg`). Секция `Depends` (шаблон `build/linux/control.in`) объявляет `libwebkit2gtk-4.1-0`, `libgtk-3-0t64|libgtk-3-0`, `libayatana-appindicator3-1` — **без них голый бинарь на чистой системе молча не стартует** (`cannot open shared object file`); это была одна из причин «ничего не запускается». Собирается на Linux через `dpkg-deb --root-owner-group` (root не нужен).

**Стартовый конфиг**: эндпоинтов и групп в Go-коде нет — `config.Default()` пустой. При первом запуске `config.CreateFrom` разворачивает встроенный (`//go:embed config.example.toml` в `main.go`) шаблон байт-в-байт, вместе с комментариями. Захардкоженный ранее список в `Default()` разошёлся с реальностью и ломал подключение ещё до логина.

**Путь конфига** (`main.go: configPath`): приоритет `DUALVPN_CONFIG` → локальный `config.toml` в cwd (dev) → `config.DefaultPath()` = `~/.config/dualvpn/config.toml`. Раньше путь был жёстко относительным `"config.toml"`, и при запуске из меню (cwd = `/`) запись падала → `Fatalf` → приложение не стартовало. При запуске из репозитория поведение прежнее (подхватывается `./config.toml`).

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

### Протокол Cisco ASA: чем живой шлюз отличается от ocserv и мока

Всё, что ниже, проверено на `vpn1.example.com` и `vpn2.example.com` (2026-07-27). Мок и ocserv к этим деталям нетребовательны, поэтому e2e их **не ловил** — код проходил тесты и не работал ни с одним боевым сервером.

- **`<device-id>` обязан иметь тело** — платформенный токен (`win`, `linux-64`, `mac-intel`), поле `Profile.Platform`. С пустым телом ASA отвечает `<error id="96">VPN Server internal error.</error>` уже на `init`, до всякого логина. В upstream sslcon тело пустое.
- **Ответ на challenge 2FA повторяет ровно поля формы** (как строит запрос OpenConnect в `xmlpost_append_form_opts`):
  - код кладётся в `<password>`, даже если поле формы называется `answer` (см. `codeElement`) — `<answer>` ASA не понимает;
  - `<username>` шлётся, только если он есть в challenge-форме (обычно его нет);
  - `<group-select>` не шлётся вовсе — группа выбрана на первом шаге, повторный выбор ASA считает новой попыткой логина;
  - `<opaque>` возвращается дословно из challenge-ответа: в нём `<auth-handle>`, которым сервер связывает ответ с выданным challenge.
  
  Нарушение любого пункта даёт `<error id="15">Login failed.</error>` на **верный** код.
- **Группы в конфиге — это алиасы** из `<select name="group_list">`, а не имена tunnel-group. Совпадение должно быть буквальным. Список берётся с сервера (`sslcon.FetchGroups`, кнопка «↻ с сервера» в UI, `dualvpn-harness -groups`); пустая группа означает «использовать группу сервера по умолчанию».
- **Сообщения сервера — шаблоны с подстановками в атрибутах**: `<message id="2" param1="Введите OTP-код">%s</message>`. Подставляет `formatServerMessage`; безусловный `Sprintf` давал пользователю литеральное `%s` и `Authentication failed.%!(EXTRA string=)`.
- Двухфазный выбор группы (отдельный auth-reply только с `<group-select>`) эта ASA **не** поддерживает — отвечает `Login error`. Группа и учётные данные уходят одним запросом.

### Как трафик попадает в туннель

- **TUN** — единственный способ увести в туннель трафик *всех* приложений: подсети split-include маршрутизируются в адаптер. Требует админ-прав.
- **SOCKS5** — прокси уровня соединений: приложение должно само обратиться к прокси. Перехватить трафик по адресу назначения без TUN-адаптера нельзя (нужен драйвер-фильтр вроде WFP/WinDivert, что тоже означает админ-права).
- **PAC** (`internal/pac`) — мост между этими мирами для случая без админ-прав: приложение раздаёт `http://127.0.0.1:<pac.port>/proxy.pac`, скрипт направляет домен в **свой** туннель (по split-DNS) и литеральные адреса — по split-include; остальное DIRECT. Правила строятся из данных, полученных от шлюза, и обновляются при подключении/отключении. `dnsResolve` в скрипте намеренно не используется: он резолвил бы имя системным DNS — ровно то, что для корпоративных зон не работает.

### DNS

Шлюз выдаёт `X-CSTP-DNS`, `X-CSTP-Split-DNS`, `X-CSTP-Tunnel-All-DNS` (разбираются в `session.go`).

- **TUN + два туннеля**: назначить DNS интерфейсам недостаточно — Windows опрашивает серверы по метрике интерфейса, и зоны одной сети уходят в DNS другой. Поэтому `internal/nrpt` заводит правила NRPT (`Add-DnsClientNrptRule`, зона → серверы туннеля) и снимает их при закрытии туннеля; правила помечаются комментарием `DualVPN:<интерфейс>`, по нему же удаляются.

- **SOCKS5**: `internal/socks5/resolver.go` шлёт запросы на DNS шлюза **через тот же netstack** (UDP, откат на TCP при усечении). Имена из зон split-DNS в системный резолвер не уходят даже при ошибке — это утечка имени и заведомо неверный ответ. Прежде имена разрешались `net.DefaultResolver`, то есть публичным DNS, и внутренние ресурсы были недоступны при «успешном» подключении.
- **TUN**: DNS-серверы назначаются адаптеру через `netsh ... set dnsservers ... validate=no` (`tun_windows.go`).
- Диагностика: `dualvpn-harness -resolve имя1,имя2` печатает адрес, источник (`dns-vpn` / `dns-система`) и серверы туннеля.

### Аутентификация и 2FA (`sslcon/auth.go`)
`InitAuth` (TLS + список групп) → `PasswordAuth`. При запросе второго фактора `PasswordAuth` возвращает `ErrNeeds2FA`; `run()` эмитит `Event2FARequired` и **блокируется на канале `twoFAOK`**, пока `Submit2FA(code)` (вызванная из UI/менеджера) не пройдёт. Код 2FA (TOTP) идёт в `<password>` challenge-формы, как у настоящего AnyConnect.

### События
`Client.Events()` (создаётся в `NewClient`, единственный потребитель — `Manager.forwardEvents`) → `Manager` агрегирует в `ManagerEvent{TunnelID, Event}` → `ui.App.forwardEvents` → Wails `EventsEmit("tunnel:event" / "tunnel:2fa" / "log")` → фронтенд `window.runtime.EventsOn`. Методы `App` доступны JS как `window.go.ui.App.<Метод>`.

### Тестовый эмулятор (`internal/mockasa`)
Мок Cisco ASA/AnyConnect для интеграционных тестов **без реальных серверов**: aggregate auth + 2FA-challenge + CSTP-туннель (STF-фреймы, DPD) + «внутренняя сеть» за шлюзом на gVisor netstack (echo/HTTP-сервисы). Тесты гоняют весь путь `auth → 2FA → CSTP → SOCKS5-мост → хост внутренней сети → эхо`, в т.ч. два туннеля одновременно с проверкой изоляции. Это основной способ проверять логику подключения локально.

### Легаси
`internal/vpn/openconnect.go` — **мёртвый код**: обёртка над бинарём `openconnect` + `ocproxy`, оставшаяся от первой итерации. Активный путь целиком нативный (sslcon), внешний `openconnect` не запускается. README.md/SPEC.md местами устарели (пишут «OpenConnect», «Wails v3») — верно: нативный Go-клиент, Wails **v2**.

## Эндпоинты (проверены подключением 2026-07-27)

Списки групп получены с самих серверов (`dualvpn-harness -groups`) — это **алиасы**, значение в конфиге должно совпадать с ними буквально.

1. `vpn1.example.com` — Cisco ASA. Группы: `Group-2FA`, `Group-Partners-2FA`, `VPN-1`, `Partners`, `Group-Ext`. Второй фактор (OTP) запрашивается и для `VPN-1`, то есть «2FA» в названии — не признак. DNS туннеля: `10.0.0.12`, `10.0.0.13`.
2. `vpn2.example.com` — Cisco ASA 9.22(3)5. Группы: `Remote Access`, `Remote Access MFA`, `Remote Access Full`. Базовая группа — без второго фактора. DNS туннеля: `10.0.0.11`, split-DNS: `intranet.example`, `corp.example`, `corp-tech.example`, `vpn2-lab.example`, `example.com`.

Прежние списки в этом файле (`Group-2FA`, `RA`, …) не существовали ни на одном сервере — это были имена tunnel-group, а не алиасы.

Оба без SAML/SSO, CSRF-токен в форме логина.

## Конвенции
- Комментарии и сообщения — на русском (как во всём коде).
- Коммиты `sslcon`-форка сохраняют логику оригинала; при апдейте зависимости сверять с upstream.
- Осторожно с коммитами, у которых автор `Hermes Agent` — среди них был сгенерированный нерабочий код; проверяй, что сборка проходит.
