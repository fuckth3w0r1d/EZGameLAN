[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateScript({ Test-Path -LiteralPath $_ -PathType Leaf })]
    [string]$OpenVPNMSI,

    [string]$OutputFile = "dist\EZGameLAN-Setup.exe"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Assert-PowerShellSyntax {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    $tokens = $null
    $parseErrors = $null
    [System.Management.Automation.Language.Parser]::ParseFile(
        $Path,
        [ref]$tokens,
        [ref]$parseErrors
    ) | Out-Null
    if ($parseErrors.Count -gt 0) {
        throw "PowerShell 语法检查失败：$Path：$($parseErrors[0].Message)"
    }
}

$openVPNPackageName = "OpenVPN-2.6.21-I001-amd64.msi"
$openVPNHash = "845B654EDB92CC02A05DC335693980C4F2D4A4F09689D0844309753DE2CB5471"
$openVPNSourceURL = "https://build.openvpn.net/downloads/releases/$openVPNPackageName"
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$outputPath = if ([IO.Path]::IsPathRooted($OutputFile)) {
    [IO.Path]::GetFullPath($OutputFile)
}
else {
    [IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputFile))
}
$temporary = Join-Path ([IO.Path]::GetTempPath()) (
    "EZGameLAN-Release-" + [Guid]::NewGuid().ToString("N")
)
$stage = Join-Path $temporary "stage"
$payloadDirectory = Join-Path $stage "payload"
$thirdPartyDirectory = Join-Path $stage "third_party"
$archivePath = Join-Path $temporary "payload.zip"
$setupBase = Join-Path $temporary "ezgamelan-setup-base.exe"

Assert-PowerShellSyntax -Path (Join-Path $PSScriptRoot "install.ps1")
Assert-PowerShellSyntax -Path (Join-Path $PSScriptRoot "uninstall.ps1")

$msi = Get-Item -LiteralPath $OpenVPNMSI
if ($msi.Name -ne $openVPNPackageName) {
    throw "当前分发版本只接受 $openVPNPackageName。"
}
$actualHash = (Get-FileHash -LiteralPath $msi.FullName -Algorithm SHA256).Hash
if ($actualHash -ne $openVPNHash) {
    throw "OpenVPN MSI SHA-256 校验失败。"
}
$signature = Get-AuthenticodeSignature -LiteralPath $msi.FullName
if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid `
    -or $null -eq $signature.SignerCertificate `
    -or $signature.SignerCertificate.Subject -notlike "*OpenVPN*") {
    throw "OpenVPN MSI 签名校验失败。"
}

try {
    New-Item -ItemType Directory -Path $payloadDirectory -Force | Out-Null
    New-Item -ItemType Directory -Path $thirdPartyDirectory -Force | Out-Null
    New-Item -ItemType Directory -Path (Split-Path $outputPath -Parent) `
        -Force | Out-Null

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
                -o (Join-Path $payloadDirectory "ezgamelan-service.exe") `
                ./cmd/ezgamelan-service
            if ($LASTEXITCODE -ne 0) {
                throw "构建 ezgamelan-service.exe 失败。"
            }
            & go build -trimpath -ldflags "-H=windowsgui" `
                -o (Join-Path $payloadDirectory "ezgamelan-client.exe") `
                ./cmd/ezgamelan-client
            if ($LASTEXITCODE -ne 0) {
                throw "构建 ezgamelan-client.exe 失败。"
            }
            & go build -trimpath -ldflags "-H=windowsgui" `
                -o $setupBase ./cmd/ezgamelan-setup
            if ($LASTEXITCODE -ne 0) {
                throw "构建 ezgamelan-setup.exe 失败。"
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

    Copy-Item -LiteralPath $msi.FullName `
        -Destination (Join-Path $thirdPartyDirectory $msi.Name)
    Set-Content -LiteralPath (Join-Path $thirdPartyDirectory "openvpn.sha256") `
        -Value "$openVPNHash *$openVPNPackageName" -Encoding ASCII
    # Windows PowerShell 5.1 needs the BOM to decode non-ASCII script text as UTF-8.
    $utf8WithBOM = [System.Text.UTF8Encoding]::new($true)
    foreach ($scriptName in @("install.ps1", "uninstall.ps1")) {
        $scriptSource = Join-Path $PSScriptRoot $scriptName
        $scriptDestination = Join-Path $stage $scriptName
        $scriptText = [IO.File]::ReadAllText(
            $scriptSource,
            [System.Text.UTF8Encoding]::new($false)
        )
        [IO.File]::WriteAllText($scriptDestination, $scriptText, $utf8WithBOM)
    }

    $thirdPartyNotice = @"
OpenVPN Community Windows x64
Package: $openVPNPackageName
Source: $openVPNSourceURL
SHA-256: $openVPNHash
License: GPL-2.0
License source: https://github.com/OpenVPN/openvpn/blob/master/COPYING

Only the OpenVPN core and TAP-Windows6 MSI features are selected. EZGameLAN
does not launch or depend on OpenVPN GUI.
"@
    Set-Content -LiteralPath (Join-Path $stage "THIRD_PARTY.txt") `
        -Value $thirdPartyNotice -Encoding UTF8

    Compress-Archive -LiteralPath @(
        (Join-Path $stage "install.ps1")
        (Join-Path $stage "uninstall.ps1")
        (Join-Path $stage "THIRD_PARTY.txt")
        $payloadDirectory
        $thirdPartyDirectory
    ) -DestinationPath $archivePath -CompressionLevel Optimal

    Push-Location $repositoryRoot
    try {
        & go run ./cmd/ezgamelan-pack `
            -exe $setupBase -payload $archivePath -output $outputPath
        if ($LASTEXITCODE -ne 0) {
            throw "封装 EZGameLAN-Setup.exe 失败。"
        }
    }
    finally {
        Pop-Location
    }
    $setupHash = (Get-FileHash -LiteralPath $outputPath -Algorithm SHA256).Hash
    Write-Host "单文件安装包已生成：$outputPath"
    Write-Host "SHA-256：$setupHash"
}
finally {
    Remove-Item -LiteralPath $temporary -Recurse -Force `
        -ErrorAction SilentlyContinue
}
