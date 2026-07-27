# DualVPN E2E-стенд: два туннеля против открытых AnyConnect-серверов

**Дата:** 2026-07-24
**Статус:** дизайн утверждён, готов к плану реализации

## Цель

Собрать **переиспользуемый** сетевой стенд, который проверяет DualVPN на реальном
сетевом пути (не in-process), поднимая два одновременных туннеля к двум
AnyConnect-совместимым серверам, и подтверждает:

1. оба туннеля живут одновременно, обе внутренние сети доступны из клиента;
2. трафик туннелей **изолирован** на уровне инфраструктуры, а не только маршрутов;
3. работают оба режима — **TUN** (админ, реальные split-маршруты ОС) и **SOCKS5** (без прав);
4. `.deb`/NSIS-пакеты стартуют на **чистой** системе (проверка зависимостей);
5. клиент работает и на **Linux**, и на **Windows**.

Стенд также служит инструментом для вскрытия **расхождений совместимости** между
форком `sslcon` (заточен под Cisco ASA) и открытыми серверами.

## Контекст: чем эмулируется серверная сторона AnyConnect

Полностью открытого «верного» эмулятора Cisco ASA нет. Реальные варианты
(по возрастанию верности протоколу и стоимости):

| Бэкенд | Верность ASA | Открытость | Роль на стенде |
|---|---|---|---|
| `internal/mockasa` (в репо) | Точная: aggregate-auth XML + группы + CSRF + 2FA, писался под DualVPN | свой код | tier-0 эталон «идеального ASA» |
| **ocserv** | Совместим по CSTP; расходится с ASA в выборе группы / XML логина (историч. 404 на group-URL, чинилось патчами «как Cisco ASA») | open source | основной открытый живой демон |
| Cisco ASAv (KVM) | Максимальная (это настоящий ASA) | закрытый, нужна лицензия | опциональный слой |
| Реальные эндпоинты (`vpn2.astralinux.ru`…) | Настоящий AnyConnect | — | `remote`-бэкенд при наличии доступа |

**Следствие для дизайна:** серверный бэкенд делаем **подключаемым**, не завязываясь
на один ocserv. `mockasa` даёт заведомо-сходящийся эталон; ocserv — реальную
проверку; asav/remote — опциональные слои поверх той же абстракции.

## Архитектура

### Топология

```
  ХОСТ (Linux, docker + qemu/kvm)

  docker (бэкенд ocserv):
    net «inner-a» (internal)           net «inner-b» (internal)
      [ocserv-A] .90.0.1  ─ [whoami-A]   [ocserv-B] .91.0.1 ─ [whoami-B]
        │  192.168.90.10                    │  192.168.91.10
        └── также на lab-bridge             └── также на lab-bridge
             10.20.0.11 :443                      10.20.0.12 :443

  lab-bridge  br-dualvpn 10.20.0.0/24  (L2, соединяет контейнеры и VM)
        │                         │
   tap0 │                    tap1 │
  ┌─────┴───────────┐      ┌──────┴──────────────┐
  │ Linux client VM │      │ Windows client VM   │
  │ Ubuntu + .deb   │      │ Win Eval + NSIS     │
  │ harness (TUN/   │      │ harness (Wintun/    │
  │ SOCKS5), Xvfb   │      │ SOCKS5)             │
  └─────────────────┘      └─────────────────────┘
```

### Ключевые сетевые решения

- **Уточнение API (после сверки с кодом).** `sslcon.ClientConfig.Host` **поддерживает
  `host:port`** (не только хост), и есть поле `InsecureSkipVerify`. Поэтому:
  - **Host-прогон ядра** (харнесс на хосте против контейнеров): серверы публикуются
    на `127.0.0.1:4443/4444`, харнесс идёт на `host:port` с `-insecure` — **без
    bridge/tap и без sudo для сети**. Это база для быстрого зелёного результата.
  - **VM-слой** (следующий план): клиентская VM подключается через `tap` к
    Linux-мосту `br-dualvpn`, к которому подключена docker lab-сеть; серверы видны
    по разным IP. `tap`/bridge — через `sudo` (passwordless есть). Порт при этом
    можно оставить нестандартным (Host поддерживает `:port`) или держать 443 — на
    усмотрение реализации VM-слоя.
  - `mockasa` уже является сетевым TLS-listener (`New()` → `Addr()`), поэтому
    mockasa-бэкенд запускается **в процессе теста** — весь путь проверяется чистым
    `go test` без docker/root/VM.
