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
