# DualVPN E2E Windows-VM (клиент в настоящей Windows 11 VM) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Прогонять DualVPN внутри настоящей Windows 11 Pro VM: полностью автоматическая установка (autounattend, обход TPM/SecureBoot), запуск harness в обоих режимах (SOCKS5 и Wintun-TUN) против ocserv, GUI-smoke — `make e2e-win-vm`.

**Architecture:** QEMU/KVM (под `sudo`) ставит **Windows 11 Pro (24H2)** полностью автоматически через `autounattend.xml`. Win11 требует UEFI + TPM 2.0 + Secure Boot; `swtpm` в системе нет, поэтому: загрузка через **OVMF (UEFI, без Secure Boot)**, а проверки TPM/SecureBoot/RAM/CPU снимаются ключами **LabConfig** в pass `windowsPE`. Диск размечается **GPT** (ESP+MSR+Windows). Драйверы — встроенные в Windows: **AHCI**-диск и **e1000** NIC (без virtio). Сеть — QEMU user-net (гость → ocserv на `10.0.2.2:4443/4444`). Артефакты — через **data-ISO** (CD), результат — через **FAT-диск** (хост читает loop-mount). Классический блокер UEFI «Press any key to boot from CD» снимается отправкой `sendkey ret` через QEMU-монитор. Гость на auto-logon запускает `provision.ps1`.

**Tech Stack:** QEMU 9.2 + KVM (q35, OVMF UEFI), Windows 11 Pro Ru 24H2 ISO (`/mnt/Data-2/Distr/.../ru-ru_windows_11_consumer_editions_version_24h2_updated_may_2026_x64_dvd_d061a709.iso`), `autounattend.xml` (GPT + LabConfig bypass), PowerShell (провижн), `xorriso` (data-ISO), `mkfs.vfat` (диск результата), `socat` (QEMU monitor sendkey), кросс-собранные `dualvpn-harness.exe` + `bin/DualVPN.exe` + `build/windows/deps/wintun.dll`.

## Global Constraints

- Go **не в PATH** — префиксить: `export PATH="/usr/local/go/bin:$PATH"`.
- Windows-бинари: harness — `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/dualvpn-harness/` (без webkit, проверено, 19 МБ); GUI — `make build-windows` (→ `bin/DualVPN.exe`).
- Хост **не в группе `kvm`** → QEMU под `sudo`.
- **Windows 11 Pro** (образ install.wim индекс 4, metadata Name `Windows 11 Pro`). Русский ISO — язык ОС на harness/GUI не влияет; в autounattend язык `ru-RU` (должен совпадать с ISO).
- **UEFI без Secure Boot** через OVMF: `/usr/share/OVMF/OVMF_CODE_4M.fd` (readonly) + записываемая копия `OVMF_VARS_4M.fd`. Машина `q35`.
- **TPM/SecureBoot/RAM/CPU/Storage** — обход `HKLM\System\Setup\LabConfig` (RunSynchronous в windowsPE). RAM гостю ≥4096 МБ.
- **Только AHCI + e1000** (встроенные драйверы), НЕ virtio.
- Сеть — QEMU user-net: ocserv виден как `10.0.2.2:4443/4444`.
- «Press any key to boot from CD» (UEFI) снимается фоновой отправкой `sendkey ret` через `-monitor unix:` + `socat`.
- Артефакты стенда (data-ISO, FAT-диск, install-qcow2, OVMF_VARS) — в `work/`, gitignored. Windows ISO НЕ копируется (переменная `WIN_ISO`).
- Комментарии/сообщения — на русском.
- Диск: ~66 ГБ свободно; install-qcow2 растёт до ~20 ГБ. Достаточно.
- **Установка Win11 долгая (~25–45 мин) и хрупкая** — `BOOT_TIMEOUT` большой; при провале конкретного шага это документируемая находка, ядро стенда (`make e2e`, milestone) не затрагивается.

## Структура файлов

