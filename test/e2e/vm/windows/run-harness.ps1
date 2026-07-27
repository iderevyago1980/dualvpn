# Гостевой прогон DualVPN-harness на Windows (запускается через RunOnce на готовом
# образе, см. run-ready.sh). Включает offline-диски, гоняет оба режима против
# ocserv, пишет result.txt (и hs/ht.log|err) в C:\dvlab, выключает VM.
$ErrorActionPreference='Continue'
try {
  Get-Disk | Where-Object {$_.IsOffline}  | Set-Disk -IsOffline $false  -ErrorAction SilentlyContinue
  Get-Disk | Where-Object {$_.IsReadOnly} | Set-Disk -IsReadOnly $false -ErrorAction SilentlyContinue
  Start-Sleep 5
} catch {}

# Диск результата (FAT, по маркеру) если получил букву; иначе C:\dvlab.
$res = $null
foreach ($d in 68..90) { $l = [char]$d; if (Test-Path ("${l}:\RESULTDISK.marker")) { $res = "${l}:"; break } }
if (-not $res) { $res = 'C:\dvlab' }
$out = "$res\result.txt"
"" | Out-File -Encoding ascii $out
function Log($m) { $t = (Get-Date -Format HH:mm:ss); "[$t] $m" | Tee-Object -Append $out }

Log "res=$res"
Log "=== harness SOCKS5 ==="
$p = Start-Process C:\dvlab\dualvpn-harness.exe -ArgumentList '-config','C:\dvlab\config.toml','-mode','socks5','-insecure','-timeout','40s' -Wait -NoNewWindow -PassThru -RedirectStandardOutput "$res\hs.log" -RedirectStandardError "$res\hs.err"
$socks = $p.ExitCode; Log "SOCKS5 exit=$socks"

Log "=== harness TUN (Wintun) ==="
$p = Start-Process C:\dvlab\dualvpn-harness.exe -ArgumentList '-config','C:\dvlab\config.toml','-mode','tun','-insecure','-timeout','40s' -Wait -NoNewWindow -PassThru -RedirectStandardOutput "$res\ht.log" -RedirectStandardError "$res\ht.err"
$tun = $p.ExitCode; Log "TUN exit=$tun"

$total = 0
if ($socks -ne 0) { $total++ }
if ($tun -ne 0) { $total++ }
Log "=== ITOG: socks=$socks tun=$tun ==="
"OVERALL_EXIT=$total" | Out-File -Append -Encoding ascii $out
Start-Sleep 3
Stop-Computer -Force
