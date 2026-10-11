[CmdletBinding()]
param(
    [string]$OpenVPNMSI = "",
    [string]$OutputFile = "dist\EZGameLAN-Portable.zip"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$packageName = "OpenVPN-2.6.21-I001-amd64.msi"
$packageHash = "845B654EDB92CC02A05DC335693980C4F2D4A4F09689D0844309753DE2CB5471"
if ([string]::IsNullOrWhiteSpace($OpenVPNMSI)) {
    $OpenVPNMSI = Join-Path $repositoryRoot "third_party\openvpn\$packageName"
}
$msi = Get-Item -LiteralPath $OpenVPNMSI
if ($msi.Name -ne $packageName -or (Get-FileHash -LiteralPath $msi.FullName -Algorithm SHA256).Hash -ne $packageHash) {
    throw "OpenVPN MSI 与固定版本校验信息不符。"
}
$signature = Get-AuthenticodeSignature -LiteralPath $msi.FullName
if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid -or
    $null -eq $signature.SignerCertificate -or $signature.SignerCertificate.Subject -notlike "*OpenVPN*") {
    throw "OpenVPN MSI 签名校验失败。"
}
$outputPath = if ([IO.Path]::IsPathRooted($OutputFile)) {
    [IO.Path]::GetFullPath($OutputFile)
} else {
    [IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputFile))
}
$temporary = Join-Path $repositoryRoot (".build-release-" + [Guid]::NewGuid().ToString("N"))
$stage = Join-Path $temporary "EZGameLAN"
$extracted = Join-Path $temporary "extracted"
$previousEnv = @{}
foreach ($name in @("TEMP", "TMP", "GOOS", "GOARCH", "CGO_ENABLED", "GOCACHE", "GOMODCACHE")) {
    $previousEnv[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
}

function Export-MSIStream {
    param($Database, [string]$Query, [string]$Destination)
    $view = $Database.OpenView($Query)
    try {
        $view.Execute()
        $record = $view.Fetch()
        if ($null -eq $record) { throw "MSI 中缺少指定载荷。" }
        $stream = [IO.File]::Create($Destination)
        try {
            while ($chunk = $record.ReadStream(1, 65536, 1)) {
                $bytes = [Text.Encoding]::GetEncoding(28591).GetBytes($chunk)
                $stream.Write($bytes, 0, $bytes.Length)
            }
        } finally { $stream.Dispose() }
    } finally { $view.Close() }
}

try {
    New-Item -ItemType Directory -Path $extracted, (Join-Path $stage "openvpn\bin"),
        (Join-Path $stage "openvpn\driver"), (Join-Path $temporary "tmp") -Force | Out-Null
    $env:TEMP = Join-Path $temporary "tmp"
    $env:TMP = $env:TEMP
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "0"
    if (-not $env:GOCACHE) { $env:GOCACHE = Join-Path $temporary "cache" }
    if (-not $env:GOMODCACHE) { $env:GOMODCACHE = Join-Path $temporary "mod" }
    $go = Get-Command go -ErrorAction SilentlyContinue
    $goPath = if ($go) { $go.Source } else { Join-Path $env:ProgramFiles "Go\bin\go.exe" }
    if (-not (Test-Path -LiteralPath $goPath)) { throw "请先安装 Go。" }

    $installer = New-Object -ComObject WindowsInstaller.Installer
    $database = $installer.OpenDatabase($msi.FullName, 0)
    $cabinet = Join-Path $temporary "openvpn.cab"
    Export-MSIStream $database 'SELECT `Data` FROM `_Streams` WHERE `Name`=''openvpn.cab''' $cabinet
    & "$env:SystemRoot\System32\expand.exe" '-F:*' $cabinet $extracted | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "解压 OpenVPN 载荷失败。" }
    $wanted = @("openvpn.exe", "tapctl.exe", "libcrypto-3-x64.dll", "libssl-3-x64.dll",
        "libpkcs11-helper-1.dll", "vcruntime140.dll", "openvpnservmsg.dll", "license.txt")
    $copied = @{}
    $view = $database.OpenView('SELECT `File`, `FileName` FROM `File`')
    try {
        $view.Execute()
        while ($record = $view.Fetch()) {
            $name = ($record.StringData(2) -split '\|')[-1]
            if ($wanted -contains $name) {
                $target = if ($name -eq "license.txt") { Join-Path $stage "openvpn\license.txt" }
                    else { Join-Path $stage "openvpn\bin\$name" }
                Copy-Item -LiteralPath (Join-Path $extracted $record.StringData(1)) -Destination $target
                $copied[$name] = $true
            }
        }
    } finally { $view.Close() }
    foreach ($name in $wanted) {
        if (-not $copied.ContainsKey($name)) { throw "MSI 中缺少 $name。" }
    }
    $tapDLL = Join-Path $temporary "tap.dll"
    Export-MSIStream $database 'SELECT `Data` FROM `Binary` WHERE `Name`=''installer.dll.8D9A59BA_33C1_4CA0_96AF_E7F728DEC8D7''' $tapDLL
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class EZGameLANDriverResources {
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
    static extern IntPtr LoadLibraryEx(string path, IntPtr file, uint flags);
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
    static extern IntPtr FindResource(IntPtr module, string name, IntPtr type);
    [DllImport("kernel32.dll")] static extern uint SizeofResource(IntPtr module, IntPtr resource);
    [DllImport("kernel32.dll")] static extern IntPtr LoadResource(IntPtr module, IntPtr resource);
    [DllImport("kernel32.dll")] static extern IntPtr LockResource(IntPtr resource);
    [DllImport("kernel32.dll")] static extern bool FreeLibrary(IntPtr module);
    public static byte[] Read(string path, string name) {
        IntPtr module = LoadLibraryEx(path, IntPtr.Zero, 2);
        if (module == IntPtr.Zero) throw new System.ComponentModel.Win32Exception();
        try {
            IntPtr resource = FindResource(module, name, new IntPtr(10));
            if (resource == IntPtr.Zero) throw new System.ComponentModel.Win32Exception();
            uint size = SizeofResource(module, resource);
            IntPtr data = LockResource(LoadResource(module, resource));
            if (data == IntPtr.Zero || size == 0) throw new InvalidOperationException("Empty driver resource");
            byte[] bytes = new byte[size];
            Marshal.Copy(data, bytes, 0, bytes.Length);
            return bytes;
        } finally { FreeLibrary(module); }
    }
}
'@
    foreach ($pair in @(@("INF", "OemVista.inf"), @("SYS", "tap0901.sys"), @("CAT", "tap0901.cat"))) {
        [IO.File]::WriteAllBytes((Join-Path $stage "openvpn\driver\$($pair[1])"),
            [EZGameLANDriverResources]::Read($tapDLL, "DRIVER-WHQL.$($pair[0])"))
    }
    $manifest = [ordered]@{}
    $payloadRoot = Join-Path $stage "openvpn"
    Get-ChildItem -LiteralPath (Join-Path $payloadRoot "bin"), (Join-Path $payloadRoot "driver") -File |
        ForEach-Object {
            $relative = $_.FullName.Substring($payloadRoot.Length + 1).Replace('\', '/')
            $manifest[$relative] = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        }
    $encodedManifest = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes(($manifest | ConvertTo-Json -Compress)))
    Push-Location $repositoryRoot
    try {
        & $goPath build -trimpath -ldflags "-H=windowsgui" -o (Join-Path $stage "EZGameLAN.exe") ./cmd/ezgamelan-client
        if ($LASTEXITCODE -ne 0) { throw "构建客户端失败。" }
        & $goPath build -trimpath -ldflags "-H=windowsgui -X main.payloadManifest=$encodedManifest" `
            -o (Join-Path $stage "EZGameLAN-Backend.exe") ./cmd/ezgamelan-backend
        if ($LASTEXITCODE -ne 0) { throw "构建后台失败。" }
    } finally { Pop-Location }
    [IO.File]::WriteAllText((Join-Path $stage "openvpn\SOURCE.txt"),
        "OpenVPN Community 2.6.21 Windows x64`r`nMSI: https://build.openvpn.net/downloads/releases/$packageName`r`nSource: https://github.com/OpenVPN/openvpn/tree/v2.6.21`r`nTAP source: https://github.com/OpenVPN/tap-windows6`r`nSee license.txt for license terms.`r`n")
    New-Item -ItemType Directory -Path (Split-Path $outputPath -Parent) -Force | Out-Null
    Compress-Archive -LiteralPath $stage -DestinationPath $outputPath -CompressionLevel Optimal -Force
    Write-Host "便携包已生成：$outputPath"
} finally {
    foreach ($name in $previousEnv.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previousEnv[$name], "Process")
    }
    $resolvedTemporary = [IO.Path]::GetFullPath($temporary)
    if (-not $resolvedTemporary.StartsWith($repositoryRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw "构建临时目录不在仓库内。"
    }
    if (Test-Path -LiteralPath $resolvedTemporary) { Remove-Item -LiteralPath $resolvedTemporary -Recurse -Force }
}
