<#
.SYNOPSIS
    Regression test for scripts/install.ps1, mirroring scripts/test/run.sh.

.DESCRIPTION
    Runs the installer in-session against a fake release directory with
    stubbed Invoke-WebRequest and Scheduled Task cmdlets, then verifies:
      - pinned and "latest" installs place the binary, update the user PATH
        and register/start the logon task
      - re-installing is idempotent
      - a tampered binary fails checksum verification and aborts
      - uninstall removes the binary and the PATH entry but keeps data

    The user PATH is saved before the run and restored afterwards; the
    Scheduled Task cmdlets are stubs, so nothing is registered for real.

    Usage: pwsh -NoProfile -File scripts/test/run.ps1   (Windows only)
#>
$ErrorActionPreference = 'Stop'

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$installer = Join-Path $scriptDir '..\install.ps1'
$tempBase = $env:TEMP
if ([string]::IsNullOrWhiteSpace($tempBase)) { $tempBase = $env:TMPDIR }
if ([string]::IsNullOrWhiteSpace($tempBase)) { $tempBase = '/tmp' }
$testRoot = Join-Path $tempBase ("nusamation-test-" + [guid]::NewGuid().ToString('N'))
$release = Join-Path $testRoot 'release'
$tampered = Join-Path $testRoot 'tampered'
New-Item -ItemType Directory -Path $release, $tampered | Out-Null

$script:failures = 0
function Fail([string]$msg) { Write-Host "FAIL: $msg"; $script:failures++ }
function Pass([string]$msg) { Write-Host "ok: $msg" }

# --- fake release assets -------------------------------------------------
$asset = 'nusamation-windows-amd64.exe'
Set-Content -Path (Join-Path $release $asset) -Value 'fake binary' -NoNewline
$sha = (Get-FileHash (Join-Path $release $asset) -Algorithm SHA256).Hash.ToLowerInvariant()
Set-Content -Path (Join-Path $release 'SHA256SUMS') -Value "$sha  $asset"
Copy-Item (Join-Path $release $asset) (Join-Path $tampered $asset)
Set-Content -Path (Join-Path $tampered 'SHA256SUMS') -Value "$sha  $asset"
Add-Content -Path (Join-Path $tampered $asset) -Value 'tampered' # corrupt after checksums

# --- stubs ---------------------------------------------------------------
# The installer runs in its own script scope, so $script: variables set
# inside these stubs would not be visible here; use marker files instead.
$markerDir = Join-Path $testRoot 'markers'
New-Item -ItemType Directory -Path $markerDir | Out-Null
$urlsFile = Join-Path $markerDir 'urls.txt'

function Invoke-WebRequest {
    param([string]$Uri, [string]$OutFile, [switch]$UseBasicParsing)
    Add-Content -Path $urlsFile -Value $Uri
    $name = ($Uri -split '/')[-1]
    $src = Join-Path $env:FAKE_RELEASE $name
    if (-not (Test-Path $src)) { throw "stub: missing asset $name" }
    Copy-Item $src $OutFile
}
function New-ScheduledTaskAction { param([string]$Execute) }
function New-ScheduledTaskTrigger { param() }
function New-ScheduledTaskSettingsSet { param() }
function Register-ScheduledTask { Set-Content -Path (Join-Path $markerDir 'registered') -Value '1' }
function Start-ScheduledTask { Set-Content -Path (Join-Path $markerDir 'started') -Value '1' }
function Get-ScheduledTask { $null }
function Stop-ScheduledTask { }
function Unregister-ScheduledTask { }

# --- isolated environment -------------------------------------------------
$oldLocal = $env:LOCALAPPDATA
$oldProfile = $env:USERPROFILE
$oldTemp = $env:TEMP
$oldVersion = $env:NUSAMATION_VERSION
$oldRepo = $env:NUSAMATION_REPO
$oldRelease = $env:FAKE_RELEASE
$userPathBefore = [Environment]::GetEnvironmentVariable('Path', 'User')

