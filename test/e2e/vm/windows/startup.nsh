@echo -off
echo DualVPN: chainload Windows Setup (cdboot_noprompt)...
for %v in fs0 fs1 fs2 fs3 fs4 fs5 fs6 fs7
  if exist %v:\efi\microsoft\boot\cdboot_noprompt.efi then
    echo booting from %v:
    %v:\efi\microsoft\boot\cdboot_noprompt.efi
  endif
endfor
echo cdboot_noprompt.efi NOT FOUND on any fs
