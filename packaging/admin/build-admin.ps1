[CmdletBinding()]
param(
    [string]$Output = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$root = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
if ([string]::IsNullOrWhiteSpace($Output)) {
    $Output = Join-Path $root "dist\EZGameLAN-Admin.exe"
}
$outputDirectory = Split-Path -Parent $Output
New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null

$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
Push-Location $root
try {
    & go build -trimpath -ldflags "-H=windowsgui" `
        -o $Output ./cmd/ezgamelan-admin
    if ($LASTEXITCODE -ne 0) {
        throw "构建 EZGameLAN-Admin.exe 失败。"
    }
}
finally {
    Pop-Location
}

Write-Host "administrator client: $Output"
Get-FileHash -LiteralPath $Output -Algorithm SHA256