- **Изоляция на уровне L2.** Каждый ocserv сидит на своей `internal` docker-сети со
  своим whoami-хостом. ocserv-A физически не имеет интерфейса в сеть B →
  межтуннельная утечка невозможна на уровне инфраструктуры, а не только маршрутов.

### Компоненты (всё коммитится; скачиваемые образы/сертификаты — в `.gitignore`)

```
cmd/dualvpn-harness/         — headless-драйвер на vpn.Manager (Linux+Windows)
test/e2e/
  backends/
    ocserv/                  — docker-compose, ocserv.conf, whoami, генератор CA+сертов
    mockasa/                 — сетевой врапер над internal/mockasa (TLS-listener на 443)
    (asav/, remote/          — опциональные слои, задел)
  net/                       — скрипты br-dualvpn + tap + подключение docker-сети
  vm/
    linux/                   — cloud-init + qemu-запуск Ubuntu client VM
    windows/                 — autounattend.xml + qemu-запуск Win Eval + virtio
  checks/                    — общие проверки связности/изоляции (вызываются харнессом)
  run.sh                     — оркестрация цикла + teardown
Makefile: цель `e2e`
```

## Детали компонентов

### 1. `cmd/dualvpn-harness` — headless-драйвер

Маленький бинарь **без Wails**, использует существующий `vpn.Manager`:

1. читает `config.toml` (тот же формат, что у GUI) и флаг `-mode tun|socks5`;
2. `NewManager()` → `ReplaceTunnels(cfgs)` → `StartAll(ctx)`;
3. подписан на `Events()`, ждёт `EventConnected` по **обоим** ID с таймаутом;
4. запускает проверки (`test/e2e/checks`), печатает `PASS/FAIL` по каждой;
5. `StopAll()`, exit-код = число провалов (0 = успех).

Сборка: Linux — `go build -tags ...`; Windows — по пути `make build-windows`
(`CGO_ENABLED=0`), harness не тянет webkit, поэтому кросс-сборка тривиальна.

**Важно:** харнесс переиспользует боевой `vpn.Manager`/`sslcon` — то есть проверяет
именно ту логику подключения, что и GUI, а не её копию.

### 2. Проверки (`test/e2e/checks`)

`whoami`-хосты (`traefik/whoami`) возвращают `RemoteAddr` — это даёт сильный сигнал
изоляции: через какой IP клиент пришёл.

**TUN-режим:**
- `ip route` содержит `192.168.90.0/24 dev <tun0>` и `192.168.91.0/24 dev <tun1>`;
- `curl http://192.168.90.10` и `.91.10` → HTTP 200;
- `whoami-A` видит клиента как `10.90.0.x`, `whoami-B` — как `10.91.0.x`
  (разные пулы = разные туннели);
- пока поднят только туннель A, `192.168.91.10` **недоступен** (нет утечки).

**SOCKS5-режим:**
- `curl --socks5 127.0.0.1:1080 http://192.168.90.10` → 200;
- `curl --socks5 127.0.0.1:1081 http://192.168.91.10` → 200;
- кросс `--socks5 :1080 → .91.10` **падает** (netstack A не знает сеть B) — изоляция.

### 3. Серверные бэкенды

**ocserv** (`test/e2e/backends/ocserv/`): `docker-compose.yml` с двумя ocserv + двумя
whoami; `ocserv.conf` с password-auth (без 2FA), tunnel-group на inner-подсеть,
`no-route`-конфиг под split-include. Локальный CA + серверные серты с корректным
SAN (IP серверов); CA раскладывается в trust store гостя. Группы конфигурируются под
имена из `config.toml` стенда.

**mockasa** (`test/e2e/backends/mockasa/`): тонкий бинарь, поднимающий существующий
`internal/mockasa` как TLS-listener на 443 в контейнере/на мосту. Эталон: с ним
клиент обязан сходиться всегда; на нём валидируем сам стенд до ocserv.

**asav / remote:** только задел каталогов и переменная `E2E_BACKEND` — реализация вне
текущих рамок.

