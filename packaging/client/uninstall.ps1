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
    Stop-Process -Name "ezgamelan-client" -Force `
        -ErrorAction SilentlyContinue
    $deadline = [DateTime]::UtcNow.AddSeconds(5)
    while ([DateTime]::UtcNow -lt $deadline) {
        if (@(Get-Process -Name "ezgamelan-client" `
            -ErrorAction SilentlyContinue).Count -eq 0) {
            return
        }
        Start-Sleep -Milliseconds 100
    }
    throw "停止 EZGameLAN 客户端失败，请手动结束进程后重新运行卸载程序。"
}

function Stop-EZGameLANService {
    $service = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
    if ($null -eq $service) {
        return
    }
    try {
        $stopped = [System.ServiceProcess.ServiceControllerStatus]::Stopped
        $service.Refresh()
        if ($service.Status -eq $stopped) {
            return
        }
        try {
            $service.Stop()
        }
        catch {
            # The service may have changed state between Refresh and Stop.
        }

        $deadline = [DateTime]::UtcNow.AddSeconds(10)
        while ([DateTime]::UtcNow -lt $deadline) {
            $service.Refresh()
            if ($service.Status -eq $stopped) {
                return
            }
            Start-Sleep -Milliseconds 200
        }

        $processes = @(Get-Process -Name "ezgamelan-service" `
            -ErrorAction SilentlyContinue)
        foreach ($process in $processes) {
            & taskkill.exe /PID ([string]$process.Id) /T /F `
                2>$null | Out-Null
            $process.Dispose()
        }

        $deadline = [DateTime]::UtcNow.AddSeconds(5)
        while ([DateTime]::UtcNow -lt $deadline) {
            $service.Refresh()
            if ($service.Status -eq $stopped) {
                return
            }
            Start-Sleep -Milliseconds 200
        }
        throw "停止 EZGameLAN 服务失败，请重启 Windows 后重新运行卸载程序。"
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
