@echo -off
echo DualVPN: UEFI startup — chainload Windows...
rem Windows уже установлен на диск -> его загрузчик.
for %d in fs0 fs1 fs2 fs3 fs4 fs5 fs6 fs7
  if exist %d:\EFI\Microsoft\Boot\bootmgfw.efi then
    echo boot installed Windows from %d:
    %d:\EFI\Microsoft\Boot\bootmgfw.efi
  endif
endfor
rem иначе установочный носитель (boot.wim на CD).
for %v in fs0 fs1 fs2 fs3 fs4 fs5 fs6 fs7
  if exist %v:\sources\boot.wim then
    echo boot Windows Setup from %v:
    %v:\efi\boot\bootx64.efi
  endif
endfor
rem Без reset — не зацикливаемся. Если сюда дошли, firmware не смог загрузить
rem установочный носитель (см. «Журнал расхождений» в спеке про OVMF+Win11 ISO).
echo no bootable Windows loader could be launched from shell
