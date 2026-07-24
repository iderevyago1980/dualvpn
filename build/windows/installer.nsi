; DualVPN — установщик для Windows (NSIS).
;
; Ставится per-user (в %LOCALAPPDATA%), без прав администратора — под
; философию SOCKS5-режима, который работает без админа. На Windows сейчас
; активен именно SOCKS5-режим; wintun.dll кладётся рядом с exe на будущее
; (TUN-адаптер для Windows ещё не реализован).
;
; Собирается кросс-компиляцией на Linux:
;   makensis -DAPPVERSION=x.y.z -DSRCROOT=/abs/path/to/repo installer.nsi
; Значения по умолчанию заданы ниже, так что можно и просто `makensis installer.nsi`
; из каталога build/windows после `make build-windows`.

Unicode true

!ifndef APPVERSION
  !define APPVERSION "1.7.0"
!endif
!ifndef SRCROOT
  ; По умолчанию — корень репозитория относительно этого скрипта
  !define SRCROOT "..\.."
!endif

!define APPNAME "DualVPN"
!define COMPANY "DualVPN"
!define EXENAME "DualVPN.exe"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}"

!include "MUI2.nsh"

Name "${APPNAME} ${APPVERSION}"
OutFile "${SRCROOT}\bin\DualVPN-Setup-${APPVERSION}.exe"

; Per-user установка — не требует прав администратора
RequestExecutionLevel user
InstallDir "$LOCALAPPDATA\${APPNAME}"
InstallDirRegKey HKCU "Software\${APPNAME}" "InstallDir"
SetCompressor /SOLID lzma

VIProductVersion "${APPVERSION}.0"
VIAddVersionKey "ProductName" "${APPNAME}"
VIAddVersionKey "FileVersion" "${APPVERSION}.0"
VIAddVersionKey "ProductVersion" "${APPVERSION}.0"
VIAddVersionKey "CompanyName" "${COMPANY}"
VIAddVersionKey "FileDescription" "DualVPN — одновременное подключение к двум Cisco AnyConnect VPN"
VIAddVersionKey "LegalCopyright" "${COMPANY}"

; --- Интерфейс ---
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXENAME}"
!define MUI_FINISHPAGE_RUN_TEXT "Запустить ${APPNAME}"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

; Русский интерфейс (fallback — английский)
!insertmacro MUI_LANGUAGE "Russian"
!insertmacro MUI_LANGUAGE "English"

; --- Установка ---
Section "DualVPN (обязательно)" SecMain
  SectionIn RO
  SetOutPath "$INSTDIR"

  File "${SRCROOT}\bin\${EXENAME}"
  File "${SRCROOT}\config.example.toml"
  File "/oname=wintun.dll" "${SRCROOT}\build\windows\deps\wintun\bin\amd64\wintun.dll"
  File "/oname=README.txt" "${SRCROOT}\build\windows\README.md"

  ; Ярлыки
  CreateDirectory "$SMPROGRAMS\${APPNAME}"
  CreateShortcut "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk" "$INSTDIR\${EXENAME}"
  CreateShortcut "$SMPROGRAMS\${APPNAME}\Удалить ${APPNAME}.lnk" "$INSTDIR\uninstall.exe"
  CreateShortcut "$DESKTOP\${APPNAME}.lnk" "$INSTDIR\${EXENAME}"

  ; Реестр: путь установки + запись в «Установка и удаление программ»
  WriteRegStr HKCU "Software\${APPNAME}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${APPNAME}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${APPVERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "${COMPANY}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${EXENAME}"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" "$INSTDIR\uninstall.exe"
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1

  WriteUninstaller "$INSTDIR\uninstall.exe"
SectionEnd

; --- Удаление ---
Section "Uninstall"
  ; config.toml создаётся приложением при первом запуске — удаляем осознанно
  Delete "$INSTDIR\${EXENAME}"
  Delete "$INSTDIR\wintun.dll"
  Delete "$INSTDIR\config.example.toml"
  Delete "$INSTDIR\config.toml"
  Delete "$INSTDIR\README.txt"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk"
  Delete "$SMPROGRAMS\${APPNAME}\Удалить ${APPNAME}.lnk"
  RMDir "$SMPROGRAMS\${APPNAME}"
  Delete "$DESKTOP\${APPNAME}.lnk"

  DeleteRegKey HKCU "${UNINST_KEY}"
  DeleteRegKey HKCU "Software\${APPNAME}"
SectionEnd
