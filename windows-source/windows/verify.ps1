$ErrorActionPreference = "Stop"
if ($env:OS -ne "Windows_NT") { throw "Run this script on Windows x64." }
$Root = Split-Path $PSScriptRoot -Parent
function Invoke-Checked([string]$Command, [string[]]$Arguments) {
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Command failed ($LASTEXITCODE)" }
}
$env:DOTNET_CLI_TELEMETRY_OPTOUT = "1"
$PathCheck = Join-Path ([IO.Path]::GetTempPath()) ("WBPMM-path-check-" + [Guid]::NewGuid().ToString('N') + '.exe')
try {
    Invoke-Checked "makensis" @("-WX", "-V2", "-DTEST_OUT=$PathCheck", "$PSScriptRoot\tests\InstallerPath\Check.nsi")
    $check = Start-Process -FilePath $PathCheck -Wait -PassThru
    if ($check.ExitCode -ne 0) { throw "Installer path regression failed ($($check.ExitCode))" }
} finally {
    if (Test-Path -LiteralPath $PathCheck) { Remove-Item -LiteralPath $PathCheck }
}
Invoke-Checked "dotnet" @("run", "--project", "$PSScriptRoot\tests\Foundation\Foundation.csproj")
Push-Location "$Root\gateway"
try { Invoke-Checked "go" @("test", "./internal/privatefs", "./internal/auth", "./internal/pool", "./cmd/server") }
finally { Pop-Location }
Push-Location "$Root\panel"
$PanelCheck = Join-Path ([IO.Path]::GetTempPath()) ("WBPMM-panel-check-" + [Guid]::NewGuid().ToString('N') + '.exe')
try {
    Invoke-Checked "go" @("test", "./internal/fsutil", "./internal/authstore", "./internal/ops", "./cmd/server")
    Invoke-Checked "go" @("build", "-buildvcs=false", "-o", $PanelCheck, "./cmd/server")
    Invoke-Checked "dotnet" @("run", "--project", "$PSScriptRoot\tests\CoreContracts\CoreContracts.csproj", "--", "--panel", $PanelCheck)
}
finally {
    Pop-Location
    if (Test-Path -LiteralPath $PanelCheck) { Remove-Item -LiteralPath $PanelCheck }
}
Write-Output "Synthetic Windows checks passed. Continue the manual acceptance matrix; no real model requests were sent."
