# DualVPN E2E Linux-VM (клиент в настоящей VM) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Прогонять DualVPN внутри настоящей Ubuntu-VM: ставить `.deb` на чистой системе (проверка зависимостей), поднимать оба туннеля к ocserv через harness (SOCKS5 и TUN) и делать GUI-smoke — полностью автоматически, `make e2e-vm`.

**Architecture:** QEMU/KVM (под `sudo`) поднимает Ubuntu 24.04 cloud image через overlay-диск. Сеть — user-net (SLIRP): гость ходит на ocserv, опубликованный на хосте, через `10.0.2.2:4443/4444` (трафик к inner-сетям идёт через сам туннель). Провижн — cloud-init (NoCloud seed ISO, собирается `xorriso`); артефакты (`.deb`, harness, `config.toml`, результаты) передаются через 9p-шару хостовой папки. Гость (cloud-init как root) ставит `.deb`, гоняет harness в обоих режимах и GUI-smoke, пишет результат в шару и делает `poweroff`; хост читает файл результата.

**Tech Stack:** QEMU 9.2 + KVM, Ubuntu 24.04 (noble) cloud image, cloud-init NoCloud, `xorriso` (seed ISO), virtio-9p (virtfs), существующий `bin/dualvpn-harness` и `make deb`, бэкенд ocserv из `test/e2e/backends/ocserv`.

## Global Constraints

- Go **не в PATH** — сборочные go-команды префиксить: `export PATH="/usr/local/go/bin:$PATH"`.
- Харнесс собирается **без** тега `webkit2_41`; полноценная сборка `.deb` (`make deb`) — с тегом (её делает существующий Makefile).
- Хост **не в группе `kvm`** → QEMU запускать через `sudo` (passwordless есть). `/dev/kvm` = root:kvm 660.
- **Ubuntu 24.04 (noble)** обязателен: `.deb` зависит от `libgtk-3-0t64` (именование 24.04) и `libwebkit2gtk-4.1-0`.
- **Сеть — только QEMU user-net.** Гость видит хост как `10.0.2.2`; ocserv опубликован на хосте `127.0.0.1:4443/4444` и доступен гостю как `10.0.2.2:4443/4444`. Никаких bridge/tap.
- Гостю нужен интернет для `apt` (ставит webkit/gtk/appindicator) — user-net даёт NAT в интернет хоста.
- Скачиваемые/генерируемые артефакты (cloud image, overlay, seed ISO, share, результаты) — в `.gitignore`, НЕ коммитятся. Коммитятся только скрипты/конфиги/cloud-init.
- Комментарии и сообщения — на русском.
- Диск: на `/home` ~19 ГБ свободно. Cloud image ~0.7 ГБ + overlay (растёт при `apt`). Следить, чистить overlay в teardown.
- Харнесс в VM ходит с `-insecure` (не тащим CA в доверие — снижаем хрупкость); проверка `.deb`-зависимостей идёт через сам `apt install`.

## Структура файлов

```
test/e2e/vm/linux/
  cloud-init/
    user-data          — cloud-init: монтирует 9p, зовёт provision-guest.sh
    meta-data          — cloud-init NoCloud meta-data (instance-id/hostname)
  provision-guest.sh   — гостевой скрипт: apt install .deb, harness x2, GUI-smoke, результат, poweroff
  config.toml          — конфиг харнесса для VM (Host = 10.0.2.2:4443/4444)
  prepare.sh           — хост: скачать образ, создать overlay, собрать seed ISO, наполнить share
  run.sh               — хост: prepare + ocserv up + boot QEMU + ждать poweroff + читать результат + teardown
Makefile:              цель `e2e-vm`
.gitignore:            артефакты VM
```

---

### Task 1: cloud-init, гостевой провижн и VM-конфиг

**Files:**
- Create: `test/e2e/vm/linux/cloud-init/meta-data`
- Create: `test/e2e/vm/linux/cloud-init/user-data`
- Create: `test/e2e/vm/linux/provision-guest.sh`
- Create: `test/e2e/vm/linux/config.toml`

**Interfaces:**
- Produces: гость при первом boot монтирует 9p-шару с mount_tag `share`, выполняет `/mnt/share/provision-guest.sh`, который пишет `/mnt/share/result.txt` и гасит VM. Хост later читает `result.txt`.

- [ ] **Step 1: meta-data**

Создать `test/e2e/vm/linux/cloud-init/meta-data`:
```yaml
instance-id: dualvpn-linux-vm
local-hostname: dualvpn-vm
```