```
test/e2e/vm/windows/
  autounattend.xml     — авто-установка Win11 Pro (GPT + LabConfig bypass + auto-logon + запуск provision)
  provision.ps1        — гостевой: harness socks5+tun, GUI-smoke, result.txt, shutdown
  config.toml          — конфиг harness (10.0.2.2:4443/4444)
  prepare-win.sh       — хост: собрать exe, data-ISO, FAT result-disk, install-qcow2, копия OVMF_VARS
  run-win.sh           — хост: prepare + ocserv up + boot QEMU(OVMF) + sendkey + ждать shutdown + читать result + teardown
Makefile:              цель e2e-win-vm
.gitignore:            test/e2e/vm/windows/work/
```

---

### Task 1: autounattend.xml (Win11, GPT+bypass) + provision.ps1 + config.toml

**Files:**
- Create: `test/e2e/vm/windows/autounattend.xml`
- Create: `test/e2e/vm/windows/provision.ps1`
- Create: `test/e2e/vm/windows/config.toml`

**Interfaces:**
- Produces: Windows Setup находит `autounattend.xml` в корне сменного носителя (data-ISO), снимает проверки Win11 (LabConfig), ставит систему на GPT без вопросов, авто-логинится Administrator, FirstLogonCommand запускает `provision.ps1`. `provision.ps1` пишет `result.txt` (последняя строка `OVERALL_EXIT=N`) на FAT-диск (том `RESULT`) и выключает VM.

- [ ] **Step 1: config.toml**

Создать `test/e2e/vm/windows/config.toml`:
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

- [ ] **Step 2: autounattend.xml**

Создать `test/e2e/vm/windows/autounattend.xml`:
```xml
<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <settings pass="windowsPE">
    <component name="Microsoft-Windows-International-Core-WinPE" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <SetupUILanguage><UILanguage>ru-RU</UILanguage></SetupUILanguage>
      <InputLocale>en-US</InputLocale>
      <SystemLocale>ru-RU</SystemLocale>
      <UILanguage>ru-RU</UILanguage>
      <UserLocale>ru-RU</UserLocale>
    </component>
    <component name="Microsoft-Windows-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <RunSynchronous>
        <RunSynchronousCommand wcm:action="add"><Order>1</Order><Path>cmd /c reg add HKLM\System\Setup\LabConfig /v BypassTPMCheck /t REG_DWORD /d 1 /f</Path></RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add"><Order>2</Order><Path>cmd /c reg add HKLM\System\Setup\LabConfig /v BypassSecureBootCheck /t REG_DWORD /d 1 /f</Path></RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add"><Order>3</Order><Path>cmd /c reg add HKLM\System\Setup\LabConfig /v BypassRAMCheck /t REG_DWORD /d 1 /f</Path></RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add"><Order>4</Order><Path>cmd /c reg add HKLM\System\Setup\LabConfig /v BypassCPUCheck /t REG_DWORD /d 1 /f</Path></RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add"><Order>5</Order><Path>cmd /c reg add HKLM\System\Setup\LabConfig /v BypassStorageCheck /t REG_DWORD /d 1 /f</Path></RunSynchronousCommand>
      </RunSynchronous>
      <DiskConfiguration>
        <Disk wcm:action="add">
          <DiskID>0</DiskID>
          <WillWipeDisk>true</WillWipeDisk>
          <CreatePartitions>
            <CreatePartition wcm:action="add"><Order>1</Order><Type>EFI</Type><Size>260</Size></CreatePartition>
            <CreatePartition wcm:action="add"><Order>2</Order><Type>MSR</Type><Size>16</Size></CreatePartition>
            <CreatePartition wcm:action="add"><Order>3</Order><Type>Primary</Type><Extend>true</Extend></CreatePartition>
          </CreatePartitions>
          <ModifyPartitions>
            <ModifyPartition wcm:action="add"><Order>1</Order><PartitionID>1</PartitionID><Format>FAT32</Format><Label>System</Label></ModifyPartition>
            <ModifyPartition wcm:action="add"><Order>2</Order><PartitionID>3</PartitionID><Format>NTFS</Format><Label>Windows</Label><Letter>C</Letter></ModifyPartition>
          </ModifyPartitions>
        </Disk>
      </DiskConfiguration>
      <ImageInstall>
        <OSImage>
          <InstallFrom>
            <MetaData wcm:action="add"><Key>/IMAGE/INDEX</Key><Value>4</Value></MetaData>
          </InstallFrom>
          <InstallTo><DiskID>0</DiskID><PartitionID>3</PartitionID></InstallTo>
        </OSImage>
      </ImageInstall>
      <UserData>
        <AcceptEula>true</AcceptEula>
        <ProductKey><Key>W269N-WFGWX-YVC9B-4J6C9-T83GX</Key><WillShowUI>Never</WillShowUI></ProductKey>
      </UserData>
    </component>
  </settings>
  <settings pass="specialize">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <ComputerName>DUALVPN-WIN</ComputerName>
    </component>
  </settings>
  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <UserAccounts>
        <AdministratorPassword><Value>Passw0rd!</Value><PlainText>true</PlainText></AdministratorPassword>
      </UserAccounts>
      <AutoLogon>
        <Password><Value>Passw0rd!</Value><PlainText>true</PlainText></Password>
        <Enabled>true</Enabled><Username>Administrator</Username><LogonCount>1</LogonCount>
      </AutoLogon>
      <OOBE>
        <HideEULAPage>true</HideEULAPage>
        <HideLocalAccountScreen>true</HideLocalAccountScreen>
        <HideOnlineAccountScreens>true</HideOnlineAccountScreens>
        <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE>
        <ProtectYourPC>3</ProtectYourPC>
      </OOBE>
      <FirstLogonCommands>
        <SynchronousCommand wcm:action="add">
          <Order>1</Order>
          <CommandLine>powershell -ExecutionPolicy Bypass -Command "foreach($d in 68..90){$l=[char]$d; if(Test-Path ($l+':\provision.ps1')){ Start-Process powershell -ArgumentList '-ExecutionPolicy','Bypass','-File',($l+':\provision.ps1') -Wait; break}}"</CommandLine>
          <Description>DualVPN provision</Description>
        </SynchronousCommand>
      </FirstLogonCommands>
    </component>
  </settings>
</unattend>
```

