!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "WinVer.nsh"
!include "x64.nsh"
!include "FileFunc.nsh"
!include "${__FILEDIR__}\NormalizeInstallPath.nsh"

!ifndef APP_DIR
  !error "APP_DIR is required"
!endif
!ifndef WEBVIEW2_BOOTSTRAPPER
  !error "WEBVIEW2_BOOTSTRAPPER is required"
!endif
!ifndef SETUP_OUT
  !error "SETUP_OUT is required"
!endif
!ifndef PAYLOAD_MANIFEST
  !error "PAYLOAD_MANIFEST is required"
!endif
!ifndef APP_VERSION
  !define APP_VERSION "1.0.10"
!endif
!include "${PAYLOAD_MANIFEST}"

!define PRODUCT_KEY "Software\WB-PMM"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\WB-PMM"
!define WEBVIEW2_USER "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
!define WEBVIEW2_MACHINE "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

Name "WB-PMM ${APP_VERSION}"
OutFile "${SETUP_OUT}"
Unicode True
RequestExecutionLevel user
SetCompressor /SOLID lzma
InstallDir "$LOCALAPPDATA\Programs\WB-PMM"
ShowInstDetails show
ShowUninstDetails show
Var PreviousInstallDir

!insertmacro MUI_PAGE_WELCOME
!define MUI_DIRECTORYPAGE_TEXT_TOP "请选择 WB-PMM 的安装位置。账号和设置单独保存，升级不会丢失。已有安装默认沿用原目录；更换目录请先卸载旧版（保留数据）。"
!define MUI_PAGE_CUSTOMFUNCTION_LEAVE ValidateInstallDirectory
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"

!macro CheckInstallPath prefix
  !insertmacro NormalizeInstallPath $INSTDIR ${prefix}InvalidPath
  StrLen $0 "$INSTDIR"
  IntCmp $0 3 ${prefix}InvalidPath ${prefix}InvalidPath 0
  StrCpy $0 "$INSTDIR" 2 1
  StrCmp $0 ":\" 0 ${prefix}InvalidPath
  StrCmp $INSTDIR "$PROFILE" ${prefix}InvalidPath
  StrCmp $INSTDIR "$DESKTOP" ${prefix}InvalidPath
  StrCmp $INSTDIR "$DOCUMENTS" ${prefix}InvalidPath
  StrCmp $INSTDIR "$APPDATA" ${prefix}InvalidPath
  StrCmp $INSTDIR "$LOCALAPPDATA" ${prefix}InvalidPath
  StrCmp $INSTDIR "$PROGRAMFILES64" ${prefix}InvalidPath
  StrCmp $INSTDIR "$PROGRAMFILES32" ${prefix}InvalidPath
  StrCpy $1 "$LOCALAPPDATA\WB-PMM"
  StrLen $2 $1
  StrCpy $0 "$INSTDIR" $2
  StrCmp $0 $1 0 ${prefix}CheckWindows
  StrCpy $0 "$INSTDIR" 1 $2
  StrCmp $0 "" ${prefix}InvalidPath
  StrCmp $0 "\" ${prefix}InvalidPath
${prefix}CheckWindows:
  StrLen $2 "$WINDIR"
  StrCpy $0 "$INSTDIR" $2
  StrCmp $0 "$WINDIR" 0 ${prefix}AncestryStart
  StrCpy $0 "$INSTDIR" 1 $2
  StrCmp $0 "" ${prefix}InvalidPath
  StrCmp $0 "\" ${prefix}InvalidPath
${prefix}AncestryStart:
  StrCpy $R0 "$INSTDIR"
${prefix}AncestryLoop:
  System::Call 'kernel32::GetFileAttributesW(w "$R0") i .r0'
  StrCmp $0 "-1" ${prefix}Parent
  IntOp $1 $0 & 0x400
  StrCmp $1 "0" 0 ${prefix}InvalidPath
  IntOp $1 $0 & 0x10
  StrCmp $1 "0" ${prefix}InvalidPath
${prefix}Parent:
  ${GetParent} "$R0" $R1
  StrCmp $R1 "" ${prefix}PathDone
  StrCmp $R1 $R0 ${prefix}PathDone
  StrLen $0 $R1
  IntCmp $0 2 ${prefix}PathDone ${prefix}PathDone 0
  StrCpy $R0 $R1
  Goto ${prefix}AncestryLoop
${prefix}InvalidPath:
  MessageBox MB_ICONSTOP "请选择本地磁盘中的独立程序文件夹，不能使用磁盘根目录、系统目录、账号数据目录或链接目录。"
  Abort
${prefix}PathDone:
!macroend