- [ ] **Step 2: user-data (монтирует 9p и запускает провижн)**

Создать `test/e2e/vm/linux/cloud-init/user-data`:
```yaml
#cloud-config
# Провижн клиентской VM: смонтировать 9p-шару и запустить гостевой скрипт.
# Вся логика теста — в provision-guest.sh на шаре (проще итерации без пересборки ISO).
bootcmd:
  - [ sh, -c, "mkdir -p /mnt/share" ]
  - [ sh, -c, "mount -t 9p -o trans=virtio,version=9p2000.L share /mnt/share || true" ]
runcmd:
  - [ sh, -c, "bash /mnt/share/provision-guest.sh > /mnt/share/provision.log 2>&1" ]
```

- [ ] **Step 3: config.toml для VM (ocserv через 10.0.2.2)**

Создать `test/e2e/vm/linux/config.toml`:
```toml
[mode]
  preferred = "socks5"

[[tunnels]]
  name = "a"
  endpoint = "10.0.2.2:4443"
  group = "LAB"
  socks_port = 21080
  tun_name = "dvlab0"
  routes = ["192.168.90.0/24"]
  username = "user"
  password = "pass"
  probe_url = "http://192.168.90.10/"

[[tunnels]]
  name = "b"
  endpoint = "10.0.2.2:4444"
  group = "LAB"
  socks_port = 21081
  tun_name = "dvlab1"
  routes = ["192.168.91.0/24"]
  username = "user"
  password = "pass"
  probe_url = "http://192.168.91.10/"
```

- [ ] **Step 4: гостевой провижн-скрипт**

Создать `test/e2e/vm/linux/provision-guest.sh`:
```bash
#!/usr/bin/env bash
# Гостевой провижн: ставит .deb, гоняет harness (socks5 + tun) и GUI-smoke,
# пишет сводку в /mnt/share/result.txt и гасит VM. Запускается cloud-init как root.
set -uo pipefail
SHARE=/mnt/share
RESULT="$SHARE/result.txt"
: > "$RESULT"
log() { echo "[$(date +%H:%M:%S)] $*" | tee -a "$RESULT"; }

log "=== установка .deb (проверка зависимостей на чистой системе) ==="
export DEBIAN_FRONTEND=noninteractive
apt-get update -y >/dev/null 2>&1 || log "WARN: apt-get update не прошёл"
DEB=$(ls "$SHARE"/dualvpn_*.deb 2>/dev/null | head -1)
if [[ -z "$DEB" ]]; then log "FAIL: .deb не найден в шаре"; deb_rc=1; else
  if apt-get install -y "$DEB" >>"$SHARE/apt.log" 2>&1; then
    log "PASS: .deb установлен, зависимости разрешены"; deb_rc=0
  else
    log "FAIL: apt install .deb (см. apt.log)"; deb_rc=1
  fi
fi

install -m0755 "$SHARE/dualvpn-harness" /usr/local/bin/dualvpn-harness
CFG="$SHARE/config.toml"

log "=== harness SOCKS5 (без root-специфики) ==="
/usr/local/bin/dualvpn-harness -config "$CFG" -mode socks5 -insecure -timeout 40s >>"$SHARE/harness-socks5.log" 2>&1
socks_rc=$?
log "SOCKS5 exit=$socks_rc"

log "=== harness TUN (root, реальные маршруты) ==="
/usr/local/bin/dualvpn-harness -config "$CFG" -mode tun -insecure -timeout 40s >>"$SHARE/harness-tun.log" 2>&1
tun_rc=$?
log "TUN exit=$tun_rc"

log "=== GUI-smoke (xvfb-run dualvpn N сек) ==="
gui_rc=0
if command -v xvfb-run >/dev/null 2>&1 && command -v dualvpn >/dev/null 2>&1; then
  xvfb-run -a dualvpn >>"$SHARE/gui.log" 2>&1 &
  gpid=$!
  sleep 8
  if kill -0 "$gpid" 2>/dev/null; then
    log "PASS: GUI прожил 8с без краша (зависимости на месте)"
    kill "$gpid" 2>/dev/null; wait "$gpid" 2>/dev/null
  else
    wait "$gpid" 2>/dev/null; gui_rc=$?
    log "FAIL: GUI упал за 8с (exit=$gui_rc, см. gui.log)"
    gui_rc=1
  fi
else
  # xvfb ставится в user-data через apt? ставим здесь при отсутствии
  apt-get install -y xvfb >>"$SHARE/apt.log" 2>&1 || true
  log "WARN: xvfb/dualvpn не найдены сразу; см. gui.log"
  gui_rc=1
fi

TOTAL=$(( deb_rc + (socks_rc!=0) + (tun_rc!=0) + gui_rc ))
log "=== ИТОГ: deb=$deb_rc socks=$socks_rc tun=$tun_rc gui=$gui_rc → провалов=$TOTAL ==="
echo "OVERALL_EXIT=$TOTAL" >> "$RESULT"
sync
log "гашу VM"
poweroff
```

