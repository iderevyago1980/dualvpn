; DualVPN — установщик для всех пользователей машины (NSIS).
;
; Отличие от per-user установщика (installer.nsi): ставится в Program Files
; и регистрирует службу DualVPN. Служба работает под LocalSystem и держит
; TUN-туннели, поэтому после установки обычный пользователь поднимает VPN
; в режиме TUN без прав администратора. Права нужны один раз — здесь.
;
; Каталог установки защищён от записи обычным пользователем намеренно: иначе
; подменённый exe исполнялся бы с правами системы.
;
; Собирается:
;   makensis -DAPPVERSION=x.y.z -DSRCROOT=/abs/path/to/repo installer-machine.nsi
;
; Файл обязан быть в UTF-8 С BOM: без него makensis читает его как ANSI и
; калечит русские строки (см. CLAUDE.md).

Unicode true

!ifndef APPVERSION
  !define APPVERSION "1.10.1"
!endif
!ifndef SRCROOT
  !define SRCROOT "..\.."
!endif

!define APPNAME "DualVPN"
!define COMPANY "DualVPN"
!define EXENAME "DualVPN.exe"
!define SVCNAME "dualvpn-service.exe"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}"

!include "MUI2.nsh"
!include "x64.nsh"

Name "${APPNAME} ${APPVERSION} (для всех пользователей)"
OutFile "${SRCROOT}\bin\DualVPN-Setup-${APPVERSION}-machine.exe"

; Установка для всех пользователей — нужны права администратора
RequestExecutionLevel admin
InstallDir "$PROGRAMFILES64\${APPNAME}"
InstallDirRegKey HKLM "Software\${APPNAME}" "InstallDir"
SetCompressor /SOLID lzma

VIProductVersion "${APPVERSION}.0"
VIAddVersionKey "ProductName" "${APPNAME}"
VIAddVersionKey "FileVersion" "${APPVERSION}.0"
VIAddVersionKey "CompanyName" "${COMPANY}"
VIAddVersionKey "FileDescription" "Установщик ${APPNAME} (служба, для всех пользователей)"
VIAddVersionKey "LegalCopyright" "${COMPANY}"

!define MUI_ABORTWARNING
!define MUI_ICON "${SRCROOT}\build\windows\icon.ico"
!define MUI_UNICON "${SRCROOT}\build\windows\icon.ico"
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXENAME}"
!define MUI_FINISHPAGE_RUN_TEXT "Запустить ${APPNAME}"
!define MUI_FINISHPAGE_TEXT "Служба ${APPNAME} установлена и запущена.$\r$\n$\r$\nТеперь режим TUN доступен обычному пользователю — права администратора больше не нужны."

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "Russian"
!insertmacro MUI_LANGUAGE "English"

; --- Установка ---
Section "DualVPN со службой (обязательно)" SecMain
  SectionIn RO
  SetOutPath "$INSTDIR"

  ; Служба может быть уже запущена от прошлой установки: останавливаем и
  ; удаляем, иначе файл занят и обновление не встанет.
  nsExec::ExecToLog '"$INSTDIR\${SVCNAME}" uninstall'
  Pop $0

  File "${SRCROOT}\bin\${EXENAME}"
  File "${SRCROOT}\bin\${SVCNAME}"
  File "${SRCROOT}\config.example.toml"
  ; wintun.dll грузится через LoadLibraryEx с SEARCH_APPLICATION_DIR, поэтому
  ; обязан лежать рядом с exe службы — она создаёт TUN-адаптер.
  File "/oname=wintun.dll" "${SRCROOT}\build\windows\deps\wintun\bin\amd64\wintun.dll"
  File "/oname=README.txt" "${SRCROOT}\build\windows\README.md"

  ; Регистрация и запуск службы
  nsExec::ExecToLog '"$INSTDIR\${SVCNAME}" install'
  Pop $0
  ${If} $0 != 0
    MessageBox MB_ICONEXCLAMATION|MB_OK "Не удалось установить службу ${APPNAME} (код $0).$\r$\n$\r$\nПриложение установлено и будет работать в режиме SOCKS5; режим TUN потребует запуска от администратора."
  ${EndIf}

  ; Ярлыки для всех пользователей
  SetShellVarContext all
  CreateDirectory "$SMPROGRAMS\${APPNAME}"
  CreateShortcut "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk" "$INSTDIR\${EXENAME}"
  CreateShortcut "$SMPROGRAMS\${APPNAME}\Удалить ${APPNAME}.lnk" "$INSTDIR\uninstall.exe"
  CreateShortcut "$DESKTOP\${APPNAME}.lnk" "$INSTDIR\${EXENAME}"

  ; Реестр машины: путь установки + запись в «Установка и удаление программ»
  WriteRegStr HKLM "Software\${APPNAME}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayName" "${APPNAME} (для всех пользователей)"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayVersion" "${APPVERSION}"
  WriteRegStr HKLM "${UNINST_KEY}" "Publisher" "${COMPANY}"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${EXENAME}"
  WriteRegStr HKLM "${UNINST_KEY}" "UninstallString" "$INSTDIR\uninstall.exe"
  WriteRegStr HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoRepair" 1

  WriteUninstaller "$INSTDIR\uninstall.exe"
SectionEnd

; --- Удаление ---
Section "Uninstall"
  ; Служба снимается первой: она держит адаптеры, маршруты и правила DNS,
  ; и её остановка возвращает систему в исходное состояние.
  nsExec::ExecToLog '"$INSTDIR\${SVCNAME}" uninstall'
  Pop $0

  Delete "$INSTDIR\${EXENAME}"
  Delete "$INSTDIR\${SVCNAME}"
  Delete "$INSTDIR\wintun.dll"
  Delete "$INSTDIR\config.example.toml"
  Delete "$INSTDIR\README.txt"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  SetShellVarContext all
  Delete "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk"
  Delete "$SMPROGRAMS\${APPNAME}\Удалить ${APPNAME}.lnk"
  RMDir "$SMPROGRAMS\${APPNAME}"
  Delete "$DESKTOP\${APPNAME}.lnk"

  DeleteRegKey HKLM "${UNINST_KEY}"
  DeleteRegKey HKLM "Software\${APPNAME}"
SectionEnd