### 4. Клиентские VM

**Linux** (`vm/linux/`, реализуется отдельным планом): Ubuntu 24.04 cloud image +
QEMU/KVM (под `sudo` — хост не в группе `kvm`). **Сеть — QEMU user-net (SLIRP), НЕ
bridge/tap:** гость ходит на ocserv через `10.0.2.2:4443/4444` (`ClientConfig.Host`
понимает `host:port`), трафик к inner-сетям идёт через сам туннель — прямой L2 к
docker не нужен, лишний `sudo` для сети тоже. Провижн через **cloud-init (NoCloud
ISO, собирается `xorriso`)** + **9p-шара** хостовой папки с артефактами (`.deb`,
harness, `config.toml` с `10.0.2.2`, CA) и для вывода результатов — без SSH и без
пересборки образа. Внутри (cloud-init как root): `apt install ./dualvpn_*.deb`
(проверка объявленных зависимостей на чистой системе), harness `-mode socks5` и
`-mode tun` (реальный TUN + split-маршруты), GUI-smoke `xvfb-run dualvpn` N секунд
(не падает → зависимости на месте); результат пишется в 9p-шару, VM гасится
(`poweroff`), хост читает файл результата. Полностью автоматически.

**Windows** (`vm/windows/`): QEMU + Windows Eval ISO, **полностью автоматически**
через `autounattend.xml` (+ virtio-драйверы для диска/сети). Провижн: NSIS-инсталлятор
(per-user, кладёт `wintun.dll`), harness `.exe`, `config.toml`, импорт CA в хранилище
Windows. Прогон: harness (Wintun-TUN и SOCKS5), GUI-smoke. Это самая тяжёлая и
хрупкая часть (образ ~5 ГБ, отладка unattend) — изолирована в своём слое, ядро стенда
от неё не зависит.

### 5. Оркестрация (`run.sh` + `make e2e`)

Параметризуется `E2E_BACKEND` (по умолчанию `ocserv`) и списком клиентов. Цикл:

1. поднять bridge/tap-сеть;
2. поднять бэкенд (compose up / mockasa);
3. **эталон**: с хоста прогнать штатный `openconnect` против каждого сервера —
   доказать, что серверы принимают AnyConnect-клиента (снять из уравнения ocserv);
4. поднять клиентскую(ие) VM, прогнать harness (TUN, затем SOCKS5) + GUI-smoke;
5. собрать отчёт PASS/FAIL;
6. teardown (VM, compose, tap/bridge) — идемпотентно, даже при ошибке (`trap`).

## Лестница совместимости (ожидаемая находка)

ocserv ≠ ASA в логине/группах. Порядок прогона на каждом бэкенде:

1. `mockasa` — обязан пройти полностью (валидирует стенд);
2. `openconnect` против ocserv — обязан пройти (валидирует сервер);
3. DualVPN против ocserv — **если handshake не сходится**, это фиксируется как
   находка в разделе ниже, а не как провал стенда. Возможные исходы: доработка
   `sslcon` под ocserv-XML, опция доверия CA/insecure в TLS-конфиге клиента,
   подстройка group-конфига ocserv.

### Журнал расхождений (заполняется при прогоне)

**Task 6, 2026-07-24: DualVPN (форк sslcon) не проходит handshake с ocserv из-за поля `group-select`.**

Прогон `make e2e`: харнесс собирается, ocserv-a/b поднимаются, но оба туннеля падают
на этапе пароля. Точная строка лога (оба туннеля идентично):

```
2026/07/24 14:25:20 [a] error: PasswordAuth: auth error 401 Authentication failed
2026/07/24 14:25:20 [b] error: PasswordAuth: auth error 401 Authentication failed
2026/07/24 14:25:50 подъём туннелей: таймаут готовности (30s), не поднялись: [a b]
```
(идентично воспроизведено в TUN-режиме через `sudo` — падает на той же строке auth.go,
до какой-либо TUN-специфичной настройки, т.е. расхождение целиком в auth-фазе, общей
для обоих режимов.)

Причина найдена и точно локализована ручным воспроизведением aggregate-auth
последовательности через `curl` (без изменения кода sslcon):