- [ ] **Step 3: provision.ps1**

Создать `test/e2e/vm/windows/provision.ps1`:
```powershell
# Гостевой провижн Windows: находит артефакты (CD) и диск результата (FAT),
# гоняет harness (socks5 + Wintun-tun) и GUI-smoke, пишет result.txt, выключает VM.
$ErrorActionPreference = "Continue"

function Find-Drive($marker) {
  foreach ($d in 68..90) { $l = [char]$d; if (Test-Path ("${l}:\$marker")) { return "${l}:" } }
  return $null
}
$art = Find-Drive "provision.ps1"          # CD с артефактами
$res = Find-Drive "RESULTDISK.marker"      # FAT-диск результата
if (-not $res) { $res = "R:" }
$result = "$res\result.txt"
"" | Out-File -Encoding ascii $result
function Log($m) { $t = (Get-Date -Format HH:mm:ss); "[$t] $m" | Tee-Object -Append $result }

Log "artifacts=$art result=$res"
New-Item -ItemType Directory -Force C:\dvlab | Out-Null
Copy-Item "$art\dualvpn-harness.exe" C:\dvlab\ -Force
Copy-Item "$art\wintun.dll" C:\dvlab\ -Force
Copy-Item "$art\DualVPN.exe" C:\dvlab\ -Force -ErrorAction SilentlyContinue
Copy-Item "$art\config.toml" C:\dvlab\ -Force

Log "=== harness SOCKS5 ==="
$p = Start-Process C:\dvlab\dualvpn-harness.exe -ArgumentList '-config','C:\dvlab\config.toml','-mode','socks5','-insecure','-timeout','40s' -Wait -NoNewWindow -PassThru -RedirectStandardOutput "$res\harness-socks5.log" -RedirectStandardError "$res\harness-socks5.err"
$socks = $p.ExitCode; Log "SOCKS5 exit=$socks"

Log "=== harness TUN (Wintun) ==="
$p = Start-Process C:\dvlab\dualvpn-harness.exe -ArgumentList '-config','C:\dvlab\config.toml','-mode','tun','-insecure','-timeout','40s' -Wait -NoNewWindow -PassThru -RedirectStandardOutput "$res\harness-tun.log" -RedirectStandardError "$res\harness-tun.err"
$tun = $p.ExitCode; Log "TUN exit=$tun"

Log "=== GUI-smoke ==="
$gui = 0
if (Test-Path C:\dvlab\DualVPN.exe) {
  $g = Start-Process C:\dvlab\DualVPN.exe -PassThru
  Start-Sleep 8
  if ($g.HasExited) { $gui = 1; Log "FAIL: GUI вышел за 8с (code=$($g.ExitCode))" }
  else { Log "PASS: GUI жив 8с"; try { $g.Kill() } catch {} }
} else { Log "WARN: DualVPN.exe нет"; $gui = 1 }

$total = 0
if ($socks -ne 0) { $total++ }
if ($tun -ne 0) { $total++ }
$total += $gui
Log "=== ИТОГ: socks=$socks tun=$tun gui=$gui provalov=$total ==="
"OVERALL_EXIT=$total" | Out-File -Append -Encoding ascii $result
Start-Sleep 3
Stop-Computer -Force
```

