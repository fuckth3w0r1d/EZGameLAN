[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$serviceName = "EZGameLAN"
$installDir = Join-Path $env:ProgramFiles "EZGameLAN"
$dataDir = Join-Path $env:ProgramData "EZGameLAN"
$installerDir = Join-Path $dataDir "installer"
$managedMarker = Join-Path $installerDir "openvpn-installed-by-ezgamelan"
$cachedOpenVPNMSI = Join-Path $installerDir "openvpn.msi"

function Test-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    return $principal.IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator
    )
}

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)]
        [string]$FilePath,

        [Parameter(Mandatory = $true)]
        [string[]]$ArgumentList,

        [int[]]$SuccessExitCode = @(0)
    )

    $process = Start-Process -FilePath $FilePath `
        -ArgumentList $ArgumentList -Wait -PassThru
    if ($SuccessExitCode -notcontains $process.ExitCode) {
        throw "$FilePath 退出码为 $($process.ExitCode)"
    }
}

function Stop-EZGameLANService {
    $service = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
    if ($null -eq $service) {
        return
    }
    if ($service.Status -ne [System.ServiceProcess.ServiceControllerStatus]::Stopped) {
        Stop-Service -Name $serviceName -Force
        $service.WaitForStatus(
            [System.ServiceProcess.ServiceControllerStatus]::Stopped,
            [TimeSpan]::FromSeconds(20)
        )
    }
}

function Stop-EZGameLANClient {
    $processes = @(Get-Process -Name "ezgamelan-client" `
        -ErrorAction SilentlyContinue)
    foreach ($process in $processes) {
        Stop-Process -Id $process.Id -Force
        $process.WaitForExit()
        $process.Dispose()
    }
}

if (-not [Environment]::Is64BitOperatingSystem) {
    throw "EZGameLAN 当前分发包仅支持 Windows x64。"
}

if (-not (Test-Administrator)) {
    $arguments = @(
        "-NoLogo",
        "-NoProfile",
        "-ExecutionPolicy", "Bypass",
        "-File", "`"$PSCommandPath`""
    )
    $process = Start-Process -FilePath "powershell.exe" `
        -ArgumentList $arguments -Verb RunAs -Wait -PassThru
    exit $process.ExitCode
}

$payloadDir = Join-Path $PSScriptRoot "payload"
$clientSource = Join-Path $payloadDir "ezgamelan-client.exe"
$serviceSource = Join-Path $payloadDir "ezgamelan-service.exe"
$uninstallSource = Join-Path $PSScriptRoot "uninstall.ps1"
$thirdPartyDir = Join-Path $PSScriptRoot "third_party"
$hashFile = Join-Path $thirdPartyDir "openvpn.sha256"

foreach ($path in @($clientSource, $serviceSource, $uninstallSource, $hashFile)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "安装包缺少文件：$path"
    }
}