- шаблон `templateAuthReply` (`internal/vpn/sslcon/auth.go:627-644`) всегда шлёт
  `<group-select>{{.Group}}</group-select>`, заполняя его значением поля `group` из
  `config.toml` (в стенде — `"LAB"`, как задано брифом Task 6 шаг 1);
- `ocserv-a.conf`/`ocserv-b.conf` (Task 5) не определяют ни одной именованной
  auth-группы (нет директивы `select-group`) — в отличие от ASA, где `tunnel-group`/
  `group-select` — обязательная часть XML-протокола, но сервер сопоставляет её со
  списком известных групп и либо принимает известную, либо (по наблюдаемому поведению
  ASA-эндпоинтов проекта) просто использует единственную доступную;
- ocserv же при получении auth-reply с **непустым** `<group-select>`, не совпадающим
  ни с одной настроенной группой, отвечает `HTTP 401 Authentication failed` с пустым
  телом сразу на финальном (username+password) запросе — даже при абсолютно верных
  логине/пароле и корректно перенесённой сессионной cookie `webvpncontext`.

Прямое воспроизведение через `curl` (без sslcon) подтвердило причину экспериментально:
тот же 3-шаговый aggregate-auth (`init` → username → username+password), с теми же
`user`/`pass`, тем же TLS-соединением к `127.0.0.1:4443`, тем же cookie-jar:

- с `<group-select>LAB</group-select>` в финальном запросе → `HTTP/1.1 401
  Authentication failed`, `Content-Length: 0`;
- с **пустым/отсутствующим** `<group-select>` → `HTTP/1.1 200 OK`,
  `type="complete"`, `<auth id="success">`, плюс `Set-Cookie: webvpn=...` (сессионный
  токен для CSTP) — то есть логин проходит целиком.

**Вывод**: это не общая несовместимость sslcon с ocserv-протоколом (TLS, cookie,
username/password-шаги, парсинг XML — всё это отработало штатно), а узкое расхождение
в семантике одного поля: ASA терпимо относится к `group-select`, ocserv в лабораторной
конфигурации без `select-group` — нет. Два равноценных пути устранения (оба вне рамок
Task 6, будущая работа): (а) в `sslcon` не отправлять `<group-select>`, когда группа не
задана явно/не нужна серверу, либо (б) на стороне лабораторного стенда добавить в
ocserv-конфиги секцию `select-group = LAB = ...`, чтобы имя группы стало для ocserv
валидным. Код `internal/vpn/sslcon` в рамках Task 6 не менялся.

Milestone-тест (`TestDualTunnelSocksIsolation`, mockasa-бэкенд) остаётся зелёным
независимо от этой находки — перепрогнан после диагностики, `PASS` (5.2s).

**RESOLVED, 2026-07-24: путь (а) реализован.** `sslcon` теперь шлёт
`<group-select>` только когда сервер реально предложил список групп:
`InitAuth` ставит `Profile.SendGroupSelect = (len(dtd.Auth.Form.Groups) != 0)`,
оба шаблона (`templateAuthReply`, `template2FAReply`) обёрнуты в
`{{if .SendGroupSelect}}`. ocserv без `select-group` групп не предлагает → флаг
`false` → group-select не уходит; реальный ASA список групп предлагает → флаг
`true` → поведение прежнее. Покрыто тестами: `group_select_test.go`
(рендер шаблонов, оба направления) и `internal/mockasa/nogroup_test.go`
(режим `Config.NoGroupSelect` — ocserv-строгий сервер, сквозной auth). mockasa
получил режим `NoGroupSelect`, эмулирующий ocserv без select-group.

**Проверено против реального ocserv 1.1.6:** оба туннеля поднимаются
одновременно, auth проходит (401 устранён), harness даёт `exit 0` —
связность обоих inner-сетей (whoami → 200) и изоляция (A↛B, B↛A) подтверждены.

**Побочная находка (data-plane, не DualVPN): лабораторный ocserv не настраивал
NAT.** Auth/CSTP работали, но данные до inner-host не доходили: whoami получал
пакеты с адреса VPN-пула `10.9x.0.x`, а обратного маршрута к пулу не было. ocserv
сам NAT не поднимает. Устранено на стенде: в образ добавлен `iptables`, `up.sh`
ставит `MASQUERADE` для `10.90.0.0/24` и `10.91.0.0/24`. Это конфигурация стенда,
не изменение клиента.

