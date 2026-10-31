param([Parameter(Mandatory = $true)][string]$Executable)

$ErrorActionPreference = 'Stop'
try {
    if (-not (Test-Path -LiteralPath $Executable -PathType Leaf)) { exit 0 }
    $process = Start-Process -FilePath $Executable -ArgumentList '--shutdown' -WindowStyle Hidden -PassThru
    if (-not $process.WaitForExit(65000)) { exit 124 }
    exit $process.ExitCode
} catch {
    exit 1
}
