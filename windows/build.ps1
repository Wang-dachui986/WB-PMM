param([string]$OutputDir = "$PSScriptRoot\dist\app", [string]$Version = "1.0.10")
$ErrorActionPreference = "Stop"
$Root = Split-Path $PSScriptRoot -Parent
$OutputDir = [IO.Path]::GetFullPath($OutputDir)
function Invoke-Checked([string]$Command, [string[]]$Arguments) {
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Command failed ($LASTEXITCODE)" }
}
foreach ($tool in @("node", "npm", "go", "dotnet")) { Get-Command $tool -ErrorAction Stop | Out-Null }
$env:DOTNET_CLI_TELEMETRY_OPTOUT = "1"
$env:CGO_ENABLED = "0"; $env:GOOS = "windows"; $env:GOARCH = "amd64"
Push-Location "$Root\panel\web"
try {
    if (!(Test-Path node_modules)) { Invoke-Checked "npm" @("ci", "--no-audit", "--no-fund") }
    Invoke-Checked "npm" @("run", "build")
} finally { Pop-Location }
Invoke-Checked "dotnet" @("publish", "$PSScriptRoot\WBPMM\WBPMM.csproj", "-c", "Release", "-r", "win-x64", "--self-contained", "true", "-p:Version=$Version", "-o", $OutputDir)
New-Item -ItemType Directory -Force "$OutputDir\Helpers" | Out-Null
foreach ($component in @(@("panel", "wbgui"), @("gateway", "wb2api"))) {
    Push-Location "$Root\$($component[0])"
    try { Invoke-Checked "go" @("build", "-buildvcs=false", "-trimpath", "-ldflags", "-s -w -X main.version=$Version", "-o", "$OutputDir\Helpers\$($component[1]).exe", "./cmd/server") }
    finally { Pop-Location }
}
Copy-Item "$Root\LICENSE" "$OutputDir\LICENSE.txt"
Copy-Item "$PSScriptRoot\README.md" "$OutputDir\README-Windows.md"
Write-Output "Windows x64 application: $OutputDir"
