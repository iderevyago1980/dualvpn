<#
.SYNOPSIS
Подписывает сборки DualVPN сертификатом Authenticode.

.DESCRIPTION
signtool.exe из Windows SDK не нужен: Set-AuthenticodeSignature входит в
PowerShell. Метка времени обязательна — без неё подпись перестаёт считаться
действительной в день истечения сертификата, а с ней остаётся валидной для
файлов, подписанных до этой даты.

Сертификат ищется в хранилище текущего пользователя по отпечатку. Ключ из
хранилища не извлекается и в репозиторий не попадает.

.PARAMETER Thumbprint
Отпечаток сертификата в Cert:\CurrentUser\My. По умолчанию берётся из
переменной окружения DUALVPN_SIGN_THUMBPRINT.

.PARAMETER Path
Файлы для подписи. По умолчанию — все exe в bin\.

.PARAMETER CreateSelfSigned
Создать самоподписанный сертификат (на 3 года) и подписать им. Такая подпись
действительна только там, где сертификат установлен в доверенные корневые
центры и доверенные издатели: Windows не знает этот корень. Для раздачи
посторонним нужен коммерческий OV/EV-сертификат либо Azure Trusted Signing —
Smart App Control и SmartScreen смотрят на репутацию издателя, а не на факт
наличия подписи.

.EXAMPLE
build\windows\sign.ps1 -CreateSelfSigned
.EXAMPLE
build\windows\sign.ps1 -Thumbprint A97246B7... -Path bin\DualVPN.exe
#>
[CmdletBinding()]
param(
    [string]   $Thumbprint = $env:DUALVPN_SIGN_THUMBPRINT,
    [string[]] $Path,
    [switch]   $CreateSelfSigned,
    [string]   $TimestampServer = 'http://timestamp.digicert.com',
    # Куда выгрузить публичную часть сертификата (нужна пользователям для
    # проверки подписи). Приватный ключ не экспортируется.
    [string]   $ExportCer
)

$ErrorActionPreference = 'Stop'
$root = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent

if (-not $Path) {
    $Path = Get-ChildItem (Join-Path $root 'bin') -Filter *.exe -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -notlike '*.test.exe' } |
            Select-Object -ExpandProperty FullName
}
if (-not $Path) { throw 'нечего подписывать: не найдены exe (соберите проект)' }

if ($CreateSelfSigned) {
    $cert = New-SelfSignedCertificate -Type CodeSigningCert `
        -Subject 'CN=DualVPN (self-signed), O=DualVPN' `
        -FriendlyName 'DualVPN code signing (self-signed)' `
        -CertStoreLocation Cert:\CurrentUser\My `
        -KeyAlgorithm RSA -KeyLength 3072 -HashAlgorithm SHA256 `
        -NotAfter (Get-Date).AddYears(3)
    Write-Host "создан сертификат: $($cert.Subject)"
    Write-Host "отпечаток:         $($cert.Thumbprint)  (сохраните: он нужен для следующих подписей)"
} else {
    if (-not $Thumbprint) {
        throw 'не указан -Thumbprint (или DUALVPN_SIGN_THUMBPRINT); для первого раза: -CreateSelfSigned'
    }
    $cert = Get-ChildItem "Cert:\CurrentUser\My\$Thumbprint" -ErrorAction SilentlyContinue
    if (-not $cert) { throw "сертификат $Thumbprint не найден в Cert:\CurrentUser\My" }
}

foreach ($file in $Path) {
    $sig = Set-AuthenticodeSignature -FilePath $file -Certificate $cert `
        -HashAlgorithm SHA256 -TimestampServer $TimestampServer
    if ($sig.SignerCertificate.Thumbprint -ne $cert.Thumbprint) {
        throw "подпись $file не наложена: $($sig.StatusMessage)"
    }
    if (-not $sig.TimeStamperCertificate) { throw "нет метки времени для $file" }
    # Status = NotTrusted/UnknownError ожидаем для самоподписанного сертификата:
    # цепочка не доверена, пока корень не установлен на машине проверки.
    Write-Host ("подписан: {0}  [{1}]" -f (Split-Path $file -Leaf), $sig.Status)
}

if ($ExportCer) {
    Export-Certificate -Cert $cert -FilePath $ExportCer -Type CERT | Out-Null
    Write-Host "публичная часть сертификата: $ExportCer"
}
