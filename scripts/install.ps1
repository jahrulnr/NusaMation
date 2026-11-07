<#
.SYNOPSIS
    NusaMation installer for Windows.

.DESCRIPTION
    Downloads the NusaMation release binary, verifies its SHA-256 checksum,
    installs it under %LOCALAPPDATA%\NusaMation\bin, adds that directory to
    the user PATH, creates the data directory and registers a hidden
    "NusaMation" scheduled task that starts the app at logon (and starts it
    right away). Re-running is safe: data is kept and the task is
    re-registered.

    Quick install (latest release):
        irm https://github.com/jahrulnr/NusaMation/releases/latest/download/install.ps1 | iex

    Pin a version (before piping):
        $env:NUSAMATION_VERSION = '0.1.0'
        irm https://github.com/jahrulnr/NusaMation/releases/latest/download/install.ps1 | iex

    Binary only, no scheduled task:
        powershell -ExecutionPolicy Bypass -File install.ps1 -NoService

    Uninstall (keeps data):
        powershell -ExecutionPolicy Bypass -File install.ps1 -Uninstall
#>
[CmdletBinding()]
param(
    [string]$Version = $env:NUSAMATION_VERSION,
    [string]$Repo = $env:NUSAMATION_REPO,
    [switch]$NoService,
    [switch]$Uninstall
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# $IsWindows is $null on Windows PowerShell 5.1, so only throw when it is
# explicitly $false (PowerShell Core on non-Windows).
if ($IsWindows -eq $false) {
    throw 'install.ps1 supports Windows only; on Linux run: curl -fsSL https://github.com/jahrulnr/NusaMation/releases/latest/download/install.sh | bash'
}
if ([string]::IsNullOrWhiteSpace($Repo)) { $Repo = 'jahrulnr/NusaMation' }

$installDir = Join-Path $env:LOCALAPPDATA 'NusaMation\bin'
$binary = Join-Path $installDir 'nusamation.exe'
$dataDir = Join-Path $env:USERPROFILE '.nusamation'
$taskName = 'NusaMation'

function Get-ReleaseUrl([string]$Asset) {
    if ([string]::IsNullOrWhiteSpace($Version) -or $Version -eq 'latest') {
        "https://github.com/$Repo/releases/latest/download/$Asset"
    } else {
        $tag = 'v' + $Version.TrimStart('v')
        "https://github.com/$Repo/releases/download/$tag/$Asset"
    }
}

function Remove-FromUserPath([string]$Dir) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ([string]::IsNullOrWhiteSpace($userPath)) { return }
    $dir = $Dir.TrimEnd('\')
    $parts = @($userPath -split ';' | Where-Object { $_ -and $_.TrimEnd('\') -ne $dir })
    [Environment]::SetEnvironmentVariable('Path', ($parts -join ';'), 'User')
}

if ($Uninstall) {
    if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
        Write-Host "Removed scheduled task '$taskName'."
    }
    if (Test-Path -LiteralPath $binary) {
        Remove-Item -LiteralPath $binary -Force
        Write-Host "Removed $binary"
    }
    Remove-FromUserPath $installDir
    Write-Host "Data was kept at $dataDir"
    Write-Host 'Delete it manually to purge.'
    exit 0
}

$procArch = $env:PROCESSOR_ARCHITECTURE
if ([string]::IsNullOrWhiteSpace($procArch)) {
    $procArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
}
switch ($procArch) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    default { throw "unsupported architecture: $procArch" }
}

$assetName = "nusamation-windows-$arch.exe"
$tmp = Join-Path $env:TEMP ("nusamation-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    $binUrl = Get-ReleaseUrl $assetName
    $sumUrl = Get-ReleaseUrl 'SHA256SUMS'
    Write-Host "Downloading $binUrl"
    Invoke-WebRequest -Uri $binUrl -OutFile (Join-Path $tmp $assetName) -UseBasicParsing
    Invoke-WebRequest -Uri $sumUrl -OutFile (Join-Path $tmp 'SHA256SUMS') -UseBasicParsing

    $expected = Get-Content (Join-Path $tmp 'SHA256SUMS') |
        Where-Object { $_ -like "*$assetName" } |
        ForEach-Object { ($_ -split '\s+')[0] } |
        Select-Object -First 1
    if (-not $expected) { throw "no checksum found for $assetName in SHA256SUMS" }
    $actual = (Get-FileHash (Join-Path $tmp $assetName) -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected.ToLowerInvariant()) { throw "checksum mismatch for $assetName" }
    Write-Host 'Checksum verified (SHA-256).'

    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $tmp $assetName) -Destination $binary -Force
    Write-Host "Installed $binary"
}
finally {
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

# Add to user PATH (visible in new terminals).
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$dir = $installDir.TrimEnd('\')
$hasEntry = @($userPath -split ';' | Where-Object { $_ -and $_.TrimEnd('\') -eq $dir })
if (-not $hasEntry) {
    $entry = if ([string]::IsNullOrWhiteSpace($userPath)) { $installDir } else { $installDir + ';' + $userPath }
    [Environment]::SetEnvironmentVariable('Path', $entry, 'User')
    Write-Host "Added $installDir to the user PATH (new terminals only)."
}

# Data directory (the app defaults to ~/.nusamation anyway; create it up front).
New-Item -ItemType Directory -Path $dataDir -Force | Out-Null

# Hidden logon task: autostart at logon without a console window.
if (-not $NoService) {
    $action = New-ScheduledTaskAction -Execute $binary
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
    $settings = New-ScheduledTaskSettingsSet -Hidden `
        -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
        -MultipleInstances IgnoreNew
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Settings $settings -Force | Out-Null
    Start-ScheduledTask -TaskName $taskName
    Write-Host "Registered and started scheduled task '$taskName' (logon autostart)."
} else {
    Write-Host "Skipped scheduled task (-NoService). Start NusaMation manually with: $binary"
}

Write-Host ''
Write-Host 'NusaMation is installed.'
Write-Host "  binary: $binary"
Write-Host "  data:   $dataDir"
if (-not $NoService) {
    Write-Host "  task:   $taskName (logon autostart)"
    Write-Host '  stop:   schtasks /End /TN NusaMation'
    Write-Host '  start:  schtasks /Run /TN NusaMation'
} else {
    Write-Host "  start:  $binary"
}
Write-Host ''
Write-Host 'If this is a first run, read the generated password from the log file:'
Write-Host "  Get-Content (Join-Path '$dataDir' 'logs\nusamation.log')"