**Task 3, 2026-07-24: `make e2e-vm` (реальная Linux-VM, QEMU/KVM user-net) —
PASS по ядру, находка в GUI-smoke.** Полный автоматический прогон:
`prepare.sh` → QEMU (`sudo`, `-enable-kvm`, user-net, 9p-шара, cloud-init) →
`provision-guest.sh` в госте → `poweroff` → хост читает `result.txt`. Все три
ключевые точки проверки подтверждены эмпирически:

- 9p-шара монтируется в госте (`mount -t 9p ... share` из `bootcmd` cloud-init
  отработал без ошибок, `provision-guest.sh` и артефакты видны в `/mnt/share`);
- гость реально достаёт ocserv через `10.0.2.2:4443/4444` по user-net (harness
  SOCKS5 и TUN оба дали `exit 0`, включая связность до inner-хостов и изоляцию
  A↛B/B↛A — то же покрытие, что и в host-стенде `make e2e`);
- `.deb` тянет зависимости из архива Ubuntu по user-net на чистом образе
  (`apt-get install ./dualvpn_*.deb` разрешил полный граф зависимостей —
  webkit2gtk-4.1, gtk3, libayatana-appindicator3 и т.д. — без ручной подкрутки
  источников).

Результат гостя (`result.txt`):
```
[14:53:14] === установка .deb (проверка зависимостей на чистой системе) ===
[14:54:02] PASS: .deb установлен, зависимости разрешены
[14:54:02] === harness SOCKS5 (без root-специфики) ===
[14:54:15] SOCKS5 exit=0
[14:54:15] === harness TUN (root, реальные маршруты) ===
[14:54:15] TUN exit=0
[14:54:15] === GUI-smoke (xvfb-run dualvpn N сек) ===
[14:54:23] FAIL: GUI упал за 8с (exit=2, см. gui.log)
[14:54:23] === ИТОГ: deb=0 socks=0 tun=0 gui=1 → провалов=1 ===
OVERALL_EXIT=1
```

Единственный провал — GUI-smoke (`exit=1` из четырёх шагов), причина точно
локализована по `gui.log`: `dualvpn` падает `SIGABRT` внутри cgo-вызова
`github.com/getlantern/systray._Cfunc_nativeLoop` (трей-иконка), которому
предшествуют предупреждения `Unable to get the session bus: Failed to execute
child process "dbus-launch" (No such file or directory)` от
`libayatana-appindicator`/`libdbusmenu-glib`. Причина — в минимальном cloud-image
+ `xvfb-run` нет ни `dbus-launch`, ни вообще работающей сессионной шины/трей-хоста
(StatusNotifierWatcher), которых требует системный трей; это ожидаемо в headless
Xvfb без окружения рабочего стола и не относится к оркестрации VM или к ядру
DualVPN (auth/CSTP/маршруты/изоляция все отработали штатно). Возможные пути на
будущее (вне рамок Task 3): поставить `dbus-x11`/`at-spi-bus-launcher` в пакеты
cloud-init и оборачивать GUI-smoke в `dbus-run-session`, либо сделать трей
DualVPN устойчивым к отсутствию шины (не паниковать, а деградировать без иконки).

`make e2e-vm` завершился `exit 1` (по `OVERALL_EXIT=1` из гостя) — корректно
пробросил код через `run.sh`. Ядро стенда (`make e2e`, milestone-тесты) от
VM-слоя не зависит и не затронуто.

**RESOLVED, 2026-07-24: выбран второй путь — трей DualVPN сделан устойчивым к
отсутствию шины.** `internal/ui/tray.go`: `Tray.Start` теперь вызывает
`systray.Run` только если окружение поддерживает трей (`trayEnvAvailable` →
на Linux `hasSessionBus`: задан `DBUS_SESSION_BUS_ADDRESS` или есть сокет
`$XDG_RUNTIME_DIR/bus`); иначе трей пропускается с логом, окно приложения
работает без иконки. Это правильный продуктовый фикс (SIGABRT был C-уровневым,
`recover` его не ловит) и он же снимает провал GUI-smoke — dbus в госте больше
не нужен. Проверено локально: `env -u DBUS_SESSION_BUS_ADDRESS -u XDG_RUNTIME_DIR
xvfb-run ./bin/dualvpn-linux` раньше падал `SIGABRT`, теперь живёт до таймаута
(лог «трей: окружение не поддерживает системный трей — пропускаю»). Покрыто
`internal/ui/tray_test.go` (`TestHasSessionBus`).

