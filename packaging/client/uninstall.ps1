[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$OutputEncoding = [Console]::OutputEncoding

$serviceName = "EZGameLAN"
$installDir = Join-Path $env:ProgramFiles "EZGameLAN"
$dataDir = Join-Path $env:ProgramData "EZGameLAN"
$localDataDir = Join-Path $env:LOCALAPPDATA "EZGameLAN"
$installerDir = Join-Path $dataDir "installer"
$managedMarker = Join-Path $installerDir "openvpn-installed-by-ezgamelan"
$cachedOpenVPNMSI = Join-Path $installerDir "openvpn.msi"
$adapterMarker = Join-Path $dataDir "config\adapter-original-name.txt"

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

function Stop-EZGameLANClient {
    $processes = @(Get-Process -Name "ezgamelan-client" `
        -ErrorAction SilentlyContinue)
    foreach ($process in $processes) {
        Stop-Process -Id $process.Id -Force
        $process.WaitForExit()
        $process.Dispose()
    }
}

if (-not (Test-Administrator)) {
    $arguments = @(
        "-NoLogo"
        "-NoProfile"
        "-ExecutionPolicy"
        "Bypass"
        "-File"
        "`"$PSCommandPath`""
    )
    $process = Start-Process -FilePath "powershell.exe" `
        -ArgumentList $arguments -Verb RunAs -Wait -PassThru
    exit $process.ExitCode
}

$service = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
if ($null -ne $service) {
    if ($service.Status -ne [System.ServiceProcess.ServiceControllerStatus]::Stopped) {
        Stop-Service -Name $serviceName -Force
        $service.WaitForStatus(
            [System.ServiceProcess.ServiceControllerStatus]::Stopped,
            [TimeSpan]::FromSeconds(20)
        )
    }
    $service.Dispose()
    Invoke-Checked -FilePath "sc.exe" -ArgumentList @("delete", $serviceName)
}
Stop-EZGameLANClient

if (Test-Path -LiteralPath $adapterMarker -PathType Leaf) {
    $originalAdapterName = (
        Get-Content -LiteralPath $adapterMarker -Raw
    ).Trim()
    if ($originalAdapterName -match "^[^\r\n]{1,128}$" `
        -and $originalAdapterName -ne "EZGameLAN") {
        $ezAdapter = Get-NetAdapter -IncludeHidden -Name "EZGameLAN" `
            -ErrorAction SilentlyContinue
        $nameConflict = Get-NetAdapter -IncludeHidden -Name $originalAdapterName `
            -ErrorAction SilentlyContinue
        if ($null -ne $ezAdapter -and $null -eq $nameConflict) {
            Rename-NetAdapter -Name "EZGameLAN" `
                -NewName $originalAdapterName -Confirm:$false
        }
    }
}

$protocolKey = "HKLM:\Software\Classes\ezgamelan"
if (Test-Path -LiteralPath $protocolKey) {
    $commandKey = Join-Path $protocolKey "shell\open\command"
    $command = ""
    if (Test-Path -LiteralPath $commandKey) {
        $command = [string](Get-Item -LiteralPath $commandKey).GetValue("")
    }
    if ($command -like "*\EZGameLAN\ezgamelan-client.exe*") {
        Remove-Item -LiteralPath $protocolKey -Recurse -Force
    }
}

$startMenuDir = Join-Path $env:ProgramData `
    "Microsoft\Windows\Start Menu\Programs\EZGameLAN"
if (Test-Path -LiteralPath $startMenuDir) {
    Remove-Item -LiteralPath $startMenuDir -Recurse -Force
}

Remove-Item -LiteralPath `
    "HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\EZGameLAN" `
    -Recurse -Force -ErrorAction SilentlyContinue

$removeOpenVPN = (Test-Path -LiteralPath $managedMarker -PathType Leaf) `
    -and (Test-Path -LiteralPath $cachedOpenVPNMSI -PathType Leaf)
if ($removeOpenVPN) {
    Invoke-Checked -FilePath "msiexec.exe" -ArgumentList @(
        "/x"
        "`"$cachedOpenVPNMSI`""
        "/qn"
        "/norestart"
    ) -SuccessExitCode @(0, 1605, 1614, 1641, 3010)
}

if (Test-Path -LiteralPath $dataDir) {
    Remove-Item -LiteralPath $dataDir -Recurse -Force
}
if (Test-Path -LiteralPath $installDir) {
    Remove-Item -LiteralPath $installDir -Recurse -Force
}
if (Test-Path -LiteralPath $localDataDir) {
    Remove-Item -LiteralPath $localDataDir -Recurse -Force
}
