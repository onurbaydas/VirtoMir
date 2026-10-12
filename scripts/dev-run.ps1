<#
.SYNOPSIS
    Builds virtomird for Linux on Windows, copies it to the dev host and (re)starts it.

.DESCRIPTION
    Runs the daemon on the dev host as a transient systemd unit "virtomird-dev" (user onur,
    which is in the libvirt group). The API is then reachable from Windows at http://<host>:8420.

    Logs:  ssh virtomir-dev journalctl -u virtomird-dev -f
    Stop:  ssh virtomir-dev sudo systemctl stop virtomird-dev

.EXAMPLE
    ./scripts/dev-run.ps1
#>
[CmdletBinding()]
param(
    [string]$SshHost = 'virtomir-dev',
    [string]$Listen  = '0.0.0.0:8420'
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$out      = Join-Path $repoRoot 'bin\virtomird-linux-amd64'

$go = Get-Command go -ErrorAction SilentlyContinue
$goExe = if ($go) { $go.Source } else { Join-Path $env:LOCALAPPDATA 'Programs\go\bin\go.exe' }
if (-not (Test-Path $goExe)) { throw 'Go not found. Install it or add it to PATH.' }

$npm = Get-Command npm.cmd -ErrorAction SilentlyContinue
$npmCmd = if ($npm) { $npm.Source } else { Join-Path $env:LOCALAPPDATA 'Programs\nodejs\npm.cmd' }
if (-not (Test-Path $npmCmd)) { throw 'Node.js/npm not found. Install it or add it to PATH.' }
$env:Path = "$(Split-Path $npmCmd);$env:Path"   # npm scripts need node on PATH

Write-Host '==> Building web UI' -ForegroundColor Cyan
try {
    Push-Location (Join-Path $repoRoot 'web')
    if (-not (Test-Path 'node_modules')) {
        & $npmCmd ci --no-fund --no-audit
        if ($LASTEXITCODE -ne 0) { throw 'npm ci failed' }
    }
    & $npmCmd run build --silent
    if ($LASTEXITCODE -ne 0) { throw 'web build failed' }
} finally { Pop-Location }

$rev = (git -C $repoRoot rev-parse --short HEAD).Trim()
if (git -C $repoRoot status --porcelain) { $rev += '-dirty' }

Write-Host "==> Building virtomird ($rev) for linux/amd64" -ForegroundColor Cyan
$env:GOOS = 'linux'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
try {
    Push-Location $repoRoot
    & $goExe build -trimpath -ldflags "-s -w -X main.version=$rev" -o $out ./cmd/virtomird
    if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
} finally {
    Pop-Location
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
}

Write-Host "==> Deploying to $SshHost" -ForegroundColor Cyan
ssh $SshHost 'mkdir -p ~/virtomir'
if ($LASTEXITCODE -ne 0) { throw "ssh $SshHost failed" }
scp -q $out "${SshHost}:virtomir/virtomird.new"
if ($LASTEXITCODE -ne 0) { throw 'scp failed' }

$remote = @"
set -e
mv ~/virtomir/virtomird.new ~/virtomir/virtomird
chmod +x ~/virtomir/virtomird
sudo systemctl stop virtomird-dev 2>/dev/null || true
sudo systemctl reset-failed virtomird-dev 2>/dev/null || true
sudo systemd-run --quiet --unit=virtomird-dev -p User=`$(id -un) `$HOME/virtomir/virtomird -listen $Listen
sleep 1
systemctl is-active virtomird-dev
"@
# Windows PowerShell 5.1 mangles quotes in native args -> ship the script base64-encoded.
$b64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes(($remote -replace "`r`n", "`n")))
ssh $SshHost "echo $b64 | base64 -d | bash"
if ($LASTEXITCODE -ne 0) { throw 'starting virtomird-dev failed' }

$hostName = (ssh -G $SshHost | Select-String '^hostname ' | ForEach-Object { ($_ -split ' ')[1] })
$port = ($Listen -split ':')[-1]
Write-Host "==> virtomird is running: http://${hostName}:$port/api/v1/domains" -ForegroundColor Green
