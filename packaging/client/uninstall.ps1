[CmdletBinding()]
param(
    [switch]$Quiet
)

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
        try {
            if (-not $process.HasExited) {
                $process.Kill()
            }
            if (-not $process.WaitForExit(5000)) {
                throw "停止 EZGameLAN 客户端超过 5 秒，请结束进程后重新运行卸载程序。"
            }
        }
        finally {
            $process.Dispose()
        }
    }
}

function Stop-EZGameLANService {
    $service = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
    if ($null -eq $service) {
        return
    }
    try {
        $service.Refresh()
        $stopped = [System.ServiceProcess.ServiceControllerStatus]::Stopped
        $stopPending = [System.ServiceProcess.ServiceControllerStatus]::StopPending
        if ($service.Status -eq $stopped) {
            return
        }
        if ($service.Status -ne $stopPending) {
            $service.Stop()
        }
        try {
            $service.WaitForStatus($stopped, [TimeSpan]::FromSeconds(20))
        }
        catch [System.ServiceProcess.TimeoutException] {
            throw "停止 EZGameLAN 服务超过 20 秒，请重启 Windows 后重新运行卸载程序。"
        }
        $service.Refresh()
        if ($service.Status -ne $stopped) {
            throw "EZGameLAN 服务停止后状态异常，请重启 Windows 后重新运行卸载程序。"
        }
    }
    finally {
        $service.Dispose()
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
    if ($Quiet) {
        $arguments += "-Quiet"
    }
    $startParameters = @{
        FilePath = "powershell.exe"
        ArgumentList = $arguments
        Verb = "RunAs"
        Wait = $true
        PassThru = $true
    }
    if ($Quiet) {
        $startParameters["WindowStyle"] = "Hidden"
    }
    $process = Start-Process @startParameters
    exit $process.ExitCode
}

Stop-EZGameLANClient
Stop-EZGameLANService
$service = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
if ($null -ne $service) {
    $service.Dispose()
    Invoke-Checked -FilePath "sc.exe" -ArgumentList @("delete", $serviceName)
}

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
