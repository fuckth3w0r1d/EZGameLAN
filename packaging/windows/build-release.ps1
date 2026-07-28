[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateScript({ Test-Path -LiteralPath $_ -PathType Leaf })]
    [string]$OpenVPNMSI,

    [string]$OutputDirectory = "dist\EZGameLAN-windows-amd64"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$openVPNPackageName = "OpenVPN-2.6.21-I001-amd64.msi"
$openVPNSourceURL = "https://build.openvpn.net/downloads/releases/$openVPNPackageName"
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$outputPath = [IO.Path]::GetFullPath(
    (Join-Path $repositoryRoot $OutputDirectory)
)
$payloadPath = Join-Path $outputPath "payload"
$thirdPartyPath = Join-Path $outputPath "third_party"

if (Test-Path -LiteralPath $outputPath) {
    Remove-Item -LiteralPath $outputPath -Recurse -Force
}
New-Item -ItemType Directory -Path $payloadPath -Force | Out-Null
New-Item -ItemType Directory -Path $thirdPartyPath -Force | Out-Null

$oldGOOS = $env:GOOS
$oldGOARCH = $env:GOARCH
$oldCGOEnabled = $env:CGO_ENABLED
try {
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "0"

    Push-Location $repositoryRoot
    try {
        & go build -trimpath `
            -o (Join-Path $payloadPath "ezgamelan-service.exe") `
            ./cmd/ezgamelan-service
        if ($LASTEXITCODE -ne 0) {
            throw "构建 ezgamelan-service.exe 失败。"
        }
        & go build -trimpath -ldflags "-H=windowsgui" `
            -o (Join-Path $payloadPath "ezgamelan-client.exe") `
            ./cmd/ezgamelan-client
        if ($LASTEXITCODE -ne 0) {
            throw "构建 ezgamelan-client.exe 失败。"
        }
    }
    finally {
        Pop-Location
    }
}
finally {
    $env:GOOS = $oldGOOS
    $env:GOARCH = $oldGOARCH
    $env:CGO_ENABLED = $oldCGOEnabled
}

$msi = Get-Item -LiteralPath $OpenVPNMSI
if ($msi.Name -ne $openVPNPackageName) {
    throw "当前分发版本只接受 $openVPNPackageName。"
}
$signature = Get-AuthenticodeSignature -LiteralPath $msi.FullName
if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid `
    -or $null -eq $signature.SignerCertificate `
    -or $signature.SignerCertificate.Subject -notlike "*OpenVPN*") {
    throw "OpenVPN MSI 签名校验失败。"
}

$msiDestination = Join-Path $thirdPartyPath $msi.Name
Copy-Item -LiteralPath $msi.FullName -Destination $msiDestination
$hash = (Get-FileHash -LiteralPath $msiDestination -Algorithm SHA256).Hash
Set-Content -LiteralPath (Join-Path $thirdPartyPath "openvpn.sha256") `
    -Value "$hash *$($msi.Name)" -Encoding ASCII

Copy-Item -LiteralPath (Join-Path $PSScriptRoot "install.cmd") `
    -Destination $outputPath
Copy-Item -LiteralPath (Join-Path $PSScriptRoot "install.ps1") `
    -Destination $outputPath
Copy-Item -LiteralPath (Join-Path $PSScriptRoot "uninstall.ps1") `
    -Destination $outputPath

$thirdPartyNotice = @"
OpenVPN Community Windows x64
Package: $($msi.Name)
Source: $OpenVPNSourceURL
SHA-256: $hash
License: GPL-2.0
License source: https://github.com/OpenVPN/openvpn/blob/master/COPYING

Only the OpenVPN core and TAP-Windows6 MSI features are selected. EZGameLAN
does not launch or depend on OpenVPN GUI.
"@
Set-Content -LiteralPath (Join-Path $outputPath "THIRD_PARTY.txt") `
    -Value $thirdPartyNotice -Encoding UTF8

Write-Host "Windows 分发目录已生成：$outputPath"
