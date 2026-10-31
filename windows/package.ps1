param(
    [string]$AppDir = (Join-Path $PSScriptRoot 'dist/app'),
    [string]$OutputDir = (Join-Path $PSScriptRoot 'dist'),
    [string]$BootstrapperPath,
    [switch]$CompileCheck
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$app = (Resolve-Path -LiteralPath $AppDir).Path
foreach ($relative in @('WB-PMM.exe', 'WB-PMM.runtimeconfig.json', 'hostfxr.dll', 'Helpers/wbgui.exe', 'Helpers/wb2api.exe')) {
    if (-not (Test-Path -LiteralPath (Join-Path $app $relative) -PathType Leaf)) {
        throw "发布目录缺少 $relative"
    }
}
$project = [xml](Get-Content -LiteralPath (Join-Path $PSScriptRoot 'WBPMM/WBPMM.csproj') -Raw)
$version = [string]$project.Project.PropertyGroup.Version
if ($version -notmatch '^\d+\.\d+\.\d+$') { throw '项目版本号无效' }

$onWindows = [Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT
if (-not $onWindows -and -not $CompileCheck) {
    throw '正式安装包必须在 Windows 上验证微软引导程序签名；本机只能使用 -CompileCheck。'
}

$downloaded = $null
$manifest = $null
try {
    if ($BootstrapperPath) {
        $bootstrapper = (Resolve-Path -LiteralPath $BootstrapperPath).Path
    } else {
        $downloaded = Join-Path ([IO.Path]::GetTempPath()) ("WBPMM-WebView2-" + [Guid]::NewGuid().ToString('N') + '.exe')
        Invoke-WebRequest -Uri 'https://go.microsoft.com/fwlink/p/?LinkId=2124703' -OutFile $downloaded -UseBasicParsing
        $bootstrapper = $downloaded
    }
    if ((Get-Item -LiteralPath $bootstrapper).Length -lt 100000) { throw 'WebView2 引导程序文件无效' }
    if ($onWindows) {
        $signature = Get-AuthenticodeSignature -LiteralPath $bootstrapper
        if ($signature.Status -ne 'Valid' -or
            $signature.SignerCertificate.Subject -notmatch 'O=Microsoft Corporation') {
            throw 'WebView2 引导程序未通过微软签名验证'
        }
    }

    $prefix = $app.TrimEnd([char[]]@('\', '/')) + [IO.Path]::DirectorySeparatorChar
    $files = New-Object 'System.Collections.Generic.List[string]'
    $directories = New-Object 'System.Collections.Generic.List[string]'
    foreach ($item in (Get-ChildItem -LiteralPath $app -Recurse -Force)) {
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw '发布目录包含重解析点'
        }
        $relative = $item.FullName.Substring($prefix.Length).Replace('/', '\')
        if ($relative -match '[\$\x22\r\n]') { throw '发布文件名无法安全写入 NSIS 清单' }
        if ($item.PSIsContainer) { $directories.Add($relative) }
        else { $files.Add($relative) }
    }
    if ($files.Contains('Uninstall.exe')) { throw '发布目录不得包含卸载程序' }

    $lines = New-Object 'System.Collections.Generic.List[string]'
    $lines.Add('!macro GuardPayloadDirectories prefix')
    $index = 0
    foreach ($directory in ($directories | Sort-Object)) {
        $done = '${prefix}Guard' + $index + 'Done'
        $lines.Add('  System::Call ''kernel32::GetFileAttributesW(w "$INSTDIR\' + $directory + '") i .r0''')
        $lines.Add('  StrCmp $0 "-1" ' + $done)
        $lines.Add('  IntOp $1 $0 & 0x400')
        $lines.Add('  StrCmp $1 "0" ' + $done)
        $lines.Add('  MessageBox MB_ICONSTOP "安装目录包含重解析点，已停止以保护其他文件。"')
        $lines.Add('  Abort')
        $lines.Add($done + ':')
        $index++
    }
    $lines.Add('!macroend')
    $lines.Add('!macro RemovePayloadFiles')
    $index = 0
    foreach ($relative in ($files | Sort-Object)) {
        $done = 'PayloadFile' + $index + 'Done'
        $lines.Add('  Delete "$INSTDIR\' + $relative + '"')
        $lines.Add('  IfFileExists "$INSTDIR\' + $relative + '" 0 ' + $done)
        $lines.Add('  MessageBox MB_ICONSTOP "安装文件被占用，无法安全卸载。"')
        $lines.Add('  Abort')
        $lines.Add($done + ':')
        $index++
    }
    $lines.Add('  Delete "$INSTDIR\Uninstall.exe"')
    $lines.Add('  IfFileExists "$INSTDIR\Uninstall.exe" 0 PayloadUninstallerDone')
    $lines.Add('  MessageBox MB_ICONSTOP "卸载程序被占用，无法安全卸载。"')
    $lines.Add('  Abort')
    $lines.Add('PayloadUninstallerDone:')
    foreach ($directory in ($directories | Sort-Object { $_.Length } -Descending)) {
        $lines.Add('  RMDir "$INSTDIR\' + $directory + '"')
    }
    $lines.Add('  RMDir "$INSTDIR"')
    $lines.Add('!macroend')
    $manifest = Join-Path ([IO.Path]::GetTempPath()) ("WBPMM-manifest-" + [Guid]::NewGuid().ToString('N') + '.nsh')
    [IO.File]::WriteAllLines($manifest, $lines, [Text.UTF8Encoding]::new($false))

    New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
    $output = (Resolve-Path -LiteralPath $OutputDir).Path
    $suffix = if ($CompileCheck) { '-compile-check' } else { '' }
    $setup = Join-Path $output ("WB-PMM-Setup-$version-x64$suffix.exe")
    $compiler = (Get-Command makensis -ErrorAction Stop).Source
    & $compiler '-WX' '-V2' "-DAPP_DIR=$app" "-DWEBVIEW2_BOOTSTRAPPER=$bootstrapper" "-DPAYLOAD_MANIFEST=$manifest" "-DSETUP_OUT=$setup" "-DAPP_VERSION=$version" (Join-Path $PSScriptRoot 'installer/WB-PMM.nsi')
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $setup -PathType Leaf)) {
        throw 'NSIS 安装包编译失败'
    }
    Write-Output $setup
} finally {
    if ($manifest -and (Test-Path -LiteralPath $manifest)) {
        Remove-Item -LiteralPath $manifest -Force
    }
    if ($downloaded -and (Test-Path -LiteralPath $downloaded)) {
        Remove-Item -LiteralPath $downloaded -Force
    }
}
