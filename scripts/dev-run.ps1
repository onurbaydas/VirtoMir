<#
.SYNOPSIS
    Builds virtomird for Linux on Windows, copies it to the dev host and (re)starts it.

.DESCRIPTION
    Runs the daemon on the dev host as a transient systemd unit "virtomird-dev" (user onur,
    which is in the libvirt group). The API is then reachable from Windows at http://<host>:8420.

    Logs:  ssh virtomir-dev journalctl -u virtomird-dev -f
    Stop:  ssh virtomir-dev sudo systemctl stop virtomird-dev

.PARAMETER Pam
    Build on the dev host with cgo + PAM (-tags pam), install /etc/pam.d/virtomir and run with
    sign-in and a self-signed TLS certificate: https://<host>:8420, sign in as onur. Without it
    the daemon runs without sign-in over plain HTTP (fast inner loop).
    The dev unit runs as onur, not root, so PAM can only verify onur's own password.

.EXAMPLE
    ./scripts/dev-run.ps1
.EXAMPLE
    ./scripts/dev-run.ps1 -Pam
#>
[CmdletBinding()]
param(
    [string]$SshHost = 'virtomir-dev',
    [string]$Listen  = '0.0.0.0:8420',
    [switch]$Pam
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

ssh $SshHost 'mkdir -p ~/virtomir'
if ($LASTEXITCODE -ne 0) { throw "ssh $SshHost failed" }

if ($Pam) {
    # PAM needs cgo, so build on the dev host itself (Go is at /usr/local/go there).
    Write-Host "==> Shipping sources ($rev) to $SshHost for a PAM build" -ForegroundColor Cyan
    $tarball = Join-Path $repoRoot 'bin\virtomir-src.tar'
    New-Item -ItemType Directory -Force (Split-Path $tarball) | Out-Null
    Push-Location $repoRoot
    try {
        tar -cf $tarball --exclude=web/node_modules --exclude=bin --exclude=.git go.mod go.sum cmd internal web packaging
        if ($LASTEXITCODE -ne 0) { throw 'tar failed' }
    } finally { Pop-Location }
    scp -q $tarball "${SshHost}:virtomir/src.tar"
    if ($LASTEXITCODE -ne 0) { throw 'scp failed' }
    $build = @"
set -e
command -v gcc >/dev/null && dpkg -s libpam0g-dev >/dev/null 2>&1 || sudo apt-get install -y -qq gcc libpam0g-dev
rm -rf ~/virtomir/src && mkdir -p ~/virtomir/src && tar -xf ~/virtomir/src.tar -C ~/virtomir/src
cd ~/virtomir/src
CGO_ENABLED=1 /usr/local/go/bin/go build -tags pam -trimpath -ldflags "-s -w -X main.version=$rev-pam" -o ~/virtomir/virtomird.new ./cmd/virtomird
sudo install -m 0644 packaging/virtomir.pam /etc/pam.d/virtomir
"@
    $b64b = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes(($build -replace "`r`n", "`n")))
    Write-Host "==> Building virtomird with PAM on $SshHost" -ForegroundColor Cyan
    ssh $SshHost "echo $b64b | base64 -d | bash"
    if ($LASTEXITCODE -ne 0) { throw 'remote PAM build failed' }
    $flags = "-listen $Listen -auth pam -tls auto -state-dir `$HOME/virtomir/state"
    $scheme = 'https'
} else {
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
    scp -q $out "${SshHost}:virtomir/virtomird.new"
    if ($LASTEXITCODE -ne 0) { throw 'scp failed' }
    $flags = "-listen $Listen -auth none -tls off -state-dir `$HOME/virtomir/state"
    $scheme = 'http'
}

$remote = @"
set -e
mv ~/virtomir/virtomird.new ~/virtomir/virtomird
chmod +x ~/virtomir/virtomird
sudo systemctl stop virtomird-dev 2>/dev/null || true
sudo systemctl reset-failed virtomird-dev 2>/dev/null || true
sudo systemd-run --quiet --unit=virtomird-dev -p User=`$(id -un) `$HOME/virtomir/virtomird $flags
sleep 1
systemctl is-active virtomird-dev
"@
# Windows PowerShell 5.1 mangles quotes in native args -> ship the script base64-encoded.
$b64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes(($remote -replace "`r`n", "`n")))
ssh $SshHost "echo $b64 | base64 -d | bash"
if ($LASTEXITCODE -ne 0) { throw 'starting virtomird-dev failed' }

$hostName = (ssh -G $SshHost | Select-String '^hostname ' | ForEach-Object { ($_ -split ' ')[1] })
$port = ($Listen -split ':')[-1]
Write-Host "==> virtomird is running: ${scheme}://${hostName}:$port/" -ForegroundColor Green
