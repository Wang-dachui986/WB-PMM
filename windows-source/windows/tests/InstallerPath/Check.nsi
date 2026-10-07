!include "${__FILEDIR__}\..\..\installer\NormalizeInstallPath.nsh"
!ifndef TEST_OUT
  !error "TEST_OUT is required"
!endif

Name "WB-PMM installer path regression"
OutFile "${TEST_OUT}"
Unicode True
RequestExecutionLevel user
SilentInstall silent
Var PreviousInstallDir

Section
  ; Reserve a unique name, then remove it to reproduce a fresh installation.
  ClearErrors
  GetTempFileName $R2
  IfErrors Failed
  Delete "$R2"
  IfFileExists "$R2" Failed

  StrCpy $R3 "$R2\Programs\WB-PMM"
  StrCpy $INSTDIR $R3
  !insertmacro NormalizeInstallPath $INSTDIR Failed
  StrCmp $INSTDIR $R3 0 Failed
  IfFileExists "$R2" Failed

  StrCpy $INSTDIR "$R2\中文 用户\..\Programs\WB-PMM"
  !insertmacro NormalizeInstallPath $INSTDIR Failed
  StrCmp $INSTDIR $R3 0 Failed
  IfFileExists "$R2" Failed

  StrCpy $PreviousInstallDir "$R2\中文 用户\WB-PMM"
  !insertmacro NormalizeInstallPath $PreviousInstallDir Failed
  StrCmp $PreviousInstallDir "$R2\中文 用户\WB-PMM" 0 Failed
  IfFileExists "$R2" Failed

  StrCpy $INSTDIR "$TEMP"
  !insertmacro NormalizeInstallPath $INSTDIR Failed
  IfFileExists "$INSTDIR\." 0 Failed

  StrCpy $INSTDIR ""
  !insertmacro NormalizeInstallPath $INSTDIR EmptyRejected
  Goto Failed
EmptyRejected:
  SetErrorLevel 0
  Quit
Failed:
  SetErrorLevel 1
  Quit
SectionEnd