- [ ] **Step 4: Проверить XML/структуру**

Run:
```bash
cd /home/ub/dualvpn
python3 -c "import xml.dom.minidom; xml.dom.minidom.parse('test/e2e/vm/windows/autounattend.xml'); print('autounattend.xml well-formed')"
grep -q 'BypassTPMCheck' test/e2e/vm/windows/autounattend.xml && grep -q '/IMAGE/INDEX' test/e2e/vm/windows/autounattend.xml && echo "bypass+edition OK"
grep -q '10.0.2.2:4443' test/e2e/vm/windows/config.toml && grep -q '10.0.2.2:4444' test/e2e/vm/windows/config.toml && echo "endpoints OK"
test -f test/e2e/vm/windows/provision.ps1 && echo "provision.ps1 есть"
```
Expected: `well-formed`, `bypass+edition OK`, `endpoints OK`, `provision.ps1 есть`.

- [ ] **Step 5: Commit**

```bash
cd /home/ub/dualvpn
git add -f test/e2e/vm/windows/config.toml
git add test/e2e/vm/windows/autounattend.xml test/e2e/vm/windows/provision.ps1
git commit -m "test(e2e): Windows 11 autounattend (GPT+LabConfig bypass) + гостевой провижн"
```

---

### Task 2: Хостовая подготовка — exe, data-ISO, FAT result-disk, install-qcow2, OVMF vars

**Files:**
- Create: `test/e2e/vm/windows/prepare-win.sh`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: `autounattend.xml`, `provision.ps1`, `config.toml` (Task 1), Go-исходники, `build/windows/deps/wintun.dll`, `/usr/share/OVMF/OVMF_VARS_4M.fd`.
- Produces: в `work/`: `data.iso` (корень: autounattend.xml, provision.ps1, config.toml, dualvpn-harness.exe, wintun.dll, DualVPN.exe), `result.img` (FAT, том `RESULT`, маркер `RESULTDISK.marker`), `win.qcow2` (пустой install-диск), `OVMF_VARS.fd` (записываемая копия).

- [ ] **Step 1: prepare-win.sh**