try {
    $env:LOCALAPPDATA = Join-Path $testRoot 'local'
    $env:USERPROFILE = Join-Path $testRoot 'profile'
    $env:TEMP = Join-Path $testRoot 'temp'
    $env:NUSAMATION_VERSION = '0.1.0'
    $env:NUSAMATION_REPO = 'local/fake'
    $env:FAKE_RELEASE = $release
    New-Item -ItemType Directory -Path $env:LOCALAPPDATA, $env:USERPROFILE, $env:TEMP | Out-Null
    $installDir = Join-Path $env:LOCALAPPDATA 'NusaMation\bin'
    $binary = Join-Path $installDir 'nusamation.exe'
    $dataDir = Join-Path $env:USERPROFILE '.nusamation'

    # 1. Pinned install (with scheduled task).
    & $installer | Out-Null
    if (Test-Path $binary) { Pass 'pinned install' } else { Fail 'binary not installed' }
    if ((Test-Path (Join-Path $markerDir 'registered')) -and (Test-Path (Join-Path $markerDir 'started'))) {
        Pass 'scheduled task registered and started'
    } else {
        Fail 'scheduled task stubs not invoked'
    }
    if (([Environment]::GetEnvironmentVariable('Path', 'User') -split ';') -contains $installDir.TrimEnd('\')) {
        Pass 'user PATH updated'
    } else {
        Fail 'user PATH not updated'
    }
    $lastUri = Get-Content $urlsFile | Select-Object -Last 1
    if ($lastUri -like '*releases/download/v0.1.0/*') {
        Pass 'pinned URL used'
    } else {
        Fail "pinned URL not used: $lastUri"
    }

    # 2. Re-install is idempotent.
    & $installer | Out-Null
    Pass 're-install ok'

    # 3. "latest" (unpinned) URL path.
    $env:NUSAMATION_VERSION = ''
    & $installer | Out-Null
    $lastUri = Get-Content $urlsFile | Select-Object -Last 1
    if ($lastUri -like '*releases/latest/download/*') {
        Pass 'latest URL used'
    } else {
        Fail "latest URL not used: $lastUri"
    }

    # 4. Tampered checksum aborts before installing.
    $env:NUSAMATION_VERSION = '0.1.0'
    $env:FAKE_RELEASE = $tampered
    Remove-Item -LiteralPath $installDir -Recurse -Force -ErrorAction SilentlyContinue
    $threw = $false
    try { & $installer | Out-Null } catch { $threw = $true }
    if ($threw -and -not (Test-Path $binary)) {
        Pass 'tampered checksum aborts'
    } else {
        Fail 'tampered install did not abort cleanly'
    }

    # 5. Uninstall removes binary and PATH entry but keeps data.
    $env:FAKE_RELEASE = $release
    & $installer | Out-Null
    & $installer -Uninstall | Out-Null
    if (-not (Test-Path $binary)) { Pass 'uninstall removed binary' } else { Fail 'uninstall left the binary' }
    if (([Environment]::GetEnvironmentVariable('Path', 'User') -split ';') -notcontains $installDir.TrimEnd('\')) {
        Pass 'uninstall removed PATH entry'
    } else {
        Fail 'uninstall left the PATH entry'
    }
    if (Test-Path $dataDir) { Pass 'uninstall keeps data' } else { Fail 'uninstall removed data' }
}
finally {
    $env:LOCALAPPDATA = $oldLocal
    $env:USERPROFILE = $oldProfile
    $env:TEMP = $oldTemp
    $env:NUSAMATION_VERSION = $oldVersion
    $env:NUSAMATION_REPO = $oldRepo
    $env:FAKE_RELEASE = $oldRelease
    [Environment]::SetEnvironmentVariable('Path', $userPathBefore, 'User')
    Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
}

if ($script:failures -gt 0) {
    Write-Host "$($script:failures) test(s) failed"
    exit 1
}
Write-Host 'all installer tests passed'