**Windows 11 VM (2026-07-24…25): слой написан, живой boot заблокирован firmware.**
`test/e2e/vm/windows/` + `make e2e-win-vm` полностью реализованы (autounattend Win11
Pro с GPT + LabConfig-обходом TPM/SecureBoot; provision.ps1: harness socks5+tun +
GUI-smoke; data-ISO с бинарями/wintun.dll; FAT-диск результата; OVMF q35; AHCI+e1000
без virtio; ISO-симлинк из-за запятой в пути; startup.nsh). Целевой ISO —
Win11 Pro Ru 24H2 (`/mnt/Data-2/Distr/...consumer...x64_dvd_d061a709.iso`, index 4).
**8 итераций живой отладки** дали твёрдый диагноз: **OVMF в этом окружении не может
загрузить установочный носитель Win11**. Из `console.log`:
`BdsDxe: failed to load Boot0001 "UEFI QEMU DVD-ROM" ... : Not Found` — собственная
boot-запись прошивки для DVD не находит EFI-загрузчик на носителе; а `bootmgfw.efi`,
запущенный из UEFI-шелла напрямую или как `bcfg`-boot-запись, стартует (виден смен
видеорежима) и **возвращается**, не найдя `\sources\boot.wim` (BCD установочного
носителя ссылается на устройство по сигнатуре, которая при не-firmware-загрузке не
совпадает). Пробовали: bootindex на своём и встроенном q35-SATA, `-cdrom`, chainload
`cdboot_noprompt.efi`/`bootx64.efi`, `bcfg boot add`+`reset` (упирается в reset-цикл,
т.к. ни одна запись не грузится). `startup.nsh` **автозапускается** только в
`-cdrom`-конфиге — этот механизм рабочий; блокер именно в невозможности прошивки
поднять загрузчик Windows-установки. Это не дефект DualVPN/теста и не тюнингуемая
мелочь: нужен другой OVMF/edk2-билд, standalone `Shell.efi` (в системе нет), `swtpm`,
либо инструмент типа Ventoy — вне разумных рамок. Прагматичная альтернатива для
живого зелёного прогона на Windows — **Windows Server 2022 (BIOS/SeaBIOS)**:
`SERVER_EVAL_x64FRE_en-us.iso` есть, BIOS `-boot once=d` надёжно грузит CD, обходя
весь UEFI-блокер (не Win11-клиент, но настоящий Windows).

**Обновление (13+ итераций, продвинулись почти до конца).** Отказались от загрузки
ISO как CD (OVMF её не поднимает) в пользу метода «UEFI-флешки»: собрали
**GPT-диск с ESP (FAT32)** — файлы установки + `install.wim`, разрезанный на
`install.swm` через `wimlib-imagex` (4.3 ГБ не влезает в FAT32), autounattend в
корне (`build-install-media.sh`). Это **сняло** блокеры «OVMF → shell» и «No mapping»:
теперь OVMF **сам грузит `\EFI\BOOT\BOOTX64.EFI` с ESP** (в `console.log`:
`BdsDxe: starting Boot0001 ... Sata(0x1)`). Финальный блокер — **bootmgfw
fast-fail-цикл**: 110 попыток за ~240с (~2.2с каждая, т.е. WinPE не грузится),
`bootmgfw.efi` не находит `\sources\boot.wim`, потому что BCD носителя
(`\efi\microsoft\boot\bcd`, скопированный с ISO) ссылается на загрузочное устройство
по сигнатуре, которая не совпадает с пересобранным GPT/FAT-носителем. Штатно это
чинит Windows-утилита `bcdboot` (регенерация BCD под новое устройство) — её на
Linux-хосте нет, а ручная правка BCD-hive (`hivexsh`) — часы с высоким риском.
Итог: пройдена вся цепочка кроме регенерации BCD; для живого прогона на Windows
остаётся Server 2022 (BIOS) как прагматичный путь.

