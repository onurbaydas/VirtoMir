<#
.SYNOPSIS
    Pulls the latest main from GitHub and redeploys the dev hosts when something changed.

.DESCRIPTION
    Fast-forward only: a working copy with local changes or local commits is left alone
    (logged). Deploys with dev-run.ps1 -AsRoot to every host in -Hosts.
    Log: %LOCALAPPDATA%\VirtoMir\auto-update.log

.EXAMPLE
    ./scripts/auto-update.ps1            # once, now
    ./scripts/auto-update.ps1 -Install   # every 10 minutes (Windows Task Scheduler)
    ./scripts/auto-update.ps1 -Uninstall
#>
[CmdletBinding()]
param(
    [string[]]$Hosts = @('virtomir-dev', 'virtomir-dev2'),
    [int]$Minutes = 10,
    [switch]$Install,
    [switch]$Uninstall,
    [switch]$Force
)

$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$task = 'VirtoMir auto-update'
$logDir = Join-Path $env:LOCALAPPDATA 'VirtoMir'
New-Item -ItemType Directory -Force $logDir | Out-Null
$log = Join-Path $logDir 'auto-update.log'
function Log($msg) { "$(Get-Date -Format s)  $msg" | Tee-Object -FilePath $log -Append }

if ($Uninstall) {
    Unregister-ScheduledTask -TaskName $task -Confirm:$false -ErrorAction SilentlyContinue
    Log 'scheduled task removed'
    return
}
if ($Install) {
    $action = New-ScheduledTaskAction -Execute 'powershell.exe' `
        -Argument "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$PSCommandPath`" -Hosts $($Hosts -join ',')"
    $trigger = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) -RepetitionInterval (New-TimeSpan -Minutes $Minutes)
    $settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -StartWhenAvailable -ExecutionTimeLimit (New-TimeSpan -Minutes 30)
    Register-ScheduledTask -TaskName $task -Action $action -Trigger $trigger -Settings $settings -Force | Out-Null
    Log "scheduled every $Minutes min for $($Hosts -join ', ')"
    return
}

Set-Location $repo
git fetch -q origin main
if ($LASTEXITCODE -ne 0) { Log 'fetch failed'; exit 1 }
$local = git rev-parse HEAD
$remote = git rev-parse origin/main
if ($local -eq $remote -and -not $Force) { return }   # nothing new: stay quiet
if (git status --porcelain --untracked-files=no) { Log 'local changes: not updating'; exit 0 }
if ((git rev-list --count "origin/main..HEAD") -ne '0') { Log 'local commits not on GitHub: not updating'; exit 0 }
if ($local -ne $remote) {
    git merge -q --ff-only origin/main
    if ($LASTEXITCODE -ne 0) { Log 'fast-forward failed'; exit 1 }
    Log "updated $($local.Substring(0,7)) -> $($remote.Substring(0,7))"
}
$ErrorActionPreference = 'Continue' # build tools write warnings to stderr
foreach ($h in $Hosts) {
    # cmd does the redirection: PowerShell 5.1 would turn the build's stderr into errors.
    cmd /c "powershell.exe -NoProfile -ExecutionPolicy Bypass -File `"$PSScriptRoot\dev-run.ps1`" -SshHost $h -AsRoot > `"$logDir\deploy-$h.log`" 2>&1"
    if ($LASTEXITCODE -eq 0) { Log "deployed to $h" } else { Log "deploy to $h failed (see deploy-$h.log)" }
}