!macro ShutdownInstalled prefix
  IfFileExists "$INSTDIR\WB-PMM.exe" 0 ${prefix}ShutdownDone
  InitPluginsDir
  SetOutPath "$PLUGINSDIR"
  File /oname=WaitForShutdown.ps1 "${__FILEDIR__}\WaitForShutdown.ps1"
  nsExec::ExecToStack /TIMEOUT=75000 '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$PLUGINSDIR\WaitForShutdown.ps1" -Executable "$INSTDIR\WB-PMM.exe"'
  Pop $0
  Pop $1
  StrCmp $0 "0" ${prefix}ShutdownDone
  MessageBox MB_ICONSTOP "WB-PMM 未能正常退出（状态 $0）。操作已中止，请退出应用后重试。"
  Abort
${prefix}ShutdownDone:
!macroend

Function .onInit
  SetShellVarContext current
  SetRegView 64
  ${IfNot} ${AtLeastWin10}
    MessageBox MB_ICONSTOP "WB-PMM 需要 Windows 10 或 Windows 11。"
    Abort
  ${EndIf}
  ${IfNot} ${RunningX64}
    MessageBox MB_ICONSTOP "此安装包仅支持 x64 Windows。"
    Abort
  ${EndIf}
  ReadRegStr $PreviousInstallDir HKCU "${PRODUCT_KEY}" "InstallLocation"
  ReadRegStr $0 HKCU "${UNINSTALL_KEY}" "InstallLocation"
  StrCmp $PreviousInstallDir "" UseUninstallLocation
  StrCmp $0 "" UsePrevious
  StrCmp $PreviousInstallDir $0 UsePrevious
  MessageBox MB_ICONSTOP "旧安装位置记录不一致，请先修复旧安装。"
  Abort
UseUninstallLocation:
  StrCpy $PreviousInstallDir $0
UsePrevious:
  StrCmp $PreviousInstallDir "" InitDone
  !insertmacro NormalizeInstallPath $PreviousInstallDir InvalidPreviousPath
  StrCpy $INSTDIR $PreviousInstallDir
  Goto InitDone
InvalidPreviousPath:
  MessageBox MB_ICONSTOP "无法解析原安装位置，请检查 Windows 中的安装记录。"
  Abort
InitDone:
FunctionEnd

Function ValidateInstallDirectory
  !insertmacro CheckInstallPath Selected
  StrCmp $PreviousInstallDir "" CheckDirectoryContents
  StrCmp $PreviousInstallDir $INSTDIR DirectoryAccepted
  MessageBox MB_ICONINFORMATION "已安装在 $PreviousInstallDir。覆盖升级请使用原位置；如需更换目录，请先卸载旧版再安装，账号和设置会保留。"
  Abort
CheckDirectoryContents:
  ; Allow the complete payload left by an installation that failed before registration.
  IfFileExists "$INSTDIR\WB-PMM.exe" 0 CheckEmptyDirectory
  IfFileExists "$INSTDIR\WB-PMM.runtimeconfig.json" 0 CheckEmptyDirectory
  IfFileExists "$INSTDIR\Helpers\wbgui.exe" 0 CheckEmptyDirectory
  IfFileExists "$INSTDIR\Helpers\wb2api.exe" DirectoryAccepted
CheckEmptyDirectory:
  FindFirst $0 $1 "$INSTDIR\*.*"
DirectoryEntry:
  StrCmp $1 "" CloseDirectory
  StrCmp $1 "." NextDirectoryEntry
  StrCmp $1 ".." NextDirectoryEntry
  FindClose $0
  MessageBox MB_ICONSTOP "所选文件夹已有其他文件。请新建一个空的 WB-PMM 文件夹，避免覆盖其他程序或个人文件。"
  Abort
NextDirectoryEntry:
  FindNext $0 $1
  Goto DirectoryEntry
CloseDirectory:
  FindClose $0
DirectoryAccepted:
FunctionEnd

Function HasWebView2
  Push $0
  ReadRegStr $0 HKCU "${WEBVIEW2_USER}" "pv"
  StrCmp $0 "" CheckMachineRuntime
  StrCmp $0 "0.0.0.0" CheckMachineRuntime
  Pop $0
  Push "1"
  Return
CheckMachineRuntime:
  ReadRegStr $0 HKLM "${WEBVIEW2_MACHINE}" "pv"
  StrCmp $0 "" RuntimeMissing
  StrCmp $0 "0.0.0.0" RuntimeMissing
  Pop $0
  Push "1"
  Return
RuntimeMissing:
  Pop $0
  Push "0"
FunctionEnd

Section "WB-PMM" Install
  Call ValidateInstallDirectory
  ClearErrors
  CreateDirectory "$INSTDIR"
  IfErrors InstallDirectoryFailed
  GetTempFileName $0 "$INSTDIR"
  IfErrors InstallDirectoryFailed
  Delete $0
  Goto InstallDirectoryReady
InstallDirectoryFailed:
  MessageBox MB_ICONSTOP "当前用户无法写入所选目录，请返回选择有写入权限的位置。"
  Abort