**Финальная диагностика BCD (python-hivex, отвергает гипотезу «BCD-mismatch»).**
Разобрали BCD носителя (`\efi\microsoft\boot\bcd`): `{ramdiskoptions}.ramdisksdidevice`
= тип `0x05` (**BootDevice = `[boot]`**), `ramdisksdipath` = `\boot\boot.sdi`;
Setup-загрузчик `{7619dcc9}` (`path=\windows\system32\boot\winload.efi`) имеет
device = ramdisk (тип 3) с родителем тип `0x05` (`[boot]`) и путём `\sources\boot.wim`.
**Все ссылки — `[boot]` (относительно загрузочного устройства), как на штатной
загрузочной USB.** То есть носитель собран корректно и BCD-правка НЕ помогла бы:
`bootmgfw.efi` молча возвращается (скриншот VNC — только OVMF/TianoCore-сплэш, без
экрана ошибки Windows) при полностью валидных BCD и файлах. Вывод: блокер —
**несовместимость данной OVMF-сборки (EDK II UEFI 2.70) с этим `bootmgfw`**, а не
дефект носителя/BCD. Media-side фиксы исчерпаны; нужен другой firmware-билд/машина
с рабочим UEFI, либо Server 2022 (BIOS).

**✅ CONFIRMED (2026-07-27): DualVPN РАБОТАЕТ на Windows.** Пивот на Windows Server
2022 (BIOS/SeaBIOS) + подход «готовый образ»: `run-server.sh` ставит Server 2022
автономно один раз (ключевой фикс — для eval-ISO **без ProductKey**, иначе GVLK
фильтрует редакции → «No images available»); затем **`run-ready.sh` / `make
e2e-win-ready`** офлайн-инъектит harness в готовый `srv.qcow2` (qemu-nbd + ntfs-3g +
python-hivex: autologon + **`DisableCAD=1`** — Server без этого ждёт Ctrl+Alt+Del и
autologon не срабатывает — + RunOnce на `run-harness.ps1`) и загружает. Результат из
реального Windows Server 2022 (`C:\dvlab\result.txt`, `OVERALL_EXIT=0`):
- **SOCKS5:** `все туннели готовы: [a b]`; `PASS [a] 192.168.90.10 → 200`,
  `PASS [b] 192.168.91.10 → 200`; `PASS [isolation] a↛b`, `PASS [isolation] b↛a`.
- **TUN (Wintun-драйвер):** `все туннели готовы: [a b]`; `PASS [a] → 200`,
  `PASS [b] → 200` (туннели получили `X-Cstp-Address 10.90.0.193` / `10.91.0.193`).

Т.е. клиент на Windows поднимает два одновременных туннеля к ocserv, работает и в
SOCKS5 (gVisor netstack), и в TUN (Wintun); связность обеих inner-сетей и изоляция
подтверждены. `FirstLogonCommand`-путь (`run-win.sh`/`run-server.sh` автоустановка)
остаётся флаки для авто-запуска harness — рабочий способ = готовый образ + RunOnce.

## Оценка надёжности

- 🟢 Надёжно/быстро: ocserv-контейнеры, mockasa-бэкенд, Linux-harness, проверки.
- 🟡 Тяжело/хрупко: Windows VM (образ, autounattend), asav (лицензия).

Порядок реализации: сначала твёрдое ядро (ocserv + mockasa бэкенды × Linux-клиент,
оба режима, проверки, `make e2e`) — даёт результат сразу; Windows-VM и asav —
следующими слоями на той же абстракции.

## Вне рамок (YAGNI)

- 2FA/TOTP на стенде (auth только по паролю);
- порт в `config.Tunnel.Endpoint` (серверы на 443);
- CI-интеграция;
- реализация `asav`/`remote`-бэкендов (только задел).

## Затрагиваемый код

- **Новое:** `cmd/dualvpn-harness/`, весь `test/e2e/`, цель `e2e` в `Makefile`,
  записи в `.gitignore` (образы VM, серты, qcow2).
- **Существующее:** переиспользуется без изменений — `vpn.Manager`, `sslcon`,
  `internal/mockasa`, `config`. Изменения в `sslcon` — только если их потребует
  журнал расхождений (тогда отдельной работой).