Примечание: `xvfb`/`dualvpn` появляются после установки `.deb` (dualvpn) и `apt install xvfb`. Установку `xvfb` добавить в user-data через `packages:` ИЛИ в скрипте до GUI-smoke. Здесь ставим в user-data (шаг 2 дополнить `packages: [xvfb]`), чтобы к GUI-smoke он уже был.

- [ ] **Step 5: дополнить user-data установкой xvfb**

В `test/e2e/vm/linux/cloud-init/user-data` добавить перед `bootcmd:`:
```yaml
package_update: true
packages:
  - xvfb
```

- [ ] **Step 6: Проверить синтаксис**

Run:
```bash
cd /home/ub/dualvpn
bash -n test/e2e/vm/linux/provision-guest.sh && echo "provision syntax OK"
python3 -c "import yaml,sys; yaml.safe_load(open('test/e2e/vm/linux/cloud-init/user-data').read().split('\n',1)[1]); print('user-data YAML OK')"
python3 -c "import yaml; yaml.safe_load(open('test/e2e/vm/linux/cloud-init/meta-data')); print('meta-data YAML OK')"
```
Expected: три строки `... OK`.

- [ ] **Step 7: Commit**

```bash
cd /home/ub/dualvpn
git add test/e2e/vm/linux/cloud-init/ test/e2e/vm/linux/provision-guest.sh test/e2e/vm/linux/config.toml
git commit -m "test(e2e): cloud-init + гостевой провижн Linux-VM (.deb, harness x2, GUI-smoke)"
```

---

### Task 2: Хостовая подготовка — образ, overlay, seed ISO, share

**Files:**
- Create: `test/e2e/vm/linux/prepare.sh`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: `test/e2e/vm/linux/cloud-init/*`, `test/e2e/vm/linux/{provision-guest.sh,config.toml}`, `bin/dualvpn-harness`, `bin/dualvpn_*.deb`.
- Produces: в рабочем каталоге `test/e2e/vm/linux/work/`: `noble.img` (базовый образ, скачивается один раз), `overlay.qcow2` (backing=noble.img), `seed.iso` (CIDATA), каталог `share/` с `provision-guest.sh`, `config.toml`, `dualvpn-harness`, `dualvpn_*.deb`. Хостовый скрипт `run.sh` (Task 3) их использует.

- [ ] **Step 1: prepare.sh**

Создать `test/e2e/vm/linux/prepare.sh`:
```bash
#!/usr/bin/env bash
# Хостовая подготовка VM: базовый образ (скачать один раз) → overlay →
# seed ISO (cloud-init) → наполнить 9p-share артефактами.
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../../../.. && pwd)"
WORK="$(pwd)/work"
SHARE="$WORK/share"
IMG_URL="https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
BASE="$WORK/noble.img"
OVERLAY="$WORK/overlay.qcow2"
SEED="$WORK/seed.iso"

mkdir -p "$WORK" "$SHARE"

if [[ ! -f "$BASE" ]]; then
  echo "==> скачиваю Ubuntu 24.04 cloud image (~700 МБ, один раз)"
  curl -fL --retry 3 -o "$BASE.tmp" "$IMG_URL"
  mv "$BASE.tmp" "$BASE"
fi

echo "==> overlay-диск (backing = базовый образ, +8G под apt)"
rm -f "$OVERLAY"
qemu-img create -f qcow2 -F qcow2 -b "$BASE" "$OVERLAY" 12G >/dev/null

echo "==> seed ISO (cloud-init NoCloud, метка CIDATA)"
xorriso -as mkisofs -output "$SEED" -volid CIDATA -joliet -rock \
  cloud-init/user-data cloud-init/meta-data >/dev/null 2>&1

echo "==> наполняю 9p-share"
rm -f "$SHARE"/* 2>/dev/null || true
cp provision-guest.sh config.toml "$SHARE/"
cp "$ROOT/bin/dualvpn-harness" "$SHARE/"
DEB=$(ls "$ROOT"/bin/dualvpn_*.deb 2>/dev/null | head -1)
[[ -n "$DEB" ]] || { echo "нет bin/dualvpn_*.deb — сначала make deb" >&2; exit 1; }
cp "$DEB" "$SHARE/"

echo "==> готово: $WORK"
```
Сделать исполняемым: `chmod +x test/e2e/vm/linux/prepare.sh`.