InstallDirectoryReady:
  Call HasWebView2
  Pop $0
  StrCmp $0 "1" RuntimeReady
  MessageBox MB_ICONINFORMATION "未检测到 Microsoft Edge WebView2 Runtime，安装程序将通过微软官方引导程序安装。"
  InitPluginsDir
  SetOutPath "$PLUGINSDIR"
  File /oname=MicrosoftEdgeWebview2Setup.exe "${WEBVIEW2_BOOTSTRAPPER}"
  ExecWait '"$PLUGINSDIR\MicrosoftEdgeWebview2Setup.exe" /silent /install' $0
  StrCmp $0 "0" BootstrapSucceeded
  MessageBox MB_ICONSTOP "WebView2 Runtime 安装失败（状态 $0）。请从微软官网下载并安装后重试。"
  Abort
BootstrapSucceeded:
  Call HasWebView2
  Pop $0
  StrCmp $0 "1" RuntimeReady
  MessageBox MB_ICONSTOP "WebView2 Runtime 尚未就绪。请从微软官网下载并安装后重试。"
  Abort
RuntimeReady:
  !insertmacro ShutdownInstalled Install
  !insertmacro GuardPayloadDirectories Install

  SetOutPath "$INSTDIR"
  ClearErrors
  File /r "${APP_DIR}\*"
  IfErrors InstallCopyFailed
  ClearErrors
  WriteUninstaller "$INSTDIR\Uninstall.exe"
  IfErrors InstallCopyFailed
  Goto InstallCopyReady
InstallCopyFailed:
  MessageBox MB_ICONSTOP "安装文件写入失败。请检查安装目录后重试。"
  Abort
InstallCopyReady:

  ClearErrors
  CreateDirectory "$SMPROGRAMS\WB-PMM"
  IfErrors ShortcutCreationFailed
  CreateShortCut "$SMPROGRAMS\WB-PMM\WB-PMM.lnk" "$INSTDIR\WB-PMM.exe" "" "$INSTDIR\WB-PMM.exe" 0
  IfErrors ShortcutCreationFailed
  ClearErrors
  CreateDirectory "$DESKTOP"
  IfErrors DesktopShortcutCreationFailed
  CreateShortCut "$DESKTOP\WB-PMM.lnk" "$INSTDIR\WB-PMM.exe" "" "$INSTDIR\WB-PMM.exe" 0
  IfErrors DesktopShortcutCreationFailed
  nsExec::ExecToStack /TIMEOUT=20000 '"$INSTDIR\WB-PMM.exe" --register-shortcut'
  Pop $0
  Pop $1
  StrCmp $0 "0" ShortcutReady
  DetailPrint "快捷方式附加标识未设置：状态 $0；$1"
  MessageBox MB_ICONINFORMATION "程序、开始菜单和桌面快捷方式已安装，可以正常打开。任务栏附加标识未设置（状态 $0）；若图标显示异常，请反馈安装日志。"
  Goto ShortcutReady
ShortcutCreationFailed:
  MessageBox MB_ICONSTOP "无法创建开始菜单快捷方式，请检查当前用户开始菜单文件夹权限后重试。"
  Abort
DesktopShortcutCreationFailed:
  MessageBox MB_ICONSTOP "无法创建桌面快捷方式，请检查当前用户桌面文件夹权限后重试。"
  Abort
ShortcutReady:
  WriteRegStr HKCU "${PRODUCT_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "WB-PMM"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${APP_VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "WB-PMM"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\WB-PMM.exe"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1
SectionEnd

Function un.onInit
  SetShellVarContext current
  SetRegView 64
  ; NSIS supplies the directory of the actual installed uninstaller.
  !insertmacro NormalizeInstallPath $INSTDIR UninstallPathMismatch
  ReadRegStr $0 HKCU "${PRODUCT_KEY}" "InstallLocation"
  StrCmp $0 "$INSTDIR" UninstallPathMatched
UninstallPathMismatch:
  MessageBox MB_ICONSTOP "安装路径记录不匹配，已停止卸载以保护其他文件。"
  Abort
UninstallPathMatched:
  !insertmacro CheckInstallPath Uninstall
FunctionEnd

Section "Uninstall"
  !insertmacro ShutdownInstalled Uninstall
  !insertmacro GuardPayloadDirectories Uninstall
  SetOutPath "$TEMP"
  !insertmacro RemovePayloadFiles
  Delete "$SMPROGRAMS\WB-PMM\WB-PMM.lnk"
  RMDir "$SMPROGRAMS\WB-PMM"
  Delete "$DESKTOP\WB-PMM.lnk"
  ReadRegStr $0 HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "WB-PMM"
  StrCmpS $0 '"$INSTDIR\WB-PMM.exe" --background' RemoveManagedRun KeepRun
RemoveManagedRun:
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "WB-PMM"
KeepRun:
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
  DeleteRegKey HKCU "${PRODUCT_KEY}"
SectionEnd
