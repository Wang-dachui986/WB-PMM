!ifndef WBPMM_NORMALIZE_INSTALL_PATH
!define WBPMM_NORMALIZE_INSTALL_PATH

!macro NormalizeInstallPath path failure
  ; NSIS GetFullPathName can clear its output when the destination does not exist.
  ; The Win32 API normalizes new paths without creating directories before validation.
  StrCmp "${path}" "" ${failure}
  System::Call 'kernel32::GetFullPathNameW(w "${path}", i ${NSIS_MAX_STRLEN}, w .r0, p 0) i .r1'
  IntCmp $1 0 ${failure} ${failure} 0
  IntCmp $1 ${NSIS_MAX_STRLEN} ${failure} 0 ${failure}
  StrCpy ${path} $0
!macroend

!endif