Создать `test/e2e/vm/windows/prepare-win.sh`:
```bash
#!/usr/bin/env bash
# Хостовая подготовка Windows 11 VM: exe, data-ISO (autounattend в корне),
# FAT-диск результата, install-qcow2, записываемая копия OVMF_VARS.
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../../../.. && pwd)"
WORK="$(pwd)/work"
STAGE="$WORK/stage"
export PATH="/usr/local/go/bin:$PATH"

mkdir -p "$WORK" "$STAGE"
rm -f "$STAGE"/* 2>/dev/null || true

echo "==> сборка Windows-бинарей"
( cd "$ROOT" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o "$STAGE/dualvpn-harness.exe" ./cmd/dualvpn-harness/ )
( cd "$ROOT" && GOFLAGS="-tags=webkit2_41" make build-windows >/dev/null && cp bin/DualVPN.exe "$STAGE/" ) || echo "WARN: DualVPN.exe не собран (GUI-smoke пропустится)"
cp "$ROOT/build/windows/deps/wintun.dll" "$STAGE/"
cp autounattend.xml provision.ps1 config.toml "$STAGE/"

echo "==> data-ISO (autounattend в корне)"
xorriso -as mkisofs -output "$WORK/data.iso" -volid DUALVPN -joliet -rock "$STAGE"/ >/dev/null 2>&1

echo "==> FAT-диск результата (том RESULT + маркер)"
rm -f "$WORK/result.img"; truncate -s 64M "$WORK/result.img"; mkfs.vfat -n RESULT "$WORK/result.img" >/dev/null
MNT="$WORK/rmnt"; mkdir -p "$MNT"
sudo mount -o loop "$WORK/result.img" "$MNT"
echo "result-disk" | sudo tee "$MNT/RESULTDISK.marker" >/dev/null
sudo umount "$MNT"; rmdir "$MNT"

echo "==> install-диск (пустой qcow2, до 32G)"
rm -f "$WORK/win.qcow2"; qemu-img create -f qcow2 "$WORK/win.qcow2" 32G >/dev/null

echo "==> записываемая копия OVMF_VARS"
cp /usr/share/OVMF/OVMF_VARS_4M.fd "$WORK/OVMF_VARS.fd"

echo "==> готово: $WORK"
```
Сделать исполняемым: `chmod +x test/e2e/vm/windows/prepare-win.sh`.

- [ ] **Step 2: .gitignore**

В `.gitignore` добавить:
```
# E2E Windows-VM: генерируемые артефакты
test/e2e/vm/windows/work/
```

- [ ] **Step 3: Прогнать и проверить**

Run:
```bash
cd /home/ub/dualvpn && test/e2e/vm/windows/prepare-win.sh
xorriso -indev test/e2e/vm/windows/work/data.iso -find / -maxdepth 1 2>/dev/null | tr -d "'" | sort
file test/e2e/vm/windows/work/result.img
qemu-img info test/e2e/vm/windows/work/win.qcow2 | grep -E 'file format|virtual size'
ls -la test/e2e/vm/windows/work/OVMF_VARS.fd | awk '{print $NF, $5}'
```
Expected: в `data.iso` — `/autounattend.xml`, `/provision.ps1`, `/config.toml`, `/dualvpn-harness.exe`, `/wintun.dll`, `/DualVPN.exe`; `result.img` — FAT; `win.qcow2` qcow2 32G; `OVMF_VARS.fd` ~540 КБ.

- [ ] **Step 4: Commit**

```bash
cd /home/ub/dualvpn
git add test/e2e/vm/windows/prepare-win.sh .gitignore
git commit -m "test(e2e): подготовка Windows 11 VM (exe, data-ISO, FAT result-disk, qcow2, OVMF vars)"
```

---

### Task 3: Оркестрация — boot QEMU(OVMF), sendkey, результат, `make e2e-win-vm`

**Files:**
- Create: `test/e2e/vm/windows/run-win.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `prepare-win.sh` (Task 2), бэкенд ocserv, `work/{data.iso,result.img,win.qcow2,OVMF_VARS.fd}`, `WIN_ISO`.
- Produces: `make e2e-win-vm` — полный автоматический прогон, exit-код = `OVERALL_EXIT` из гостя.

- [ ] **Step 1: run-win.sh**

Создать `test/e2e/vm/windows/run-win.sh`:
```bash
#!/usr/bin/env bash
# E2E внутри настоящей Windows 11 Pro VM: ocserv (docker) + QEMU/KVM(OVMF) гость.
# Автоустановка через autounattend (обход TPM/SecureBoot), harness socks5+tun, GUI-smoke.
set -uo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../../../.. && pwd)"
OCS="$ROOT/test/e2e/backends/ocserv"
WORK="$(pwd)/work"
WIN_ISO="${WIN_ISO:-/mnt/Data-2/Distr/Microsoft Windows 11 [10.0.26100.8457], Version 24H2 (Updated May 2026) - Оригинальные образы от Microsoft MSDN [Ru]/ru-ru_windows_11_consumer_editions_version_24h2_updated_may_2026_x64_dvd_d061a709.iso}"
BOOT_TIMEOUT="${BOOT_TIMEOUT:-3300}"   # ~55 мин на установку+провижн
MON="$WORK/mon.sock"
export PATH="/usr/local/go/bin:$PATH"

