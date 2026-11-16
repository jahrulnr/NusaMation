<#
.SYNOPSIS
    NusaMation installer for Windows.

.DESCRIPTION
    Downloads the NusaMation release binary, verifies its SHA-256 checksum,
    installs it under %LOCALAPPDATA%\NusaMation\bin, adds that directory to
    the user PATH, creates the data directory and registers a hidden
    "NusaMation" scheduled task that starts the app at logon (and starts it
    right away). The app hides its console window on launch, so closing a
    terminal can never kill the server. Re-running is safe: data is kept
    and the task is re-registered.

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
$iconFile = Join-Path $env:LOCALAPPDATA 'NusaMation\nusamation.ico'
$notificationIcon = Join-Path $env:LOCALAPPDATA 'NusaMation\notification-icon.png'
$notificationKey = 'HKCU:\Software\Classes\AppUserModelId\NusaMation.Desktop'
$shortcut = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\NusaMation.url'

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
    Remove-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name $taskName -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $binary) {
        Remove-Item -LiteralPath $binary -Force
        Write-Host "Removed $binary"
    }
    Remove-FromUserPath $installDir
    $registeredIcon = (Get-ItemProperty -LiteralPath $notificationKey -Name IconUri -ErrorAction SilentlyContinue).IconUri
    if ($registeredIcon -eq $notificationIcon) { Remove-Item -LiteralPath $notificationKey -Force -ErrorAction SilentlyContinue }
    Remove-Item -LiteralPath $shortcut, $iconFile, $notificationIcon -Force -ErrorAction SilentlyContinue
    Write-Host "Data was kept at $dataDir"
    Write-Host 'Delete it manually to purge.'
    # return instead of exit: exit would terminate the caller's session when
    # the script is invoked with & (tests, wrappers).
    return
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
    Invoke-WebRequest -Uri (Get-ReleaseUrl 'nusamation-icon.ico') -OutFile (Join-Path $tmp 'nusamation-icon.ico') -UseBasicParsing

    $expected = Get-Content (Join-Path $tmp 'SHA256SUMS') |
        Where-Object { $_ -like "*$assetName" } |
        ForEach-Object { ($_ -split '\s+')[0] } |
        Select-Object -First 1
    if (-not $expected) { throw "no checksum found for $assetName in SHA256SUMS" }
    $actual = (Get-FileHash (Join-Path $tmp $assetName) -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected.ToLowerInvariant()) { throw "checksum mismatch for $assetName" }
    $iconExpected = Get-Content (Join-Path $tmp 'SHA256SUMS') | Where-Object { $_ -match '\s+nusamation-icon\.ico$' } | ForEach-Object { ($_ -split '\s+')[0] } | Select-Object -First 1
    if (-not $iconExpected -or (Get-FileHash (Join-Path $tmp 'nusamation-icon.ico') -Algorithm SHA256).Hash.ToLowerInvariant() -ne $iconExpected.ToLowerInvariant()) { throw 'checksum mismatch for nusamation-icon.ico' }
    Write-Host 'Checksum verified (SHA-256).'

    # A running exe is locked and cannot be overwritten: stop the scheduled
    # task instance and any manually-started copy of this binary first, then
    # wait until the image is actually unmapped. Stop-ScheduledTask returns
    # before that happens and the process is already gone from the process
    # list, so a copy straight after it fails with a sharing violation - which
    # used to abort an upgrade and leave the server down.
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    for ($i = 0; $i -lt 60; $i++) {
        $running = Get-Process nusamation -ErrorAction SilentlyContinue |
            Where-Object { $_.Path -eq $binary }
        # $null piped into Stop-Process throws a binding error that
        # -ErrorAction does not catch, so only run it when something matched.
        if ($running) {
            $running | Stop-Process -Force -ErrorAction SilentlyContinue
            Start-Sleep -Milliseconds 250
            continue
        }
        if (-not (Test-Path -LiteralPath $binary)) { break }
        # The process is gone; prove the image is writable before copying.
        try {
            $probe = [IO.File]::Open($binary, 'Open', 'ReadWrite', 'None')
            $probe.Close()
            break
        } catch { Start-Sleep -Milliseconds 250 }
    }

    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    $copied = $false
    for ($i = 0; $i -lt 5 -and -not $copied; $i++) {
        try {
            Copy-Item -LiteralPath (Join-Path $tmp $assetName) -Destination $binary -Force
            $copied = $true
        } catch {
            Start-Sleep -Seconds 1
        }
    }
    if (-not $copied) {
        throw "could not replace $binary - a nusamation process may still be running; stop it and re-run"
    }
    Copy-Item -LiteralPath (Join-Path $tmp 'nusamation-icon.ico') -Destination $iconFile -Force
    New-Item -ItemType Directory -Path (Split-Path -Parent $shortcut) -Force | Out-Null
    Set-Content -LiteralPath $shortcut -Encoding ASCII -Value "[InternetShortcut]`r`nURL=http://127.0.0.1:8787`r`nIconFile=$iconFile`r`nIconIndex=0"
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
# The action stays the binary itself (with -hide-console so the app hides
# its own window right after launch) so 'schtasks /End /TN NusaMation'
# still stops the real process - a wrapper would orphan it.
#
# The principal and trigger both use the installing user's DOMAIN\name: a bare
# $env:USERNAME can differ from the account the trigger matches on Microsoft-
# account systems, which leaves the task firing for a user who never logs on.
# Interactive logon type keeps the task tied to that user's session - no
# password is stored and it cannot run headless under the wrong profile.
if (-not $NoService) {
    $me = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
    $action = New-ScheduledTaskAction -Execute $binary -Argument '-hide-console'
    # Two triggers instead of mutating a CimInstance: Windows PowerShell and
    # PowerShell 7 both reject property assignment on the objects these cmdlets
    # return, and a repetition only works when it is part of the trigger that
    # was built by the cmdlet itself.
    #
    # - AtLogOn: the normal start.
    # - Once + RepetitionInterval: the watchdog. MultipleInstances=IgnoreNew
    #   makes each tick a no-op while the server is healthy, and
    #   -StartWhenAvailable turns the missed start after a reboot into an
    #   immediate run, so a dead server is back within five minutes instead of
    #   waiting for the next logon.
    $triggers = @(
        (New-ScheduledTaskTrigger -AtLogOn -User $me)
        (New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) `
            -RepetitionInterval (New-TimeSpan -Minutes 5) `
            -RepetitionDuration (New-TimeSpan -Days 3650))
    )
    $principal = New-ScheduledTaskPrincipal -UserId $me -LogonType Interactive -RunLevel Limited
    # -AllowStartIfOnBatteries matters on laptops: the default settings silently
    # skip a logon start when the machine is unplugged, and stop a running
    # server when it switches to battery - a server has no reason to care.
    $settings = New-ScheduledTaskSettingsSet -Hidden `
        -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -StartWhenAvailable `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
        -MultipleInstances IgnoreNew
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $triggers -Principal $principal -Settings $settings -Force | Out-Null
    Start-ScheduledTask -TaskName $taskName
    Write-Host "Registered and started scheduled task '$taskName' (logon autostart for $me, runs hidden)."

    # Second, independent autostart path. Task Scheduler is the one Windows
    # component debloated/custom images most often break, and a task that never
    # fires leaves no process to restart. The Run key is started by Explorer at
    # logon; whichever mechanism loses the race exits immediately because the
    # app takes a per-data-dir instance lock before touching the database.
    $runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
    New-Item -Path $runKey -Force | Out-Null
    Set-ItemProperty -Path $runKey -Name $taskName -Value ('"' + $binary + '" -hide-console')
    Write-Host "Registered the $taskName Run key as a fallback autostart path."

    # Record who/what was registered - post-mortem data for "works until reboot".
    @(
        "installed:  $(Get-Date -Format o)",
        "whoami:     $me",
        "principal:  $me (Interactive, Limited)",
        "trigger:    AtLogOn user=$me, repeats every 5m",
        "fallback:   HKCU Run key (single-instance guarded)",
        "binary:     $binary",
        "data:       $dataDir"
    ) | Set-Content -LiteralPath (Join-Path $installDir 'install-info.txt') -Encoding UTF8

    # Confirm the app actually answers - a silent bind failure (reserved port
    # range, held port) is otherwise invisible until the next reboot. A task
    # instance can also sit queued/refused forever, so one stop+start is tried
    # before reporting failure: that is the only recovery a user has short of
    # re-running this script.
    $up = $false
    for ($i = 0; $i -lt 20 -and -not $up; $i++) {
        Start-Sleep -Seconds 1
        try { $up = (Invoke-WebRequest -Uri 'http://127.0.0.1:8787/api/health' -UseBasicParsing -TimeoutSec 2).StatusCode -eq 200 } catch {}
    }
    if (-not $up) {
        Write-Host 'No answer yet - restarting the task once.'
        Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 2
        Start-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        for ($i = 0; $i -lt 20 -and -not $up; $i++) {
            Start-Sleep -Seconds 1
            try { $up = (Invoke-WebRequest -Uri 'http://127.0.0.1:8787/api/health' -UseBasicParsing -TimeoutSec 2).StatusCode -eq 200 } catch {}
        }
    }
    if ($up) {
        Write-Host 'Server is up at http://127.0.0.1:8787'
    } else {
        Write-Warning 'The task started but http://127.0.0.1:8787 is not responding.'
        $held = Get-NetTCPConnection -LocalPort 8787 -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($held) {
            $owner = Get-Process -Id $held.OwningProcess -ErrorAction SilentlyContinue
            Write-Host "The port is held by PID $($held.OwningProcess) ($($owner.ProcessName))."
        }
        Write-Host 'Diagnose with:'
        Write-Host '  Get-ScheduledTaskInfo NusaMation                       # LastTaskResult'
        Write-Host '  netstat -ano | findstr :8787                           # anything listening?'
        Write-Host '  netsh int ipv4 show excludedportrange protocol=tcp     # port reserved by Windows?'
        Write-Host "  Get-Content (Join-Path '$dataDir' 'logs\nusamation.log') -Tail 30"
    }
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