- [ ] **Step 2: .gitignore**

В `.gitignore` добавить:
```
# E2E Linux-VM: скачиваемые/генерируемые артефакты
test/e2e/vm/linux/work/
```

- [ ] **Step 3: Собрать артефакты и проверить prepare.sh**

Run:
```bash
cd /home/ub/dualvpn
export PATH="/usr/local/go/bin:$PATH" GOFLAGS="-tags=webkit2_41"
go build -o bin/dualvpn-harness ./cmd/dualvpn-harness/
make deb >/dev/null 2>&1 || make deb
test/e2e/vm/linux/prepare.sh
```
Expected: скачан `work/noble.img`, созданы `work/overlay.qcow2`, `work/seed.iso`, в `work/share/` лежат `provision-guest.sh config.toml dualvpn-harness dualvpn_*.deb`.

- [ ] **Step 4: Проверить seed ISO валиден**

Run:
```bash
cd /home/ub/dualvpn
xorriso -indev test/e2e/vm/linux/work/seed.iso -toc 2>&1 | grep -i 'Volume id' 
ls -la test/e2e/vm/linux/work/share/
```
Expected: `Volume id : 'CIDATA'`; в share четыре файла.

- [ ] **Step 5: Commit**

```bash
cd /home/ub/dualvpn
git add test/e2e/vm/linux/prepare.sh .gitignore
git commit -m "test(e2e): подготовка Linux-VM (образ, overlay, cloud-init seed ISO, 9p-share)"
```

---

### Task 3: Оркестрация — boot QEMU, ожидание, чтение результата, `make e2e-vm`

**Files:**
- Create: `test/e2e/vm/linux/run.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `prepare.sh` (Task 2), бэкенд ocserv (`test/e2e/backends/ocserv/up.sh|down.sh`), `work/{overlay.qcow2,seed.iso,share}`.
- Produces: `make e2e-vm` — полный автоматический прогон, exit-код = `OVERALL_EXIT` из гостя.

- [ ] **Step 1: run.sh**

Создать `test/e2e/vm/linux/run.sh`:
```bash
#!/usr/bin/env bash
# E2E внутри настоящей Linux-VM: ocserv (docker) + QEMU/KVM гость через user-net.
# Гость ставит .deb, гоняет harness (socks5+tun) и GUI-smoke, пишет result.txt.
set -uo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../../../.. && pwd)"
OCS="$ROOT/test/e2e/backends/ocserv"
WORK="$(pwd)/work"
SHARE="$WORK/share"
BOOT_TIMEOUT="${BOOT_TIMEOUT:-360}"
export PATH="/usr/local/go/bin:$PATH"