cleanup() {
  sudo pkill -f "file=$WORK/win.qcow2" 2>/dev/null || true
  "$OCS/down.sh" >/dev/null 2>&1 || true
}
trap cleanup EXIT

[[ -f "$WIN_ISO" ]] || { echo "нет Windows ISO: $WIN_ISO (задай WIN_ISO=...)" >&2; exit 1; }

echo "==> подготовка артефактов Windows-VM"
./prepare-win.sh
echo "==> ocserv up"; "$OCS/up.sh" >/dev/null

# Снятие блокера UEFI "Press any key to boot from CD": шлём Enter в монитор
# первые ~150с, пока не начнётся автоустановка. socat подключается к unix-сокету.
rm -f "$MON"
sendkeys() {
  for _ in $(seq 1 75); do
    [[ -S "$MON" ]] && printf 'sendkey ret\n' | socat - "UNIX-CONNECT:$MON" 2>/dev/null
    sleep 2
  done
}

echo "==> запуск QEMU(OVMF, q35, AHCI, e1000); установка Win11 + провижн (до ${BOOT_TIMEOUT}s)"
sendkeys &
SK_PID=$!
sudo timeout "$BOOT_TIMEOUT" qemu-system-x86_64 \
  -enable-kvm -machine q35 -m 4096 -smp 4 -display none \
  -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
  -drive if=pflash,format=raw,file="$WORK/OVMF_VARS.fd" \
  -drive file="$WORK/win.qcow2",if=none,id=disk0,format=qcow2 \
  -device ich9-ahci,id=ahci -device ide-hd,drive=disk0,bus=ahci.0 \
  -drive file="$WIN_ISO",if=none,id=wincd,format=raw,media=cdrom,readonly=on \
  -device ide-cd,drive=wincd,bus=ahci.1 \
  -drive file="$WORK/data.iso",if=none,id=datacd,format=raw,media=cdrom,readonly=on \
  -device ide-cd,drive=datacd,bus=ahci.2 \
  -drive file="$WORK/result.img",if=none,id=resdisk,format=raw \
  -device ide-hd,drive=resdisk,bus=ahci.3 \
  -netdev user,id=n0 -device e1000,netdev=n0 \
  -monitor "unix:$MON,server,nowait" \
  -serial file:"$WORK/console.log" || true
kill "$SK_PID" 2>/dev/null || true

echo "==> читаю результат с FAT-диска"
MNT="$WORK/rmnt"; mkdir -p "$MNT"
sudo mount -o loop "$WORK/result.img" "$MNT" 2>/dev/null || true
if [[ -f "$MNT/result.txt" ]]; then
  cat "$MNT/result.txt"
  rc=$(sed -n 's/^OVERALL_EXIT=//p' "$MNT/result.txt" | tail -1)
  sudo umount "$MNT" 2>/dev/null || true; rmdir "$MNT" 2>/dev/null || true
  echo "==> OVERALL_EXIT=${rc:-нет}"; exit "${rc:-1}"
else
  sudo umount "$MNT" 2>/dev/null || true; rmdir "$MNT" 2>/dev/null || true
  echo "FAIL: гость не оставил result.txt (см. $WORK/console.log — установка могла не дойти)"; exit 1
fi
```
Сделать исполняемым: `chmod +x test/e2e/vm/windows/run-win.sh`.

- [ ] **Step 2: Makefile-цель**

В `Makefile` добавить:
```makefile
.PHONY: e2e-win-vm
e2e-win-vm: ## E2E внутри настоящей Windows 11 VM (autounattend + harness + GUI-smoke)
	@test/e2e/vm/windows/run-win.sh
