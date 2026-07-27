@echo -off
echo DualVPN: UEFI startup
for %g in fs0 fs1 fs2 fs3 fs4 fs5
  if exist %g:\dvtried.flag then
    echo already tried once, dropping to shell
    goto DONE
  endif
endfor
for %v in fs0 fs1 fs2 fs3 fs4 fs5
  if exist %v:\sources\boot.wim then
    if exist %v:\efi\boot\bootx64.efi then
      for %r in fs0 fs1 fs2 fs3 fs4 fs5
        if exist %r:\RESULTDISK.marker then
          echo tried > %r:\dvtried.flag
        endif
      endfor
      echo add boot entry for Windows Setup on %v: and reset
      bcfg boot add 0 %v:\efi\boot\bootx64.efi "Windows Setup"
      reset
    endif
  endif
endfor
:DONE
echo startup.nsh finished