$msiFiles = @(Get-ChildItem -LiteralPath $thirdPartyDir `
    -Filter "OpenVPN-*-amd64.msi" -File)
if ($msiFiles.Count -ne 1) {
    throw "安装包中应包含一份 OpenVPN Windows x64 MSI。"
}
$openVPNMSI = $msiFiles[0].FullName

$expectedHashLine = (Get-Content -LiteralPath $hashFile -Raw).Trim()
if ($expectedHashLine -notmatch "^(?<hash>[0-9A-Fa-f]{64})\s+\*?.+$") {
    throw "OpenVPN SHA-256 清单格式错误。"
}
$expectedHash = $Matches["hash"].ToUpperInvariant()
$actualHash = (Get-FileHash -LiteralPath $openVPNMSI -Algorithm SHA256).Hash
if ($actualHash -ne $expectedHash) {
    throw "OpenVPN 安装包 SHA-256 校验失败。"
}

$signature = Get-AuthenticodeSignature -LiteralPath $openVPNMSI
if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid `
    -or $null -eq $signature.SignerCertificate `
    -or $signature.SignerCertificate.Subject -notlike "*OpenVPN*") {
    throw "OpenVPN 安装包签名校验失败。"
}

New-Item -ItemType Directory -Path $installDir -Force | Out-Null
New-Item -ItemType Directory -Path $installerDir -Force | Out-Null

$openVPNExecutable = Join-Path $env:ProgramFiles "OpenVPN\bin\openvpn.exe"
$managedOpenVPN = (Test-Path -LiteralPath $managedMarker -PathType Leaf) `
    -or -not (Test-Path -LiteralPath $openVPNExecutable -PathType Leaf)

Stop-EZGameLANService
Stop-EZGameLANClient

Invoke-Checked -FilePath "msiexec.exe" -ArgumentList @(
    "/i",
    "`"$openVPNMSI`"",
    "/qn",
    "/norestart",
    "ADDLOCAL=OpenVPN,Drivers,Drivers.TAPWindows6"
) -SuccessExitCode @(0, 1641, 3010)

if (-not (Test-Path -LiteralPath $openVPNExecutable -PathType Leaf)) {
    throw "OpenVPN 核心安装后未找到。"
}
if ($managedOpenVPN) {
    Copy-Item -LiteralPath $openVPNMSI -Destination $cachedOpenVPNMSI -Force
    Set-Content -LiteralPath $managedMarker -Value $actualHash `
        -Encoding ASCII -NoNewline
}

$clientPath = Join-Path $installDir "ezgamelan-client.exe"
$servicePath = Join-Path $installDir "ezgamelan-service.exe"
$uninstallPath = Join-Path $installDir "uninstall.ps1"
Copy-Item -LiteralPath $clientSource -Destination $clientPath -Force
Copy-Item -LiteralPath $serviceSource -Destination $servicePath -Force
Copy-Item -LiteralPath $uninstallSource -Destination $uninstallPath -Force

$existingService = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
if ($null -eq $existingService) {
    Invoke-Checked -FilePath "sc.exe" -ArgumentList @(
        "create",
        $serviceName,
        "binPath= `"$servicePath`"",
        "start= auto",
        "DisplayName= EZGameLAN"
    )
}
else {
    Invoke-Checked -FilePath "sc.exe" -ArgumentList @(
        "config",
        $serviceName,
        "binPath= `"$servicePath`"",
        "start= auto",
        "DisplayName= EZGameLAN"
    )
}
Invoke-Checked -FilePath "sc.exe" -ArgumentList @(
    "description",
    $serviceName,
    "EZGameLAN virtual LAN connection service"
)
Invoke-Checked -FilePath "sc.exe" -ArgumentList @(
    "failure",
    $serviceName,
    "reset= 86400",
    "actions= restart/5000/restart/15000"
)

$protocolKey = "HKLM:\Software\Classes\ezgamelan"
New-Item -Path $protocolKey -Force | Out-Null
Set-Item -Path $protocolKey -Value "URL:EZGameLAN Join"
New-ItemProperty -Path $protocolKey -Name "URL Protocol" -Value "" `
    -PropertyType String -Force | Out-Null
$commandKey = Join-Path $protocolKey "shell\open\command"
New-Item -Path $commandKey -Force | Out-Null
Set-Item -Path $commandKey -Value "`"$clientPath`" `"%1`""

$startMenuDir = Join-Path $env:ProgramData `
    "Microsoft\Windows\Start Menu\Programs\EZGameLAN"
New-Item -ItemType Directory -Path $startMenuDir -Force | Out-Null
$shell = New-Object -ComObject WScript.Shell
$shortcut = $shell.CreateShortcut((Join-Path $startMenuDir "EZGameLAN.lnk"))
$shortcut.TargetPath = $clientPath
$shortcut.WorkingDirectory = $installDir
$shortcut.Save()

$uninstallKey = "HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\EZGameLAN"
New-Item -Path $uninstallKey -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name "DisplayName" `
    -Value "EZGameLAN" -PropertyType String -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name "DisplayVersion" `
    -Value "0.1.0" -PropertyType String -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name "Publisher" `
    -Value "EZGameLAN" -PropertyType String -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name "UninstallString" `
    -Value "powershell.exe -NoProfile -ExecutionPolicy Bypass -File `"$uninstallPath`"" `
    -PropertyType String -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name "NoModify" `
    -Value 1 -PropertyType DWord -Force | Out-Null
New-ItemProperty -Path $uninstallKey -Name "NoRepair" `
    -Value 1 -PropertyType DWord -Force | Out-Null

Start-Service -Name $serviceName
Start-Process -FilePath $clientPath