```

- [ ] **Step 3: Полный прогон (живой, ~30–55 мин)**

Run:
```bash
cd /home/ub/dualvpn && make e2e-win-vm 2>&1 | tail -60
```
Expected один из зафиксированных исходов:
- **PASS** — Win11 установилась, провижн отработал: harness SOCKS5 и TUN дали `exit 0` (оба туннеля + связность + изоляция), GUI-smoke прожил 8с; `OVERALL_EXIT=0`, `make e2e-win-vm` завершается `0`.
- **Документированная находка** — конкретный шаг не доехал. Снять причину из `work/console.log` (ход установки) и, если гость дошёл до провижна, из `result.txt`/`harness-*.log`. Записать в спеку (VM-раздел). Типовые точки: sendkey не снял «Press any key» (тайминг) → установка не стартовала; e1000 без сети на Win11 (Microsoft мог убрать in-box драйвер) → harness не видит ocserv (fallback: virtio-net + инъекция virtio-win драйверов); autounattend отвергнут/не та редакция; GPT/InstallTo не совпали; GUI-smoke упал (маловероятно — Win11 несёт WebView2). Ядро стенда (`make e2e`, milestone) от Windows-слоя не зависит.

Ключевые точки проверки при разборе (назвать, что подтвердилось): «Press any key» снят и Setup стартовал; LabConfig-обход прошёл (Win11 не отверг железо); авто-логон + FirstLogonCommand запустили provision.ps1; гость нашёл артефакт-CD и FAT-диск; e1000 дал сеть и harness достаёт `10.0.2.2:4443`.

- [ ] **Step 4: Commit**

```bash
cd /home/ub/dualvpn
git add test/e2e/vm/windows/run-win.sh Makefile
git commit -m "test(e2e): make e2e-win-vm — DualVPN в настоящей Windows 11 VM (OVMF, AHCI/e1000, sendkey)"
```

---

## Self-Review

**Покрытие цели:**
- «Полностью автоматическая установка Win11 Pro» → Task 1 (autounattend: GPT, LabConfig-обход TPM/SecureBoot, /IMAGE/INDEX 4, GVLK Pro, auto-logon), Task 3 (OVMF UEFI, sendkey против press-any-key).
- «harness SOCKS5 + Wintun-TUN против ocserv» → Task 1 (provision.ps1), config 10.0.2.2.
- «GUI-smoke» → Task 1 (provision.ps1, DualVPN.exe 8с).
- «встроенные драйверы, без virtio» → Task 3 (AHCI + e1000).
- «артефакты в гость / результат из гостя» → Task 2 (data-ISO + FAT result.img), Task 3 (loop-mount).
- «make e2e-win-vm + teardown» → Task 3.

**Плейсхолдеры:** боевых пропусков нет. Значения конкретны: путь Win11 ISO, редакция index 4 (`Windows 11 Pro`), GVLK Win11 Pro `W269N-WFGWX-YVC9B-4J6C9-T83GX`, OVMF пути, GPT-разметка, LabConfig-ключи. Эмпирические риски (press-any-key тайминг, e1000-драйвер на Win11, autounattend-совместимость, длительность) помечены как точки разбора в Task 3 — при провале документируемая находка, ядро стенда не затрагивается.

**Согласованность:** `config.toml` совпадает с ocserv-бэкендом (10.0.2.2:4443/4444, порты 21080/21081, probe 192.168.90.10/.91.10). Маркеры поиска: `provision.ps1` в корне data-ISO (его ищут FirstLogonCommand и provision), `RESULTDISK.marker` на FAT-диске (том `RESULT`). AHCI-шины: 0=win.qcow2, 1=Win ISO, 2=data.iso, 3=result.img. OVMF_VARS — записываемая копия в work/. `OVERALL_EXIT` пишется гостем, читается run-win.sh.

**Вне рамок:** NSIS-инсталлятор в госте (ставим бинари напрямую), asav-бэкенд, Windows Server / клиент на BIOS (выбран Win11/UEFI по запросу).