cleanup() {
  sudo pkill -f "file=$WORK/overlay.qcow2" 2>/dev/null || true
  "$OCS/down.sh" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> сборка харнесса и .deb"
GOFLAGS="-tags=webkit2_41" make -C "$ROOT" deb >/dev/null
go build -o "$ROOT/bin/dualvpn-harness" "$ROOT/cmd/dualvpn-harness/"

echo "==> ocserv up"
"$OCS/up.sh" >/dev/null

echo "==> подготовка VM-артефактов"
./prepare.sh

rm -f "$SHARE/result.txt"
echo "==> запуск QEMU (user-net, 9p, cloud-init); ждём poweroff (до ${BOOT_TIMEOUT}s)"
sudo timeout "$BOOT_TIMEOUT" qemu-system-x86_64 \
  -enable-kvm -m 2048 -smp 2 -nographic \
  -drive file="$WORK/overlay.qcow2",if=virtio,format=qcow2 \
  -drive file="$WORK/seed.iso",if=virtio,format=raw,media=cdrom \
  -netdev user,id=n0 -device virtio-net-pci,netdev=n0 \
  -virtfs local,path="$SHARE",mount_tag=share,security_model=none,id=share \
  -serial file:"$WORK/console.log" -monitor none || true

echo "==> результат из гостя:"
if [[ -f "$SHARE/result.txt" ]]; then
  cat "$SHARE/result.txt"
  rc=$(sed -n 's/^OVERALL_EXIT=//p' "$SHARE/result.txt" | tail -1)
  echo "==> OVERALL_EXIT=${rc:-нет}"
  exit "${rc:-1}"
else
  echo "FAIL: гость не оставил result.txt (см. $WORK/console.log)"
  tail -30 "$WORK/console.log" 2>/dev/null || true
  exit 1
fi
```
Сделать исполняемым: `chmod +x test/e2e/vm/linux/run.sh`.

- [ ] **Step 2: Makefile-цель**

В `Makefile` добавить:
```makefile
.PHONY: e2e-vm
e2e-vm: ## E2E внутри настоящей Linux-VM (ocserv + QEMU-гость: .deb, harness, GUI-smoke)
	@test/e2e/vm/linux/run.sh
```

- [ ] **Step 3: Полный прогон**

Run:
```bash
cd /home/ub/dualvpn && make e2e-vm 2>&1 | tail -40
```
Expected один из зафиксированных исходов:
- **PASS** — гость: `.deb` установился (зависимости разрешены), harness SOCKS5 и TUN дали `exit 0` (оба туннеля + связность + изоляция), GUI-smoke прожил 8с; `OVERALL_EXIT=0`, `make e2e-vm` завершается `0`. ИЛИ
- **Документированная находка** — конкретный шаг упал (напр. VM не бутится под KVM в этом окружении; `apt` не достаёт webkit по user-net; harness в госте не видит `10.0.2.2:4443`). Тогда: снять точную причину из `work/console.log` / `work/share/*.log` и `result.txt`, записать в спеку (раздел про VM-слой) как находку. Ядро стенда (`make e2e`, milestone) от VM-слоя не зависит и остаётся зелёным.

Ключевые точки проверки при разборе (назвать в отчёте, что подтвердилось):
- гость реально достаёт ocserv через `10.0.2.2:4443` (user-net → host loopback);
- 9p-шара смонтировалась (`/mnt/share` виден в госте);
- `.deb` тянет зависимости из архива Ubuntu по user-net.

- [ ] **Step 4: Commit**

```bash
cd /home/ub/dualvpn
git add test/e2e/vm/linux/run.sh Makefile
git commit -m "test(e2e): make e2e-vm — прогон DualVPN внутри настоящей Linux-VM (QEMU user-net + 9p)"
```

---

## Self-Review

**Покрытие спеки (VM-слой Linux):**
- «Ubuntu 24.04 cloud image + QEMU/KVM под sudo» → Task 2 (образ/overlay), Task 3 (sudo qemu).
- «user-net, гость → 10.0.2.2:4443/4444» → Task 1 (config.toml), Task 3 (`-netdev user`).
- «cloud-init NoCloud ISO (xorriso) + 9p-шара» → Task 1 (cloud-init), Task 2 (seed ISO/share), Task 3 (`-virtfs`).
- «apt install .deb (проверка зависимостей на чистой системе)» → Task 1 (provision-guest.sh).
- «harness -mode socks5 и -mode tun» → Task 1 (provision-guest.sh), реальный TUN в госте под root.
- «GUI-smoke xvfb-run dualvpn N секунд» → Task 1 (provision-guest.sh + xvfb в packages).
- «результат в шару, VM гасится, хост читает» → Task 1 (result.txt + poweroff), Task 3 (чтение + exit-код).

**Плейсхолдеры:** боевых пропусков нет. Ожидаемые эмпирические риски (бут VM под KVM в этом окружении; интернет для apt по user-net; достижимость `10.0.2.2`→host-loopback) явно помечены как точки проверки в Task 3 шаг 3 — проверяются эмпирически, не угадываются; при провале — документируемая находка, ядро стенда не затрагивается.

**Согласованность:** mount_tag `share` одинаков в user-data (`mount -t 9p ... share`) и в run.sh (`mount_tag=share`). `config.toml` VM использует `10.0.2.2:4443/4444`, группы/порты/probe_url совпадают с host-конфигом ocserv (`test/e2e/backends/ocserv/config.toml`). `OVERALL_EXIT` пишется гостем и читается run.sh.

**Вне рамок (следующий план):** Windows-VM (autounattend), asav-бэкенд. GUI-smoke ограничен «процесс не упал за 8с» — глубже (клики) вне рамок.
