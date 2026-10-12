<#
.SYNOPSIS
    Creates a fully configured VirtoMir dev host on Hyper-V with ONE command:
    Debian 13 cloud image + cloud-init + nested virtualization + KVM/QEMU/libvirt.

.DESCRIPTION
    Zero interactive installation. The only prompt is a single UAC confirmation.

      1. (user)  Creates an SSH key  ~/.ssh/virtomir_dev_ed25519  (if missing)
      2. (user)  Downloads + SHA512-verifies the official Debian 13 "generic" cloud image (cached)
      3. (user)  Turns disk.raw into a fixed VHD (appends a VHD footer - no qemu-img needed)
      4. (user)  Renders dev/hyperv/cloud-init/user-data.yaml and packs a NoCloud seed ISO (label "cidata")
      5. (admin) Converts to dynamic VHDX, resizes, creates a Gen1 VM with nested virtualization,
                 MAC spoofing, static memory; attaches the seed ISO; starts it; waits for its IP
      6. (user)  Writes a "virtomir-dev" entry to ~/.ssh/config and waits for cloud-init to finish
      7. (user)  Prints KVM / libvirt validation results

    Cache & logs: %LOCALAPPDATA%\VirtoMir\devhost\
    Password    : -Password, or VM_PASSWORD=... in .ai/local/devhost.env (gitignored), or prompt.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File dev\hyperv\New-VirtoMirDevHost.ps1
.EXAMPLE
    powershell -ExecutionPolicy Bypass -File dev\hyperv\New-VirtoMirDevHost.ps1 -Force -CpuCount 8 -MemoryGB 12
#>
[CmdletBinding()]
param(
    [string]$VMName     = 'virtomir-dev',
    [int]   $CpuCount   = 6,
    [int]   $MemoryGB   = 8,
    [int]   $DiskGB     = 100,
    [string]$SwitchName = 'Default Switch',
    [string]$UserName   = 'onur',
    [string]$Password,
    [switch]$Force,
    [string]$ImageBaseUrl = 'https://cloud.debian.org/images/cloud/trixie/latest',
    [string]$ImageName    = 'debian-13-generic-amd64.tar.xz',

    # --- internal: used when the script re-launches itself elevated ---
    [ValidateSet('all', 'hyperv')][string]$Stage = 'all',
    [string]$StateFile
)

$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'
$script:LogFile        = $null

# =============================================================================
# Helpers
# =============================================================================
function Write-Step([string]$Message) {
    Write-Host "==> $Message" -ForegroundColor Cyan
    if ($script:LogFile) { Add-Content -Path $script:LogFile -Value "==> $Message" -Encoding UTF8 }
}
function Write-Info([string]$Message) {
    Write-Host "    $Message" -ForegroundColor Gray
    if ($script:LogFile) { Add-Content -Path $script:LogFile -Value "    $Message" -Encoding UTF8 }
}

# Runs a native exe without PowerShell 5.1 turning stderr into terminating errors.
function Invoke-Native([string]$Exe, [string[]]$Arguments, [switch]$Quiet) {
    $old = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        if ($Quiet) { $out = & $Exe @Arguments 2>&1 | Out-String }
        else        { & $Exe @Arguments; $out = '' }
        return [pscustomobject]@{ Code = $LASTEXITCODE; Output = $out }
    } finally { $ErrorActionPreference = $old }
}

function Write-Utf8NoBomLf([string]$Path, [string]$Text) {
    $Text = $Text -replace "`r`n", "`n"
    [System.IO.File]::WriteAllText($Path, $Text, (New-Object System.Text.UTF8Encoding($false)))
}

# Appends a VHD (v1) footer to a raw disk image -> valid fixed VHD. Spec: Microsoft VHD Image Format.
function ConvertTo-FixedVhd([string]$Path) {
    $len = (Get-Item $Path).Length
    if ($len % 1MB -ne 0) {
        $fs = [System.IO.File]::Open($Path, 'Open', 'ReadWrite')
        try { $fs.SetLength([long]([math]::Ceiling($len / 1MB) * 1MB)) } finally { $fs.Close() }
        $len = (Get-Item $Path).Length
    }

    $footer = New-Object byte[] 512
    $put = {
        param([int]$Offset, [UInt64]$Value, [int]$Length)
        for ($i = $Length - 1; $i -ge 0; $i--) { $footer[$Offset + $i] = [byte]($Value -band 0xFF); $Value = $Value -shr 8 }
    }
    $ascii = [System.Text.Encoding]::ASCII

    # CHS geometry (algorithm from the VHD spec)
    $totalSectors = [long]($len / 512)
    $maxSectors   = [long]65535 * 16 * 255
    if ($totalSectors -gt $maxSectors) { $totalSectors = $maxSectors }
    if ($totalSectors -ge [long]65535 * 16 * 63) {
        $spt = 255; $heads = 16; $cth = [long][math]::Floor($totalSectors / $spt)
    } else {
        $spt = 17; $cth = [long][math]::Floor($totalSectors / $spt)
        $heads = [long][math]::Floor(($cth + 1023) / 1024)
        if ($heads -lt 4) { $heads = 4 }
        if ($cth -ge ($heads * 1024) -or $heads -gt 16) { $spt = 31; $heads = 16; $cth = [long][math]::Floor($totalSectors / $spt) }
        if ($cth -ge ($heads * 1024)) { $spt = 63; $heads = 16; $cth = [long][math]::Floor($totalSectors / $spt) }
    }
    $cyl = [long][math]::Floor($cth / $heads)

    $ascii.GetBytes('conectix').CopyTo($footer, 0)          # cookie
    & $put 8  2 4                                           # features (reserved bit)
    & $put 12 0x00010000 4                                  # file format version 1.0
    & $put 16 ([UInt64]::MaxValue) 8                        # data offset (fixed disk)
    $ts = [UInt64][math]::Floor(((Get-Date).ToUniversalTime() - [datetime]'2000-01-01T00:00:00').TotalSeconds)
    & $put 24 $ts 4                                         # timestamp
    $ascii.GetBytes('win ').CopyTo($footer, 28)             # creator application
    & $put 32 0x000A0000 4                                  # creator version
    $ascii.GetBytes('Wi2k').CopyTo($footer, 36)             # creator host OS
    & $put 40 ([UInt64]$len) 8                              # original size
    & $put 48 ([UInt64]$len) 8                              # current size
    & $put 56 ([UInt64]$cyl) 2                              # cylinders
    $footer[58] = [byte]$heads
    $footer[59] = [byte]$spt
    & $put 60 2 4                                           # disk type: fixed
    [guid]::NewGuid().ToByteArray().CopyTo($footer, 68)     # unique id

    $sum = [UInt64]0
    foreach ($b in $footer) { $sum += $b }
    & $put 64 ([UInt64]4294967295 - $sum) 4                 # checksum = one's complement of byte sum

    $fs = [System.IO.File]::Open($Path, 'Append', 'Write')
    try { $fs.Write($footer, 0, 512) } finally { $fs.Close() }
}

# Builds an ISO9660+Joliet image with the built-in Windows IMAPI2 COM API (no extra tools).
function New-SeedIso([string]$SourceDir, [string]$IsoPath) {
    if (-not ('VirtoMir.IsoWriter' -as [type])) {
        Add-Type -TypeDefinition @'
namespace VirtoMir {
    public static class IsoWriter {
        public static void Write(string path, object stream, int blockSize, int totalBlocks) {
            var s = (System.Runtime.InteropServices.ComTypes.IStream)stream;
            byte[] buf = new byte[blockSize];
            using (var fs = System.IO.File.Create(path)) {
                for (int i = 0; i < totalBlocks; i++) {
                    s.Read(buf, blockSize, System.IntPtr.Zero);
                    fs.Write(buf, 0, blockSize);
                }
            }
        }
    }
}
'@
    }
    $img = New-Object -ComObject IMAPI2FS.MsftFileSystemImage
    $img.ChooseImageDefaultsForMediaType(13)
    $img.FileSystemsToCreate = 3          # ISO9660 + Joliet
    $img.VolumeName = 'cidata'
    $img.Root.AddTree($SourceDir, $false)
    $res = $img.CreateResultImage()
    [VirtoMir.IsoWriter]::Write($IsoPath, $res.ImageStream, $res.BlockSize, $res.TotalBlocks)
}

# =============================================================================
# Stage 2 (elevated): Hyper-V operations
# =============================================================================
function Invoke-HyperVStage($S) {
    Import-Module Hyper-V

    if (-not (Get-VMSwitch -Name $S.SwitchName -ErrorAction SilentlyContinue)) {
        throw "Hyper-V switch '$($S.SwitchName)' not found. Available: $((Get-VMSwitch).Name -join ', ')"
    }

    $existing = Get-VM -Name $S.VMName -ErrorAction SilentlyContinue
    if ($existing) {
        if (-not $S.Force) { throw "VM '$($S.VMName)' already exists. Re-run with -Force to recreate it." }
        Write-Step "Removing existing VM '$($S.VMName)'"
        if ($existing.State -ne 'Off') { Stop-VM -VM $existing -TurnOff -Force }
        $oldDisks = @(Get-VMHardDiskDrive -VM $existing | ForEach-Object Path)
        Remove-VM -VM $existing -Force
        foreach ($d in $oldDisks) { Remove-Item $d -Force -ErrorAction SilentlyContinue }
    }

    $vmDir = Join-Path (Get-VMHost).VirtualHardDiskPath $S.VMName
    New-Item -ItemType Directory -Force -Path $vmDir | Out-Null
    $vhdx = Join-Path $vmDir "$($S.VMName).vhdx"
    $iso  = Join-Path $vmDir "$($S.VMName)-seed.iso"
    Remove-Item $vhdx, $iso -Force -ErrorAction SilentlyContinue
    Copy-Item $S.SeedIso $iso -Force

    Write-Step "Converting disk to dynamic VHDX ($($S.DiskGB) GB)"
    Convert-VHD -Path $S.FixedVhd -DestinationPath $vhdx -VHDType Dynamic
    Resize-VHD -Path $vhdx -SizeBytes ([UInt64]$S.DiskGB * 1GB)

    Write-Step "Creating VM ($($S.CpuCount) vCPU, $($S.MemoryGB) GB RAM, nested virtualization ON)"
    $vm = New-VM -Name $S.VMName -Generation 1 -MemoryStartupBytes ([Int64]$S.MemoryGB * 1GB) -VHDPath $vhdx -SwitchName $S.SwitchName
    Set-VM -VM $vm -ProcessorCount $S.CpuCount -StaticMemory -AutomaticCheckpointsEnabled $false `
           -AutomaticStopAction ShutDown -Notes 'VirtoMir dev host (Debian 13 + KVM/libvirt). Created by dev/hyperv/New-VirtoMirDevHost.ps1'
    Set-VMProcessor -VM $vm -ExposeVirtualizationExtensions $true
    Get-VMNetworkAdapter -VM $vm | Set-VMNetworkAdapter -MacAddressSpoofing On
    $dvd = Get-VMDvdDrive -VM $vm
    if ($dvd) { Set-VMDvdDrive -VMDvdDrive $dvd -Path $iso }
    else      { Add-VMDvdDrive -VM $vm -ControllerNumber 1 -ControllerLocation 0 -Path $iso }
    Set-VMBios -VM $vm -StartupOrder @('IDE', 'CD', 'LegacyNetworkAdapter', 'Floppy')
    Get-VMIntegrationService -VM $vm | Where-Object { -not $_.Enabled } | Enable-VMIntegrationService

    # Convenience: allow managing Hyper-V without UAC in the future (effective after next sign-in).
    try {
        $grp = Get-LocalGroup -SID 'S-1-5-32-578'
        $isMember = $false
        try { $isMember = [bool](Get-LocalGroupMember -Group $grp -ErrorAction Stop | Where-Object { $_.Name -eq $S.User }) } catch { }
        if (-not $isMember) { Add-LocalGroupMember -Group $grp -Member $S.User -ErrorAction Stop; Write-Info "Added $($S.User) to 'Hyper-V Administrators' (effective after next sign-in)." }
    } catch { Write-Info "Could not add user to Hyper-V Administrators: $($_.Exception.Message)" }

    Write-Step 'Starting VM and waiting for its IP address (first boot installs packages; ~3-10 min)'
    Start-VM -VM $vm
    $deadline = (Get-Date).AddMinutes(25)
    $ip = $null
    while ((Get-Date) -lt $deadline -and -not $ip) {
        Start-Sleep -Seconds 5
        $na = Get-VMNetworkAdapter -VM $vm
        $ip = $na.IPAddresses | Where-Object { $_ -match '^\d{1,3}(\.\d{1,3}){3}$' } | Select-Object -First 1
        if (-not $ip -and $na.MacAddress -and $na.MacAddress -ne '000000000000') {
            $mac = $na.MacAddress -replace '(..)(?!$)', '$1-'
            $nb = Get-NetNeighbor -AddressFamily IPv4 -LinkLayerAddress $mac -ErrorAction SilentlyContinue |
                  Where-Object { $_.State -ne 'Unreachable' } | Select-Object -First 1
            if ($nb) { $ip = $nb.IPAddress }
        }
    }
    if (-not $ip) { throw 'Timed out waiting for the VM to report an IP address. Open Hyper-V Manager and check the VM console.' }
    Write-Info "VM IP: $ip"
    return $ip
}

if ($Stage -eq 'hyperv') {
    $S = Get-Content $StateFile -Raw | ConvertFrom-Json
    $script:LogFile = $S.LogFile
    $result = @{ ok = $false; ip = $null; error = $null }
    try {
        $result.ip = Invoke-HyperVStage $S
        $result.ok = $true
    } catch {
        $result.error = "$($_.Exception.Message)`n$($_.ScriptStackTrace)"
        Write-Step "ERROR: $($_.Exception.Message)"
    } finally {
        $result | ConvertTo-Json | Set-Content -Path $S.ResultFile -Encoding UTF8
    }
    return
}

# =============================================================================
# Stage 1 (user): prepare everything that doesn't need admin
# =============================================================================
$repoRoot   = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$cacheDir   = Join-Path $env:LOCALAPPDATA 'VirtoMir\devhost'
$workDir    = Join-Path $cacheDir $VMName
$sshDir     = Join-Path $env:USERPROFILE '.ssh'
$keyPath    = Join-Path $sshDir 'virtomir_dev_ed25519'
$knownHosts = Join-Path $sshDir 'known_hosts_virtomir'
$sshConfig  = Join-Path $sshDir 'config'
New-Item -ItemType Directory -Force -Path $cacheDir, $sshDir | Out-Null

# --- Password ---------------------------------------------------------------
if (-not $Password) {
    $envFile = Join-Path $repoRoot '.ai\local\devhost.env'
    if (Test-Path $envFile) {
        $line = Get-Content $envFile | Where-Object { $_ -match '^\s*VM_PASSWORD\s*=' } | Select-Object -First 1
        if ($line) { $Password = ($line -split '=', 2)[1].Trim() }
    }
}
if (-not $Password) {
    $sec = Read-Host "Password for '$UserName' on the VM" -AsSecureString
    $Password = [Runtime.InteropServices.Marshal]::PtrToStringAuto([Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec))
}

# --- SSH key ----------------------------------------------------------------
if (-not (Test-Path $keyPath)) {
    Write-Step "Creating SSH key $keyPath"
    $r = Invoke-Native 'ssh-keygen' @('-q', '-t', 'ed25519', '-N', '""', '-C', "virtomir-dev@$env:COMPUTERNAME", '-f', $keyPath) -Quiet
    if ($r.Code -ne 0) { throw "ssh-keygen failed: $($r.Output)" }
}
$pubKey = (Get-Content "$keyPath.pub" -Raw).Trim()

# --- Download + verify image ------------------------------------------------
$tarPath  = Join-Path $cacheDir $ImageName
$sumsPath = Join-Path $cacheDir 'SHA512SUMS'
Write-Step "Fetching checksums for $ImageName"
$r = Invoke-Native 'curl.exe' @('-sSL', '--fail', '-o', $sumsPath, "$ImageBaseUrl/SHA512SUMS") -Quiet
if ($r.Code -ne 0) { throw "Could not download SHA512SUMS: $($r.Output)" }
$expected = (Get-Content $sumsPath | Where-Object { $_ -match "\s\*?$([regex]::Escape($ImageName))$" } | Select-Object -First 1) -split '\s+' | Select-Object -First 1
if (-not $expected) { throw "$ImageName not listed in SHA512SUMS" }

$needDownload = $true
if (Test-Path $tarPath) {
    Write-Info 'Cached image found, verifying...'
    $needDownload = ((Get-FileHash $tarPath -Algorithm SHA512).Hash -ne $expected.ToUpper())
}
if ($needDownload) {
    Write-Step "Downloading $ImageName"
    $r = Invoke-Native 'curl.exe' @('-L', '--fail', '--progress-bar', '-o', "$tarPath.part", "$ImageBaseUrl/$ImageName")
    if ($r.Code -ne 0) { throw 'Image download failed.' }
    Move-Item "$tarPath.part" $tarPath -Force
    if ((Get-FileHash $tarPath -Algorithm SHA512).Hash -ne $expected.ToUpper()) { throw 'SHA512 mismatch on downloaded image!' }
}
Write-Info 'Image verified (SHA512 OK).'

# --- Raw -> fixed VHD -------------------------------------------------------
Write-Step 'Extracting disk image'
if (Test-Path $workDir) { Remove-Item $workDir -Recurse -Force }
New-Item -ItemType Directory -Force -Path $workDir | Out-Null
$r = Invoke-Native 'tar.exe' @('-xf', $tarPath, '-C', $workDir) -Quiet
if ($r.Code -ne 0) { throw "tar extraction failed: $($r.Output)" }
$raw = Get-ChildItem $workDir -Filter '*.raw' -Recurse | Select-Object -First 1
if (-not $raw) { throw 'No .raw disk found inside the image archive.' }
$fixedVhd = Join-Path $workDir "$VMName-base.vhd"
Move-Item $raw.FullName $fixedVhd
# bsdtar extracts disk.raw as an NTFS sparse file; Hyper-V refuses sparse/compressed VHDs (0xC03A001A).
$r = Invoke-Native 'fsutil.exe' @('sparse', 'setflag', $fixedVhd, '0') -Quiet
if ($r.Code -ne 0) { throw "Could not clear the sparse flag on ${fixedVhd}: $($r.Output)" }
if ((Get-Item $fixedVhd).Attributes -band [IO.FileAttributes]::Compressed) {
    $r = Invoke-Native 'compact.exe' @('/u', '/q', $fixedVhd) -Quiet
    if ($r.Code -ne 0) { throw "Could not decompress ${fixedVhd}: $($r.Output)" }
}
Write-Step 'Converting raw image to VHD'
ConvertTo-FixedVhd $fixedVhd

# --- cloud-init seed ISO ----------------------------------------------------
Write-Step 'Building cloud-init seed ISO'
$seedDir = Join-Path $workDir 'seed'
New-Item -ItemType Directory -Force -Path $seedDir | Out-Null
$template = Get-Content (Join-Path $PSScriptRoot 'cloud-init\user-data.yaml') -Raw -Encoding UTF8
$userData = $template.Replace('${HOSTNAME}', $VMName)
$userData = $userData.Replace('${USERNAME}', $UserName)
$userData = $userData.Replace('${SSH_PUBKEY}', $pubKey)
$userData = $userData.Replace('${PASSWORD}', $Password.Replace("'", "''"))
Write-Utf8NoBomLf (Join-Path $seedDir 'user-data') $userData
Write-Utf8NoBomLf (Join-Path $seedDir 'meta-data') ("instance-id: {0}-{1}`nlocal-hostname: {0}`n" -f $VMName, (Get-Date -Format 'yyyyMMddHHmmss'))
$seedIso = Join-Path $workDir "$VMName-seed.iso"
New-SeedIso $seedDir $seedIso
Remove-Item $seedDir -Recurse -Force     # rendered user-data contains the password

# --- Elevated Hyper-V stage -------------------------------------------------
$stateFile  = Join-Path $workDir 'state.json'
$resultFile = Join-Path $workDir 'result.json'
$logFile    = Join-Path $workDir 'hyperv.log'
Remove-Item $resultFile, $logFile -Force -ErrorAction SilentlyContinue
@{
    VMName = $VMName; CpuCount = $CpuCount; MemoryGB = $MemoryGB; DiskGB = $DiskGB
    SwitchName = $SwitchName; Force = [bool]$Force; User = "$env:USERDOMAIN\$env:USERNAME"
    FixedVhd = $fixedVhd; SeedIso = $seedIso; ResultFile = $resultFile; LogFile = $logFile
} | ConvertTo-Json | Set-Content -Path $stateFile -Encoding UTF8

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if ($isAdmin) {
    & $PSCommandPath -Stage hyperv -StateFile $stateFile
} else {
    Write-Step 'Requesting administrator rights for Hyper-V (please click YES on the UAC prompt)'
    $argList = "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$PSCommandPath`" -Stage hyperv -StateFile `"$stateFile`""
    try { $proc = Start-Process powershell.exe -Verb RunAs -ArgumentList $argList -WindowStyle Hidden -PassThru }
    catch { throw 'UAC prompt was cancelled - administrator rights are required to create the Hyper-V VM.' }

    $shown = 0
    $deadline = (Get-Date).AddMinutes(45)
    while (-not (Test-Path $resultFile)) {
        Start-Sleep -Seconds 2
        if (Test-Path $logFile) {
            try {
                $lines = @(Get-Content $logFile -Encoding UTF8)
                for ($i = $shown; $i -lt $lines.Count; $i++) { Write-Host "  [admin] $($lines[$i])" -ForegroundColor DarkCyan }
                $shown = $lines.Count
            } catch { }
        }
        if ((Get-Date) -gt $deadline) { throw 'Timed out waiting for the elevated Hyper-V stage.' }
        $exited = $false
        try { $exited = $proc.HasExited } catch { }
        if ($exited -and -not (Test-Path $resultFile)) { Start-Sleep 2; if (-not (Test-Path $resultFile)) { throw "Elevated stage exited without a result. See $logFile" } }
    }
}

$result = Get-Content $resultFile -Raw | ConvertFrom-Json
if (-not $result.ok) { throw "Hyper-V stage failed: $($result.error)" }
$ip = $result.ip
Remove-Item $fixedVhd -Force -ErrorAction SilentlyContinue   # converted copy lives in the Hyper-V folder now

# --- SSH config -------------------------------------------------------------
Write-Step "Writing SSH config entry 'Host $VMName'"
Remove-Item $knownHosts -Force -ErrorAction SilentlyContinue   # fresh VM = fresh host keys
$begin = "# >>> $VMName (managed by VirtoMir New-VirtoMirDevHost.ps1) >>>"
$end   = "# <<< $VMName <<<"
$block = @"
$begin
Host $VMName
    HostName $ip
    User $UserName
    IdentityFile ~/.ssh/virtomir_dev_ed25519
    IdentitiesOnly yes
    UserKnownHostsFile ~/.ssh/known_hosts_virtomir
    StrictHostKeyChecking accept-new
    ServerAliveInterval 30
$end
"@
$existingCfg = if (Test-Path $sshConfig) { Get-Content $sshConfig -Raw } else { '' }
$pattern = "(?s)" + [regex]::Escape($begin) + ".*?" + [regex]::Escape($end) + "\r?\n?"
$existingCfg = [regex]::Replace($existingCfg, $pattern, '')
$newCfg = ($block + "`n" + $existingCfg.TrimStart()).TrimEnd() + "`n"
[System.IO.File]::WriteAllText($sshConfig, ($newCfg -replace "`r`n", "`n"), (New-Object System.Text.UTF8Encoding($false)))

# --- Wait for cloud-init ----------------------------------------------------
Write-Step 'Waiting for cloud-init to finish provisioning (packages, libvirt)...'
$deadline = (Get-Date).AddMinutes(25)
$ready = $false
while ((Get-Date) -lt $deadline) {
    $r = Invoke-Native 'ssh' @('-o', 'BatchMode=yes', '-o', 'ConnectTimeout=5', $VMName, 'test -f /var/lib/virtomir-devhost-ready && echo READY') -Quiet
    if ($r.Output -match 'READY') { $ready = $true; break }
    Start-Sleep -Seconds 10
}
if (-not $ready) { throw "VM is up at $ip but provisioning did not finish in time. Check: ssh $VMName 'sudo cloud-init status --long'" }

# --- Validation report -----------------------------------------------------
Write-Step 'Validating KVM / libvirt inside the VM'
$check = @'
echo "--- kernel:     $(uname -r)"
echo "--- /dev/kvm:   $(ls -l /dev/kvm 2>&1)"
echo "--- cpu flags:  $(grep -c -E '^flags\s*:.*\b(vmx|svm)\b' /proc/cpuinfo) vCPU(s) with vmx/svm"
echo "--- cloud-init: $(cloud-init status 2>/dev/null | tr '\n' ' ')"
echo "--- libvirt:    $(virsh -c qemu:///system version --daemon 2>&1 | tr '\n' ' ')"
echo "--- networks:"; virsh -c qemu:///system net-list --all
echo "--- virt-host-validate:"; cat /var/log/virtomir-host-validate.log
'@
# Windows PowerShell 5.1 mangles embedded quotes in native arguments -> ship the script base64-encoded.
$b64 = [Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes(($check -replace "`r`n", "`n")))
$r = Invoke-Native 'ssh' @('-o', 'BatchMode=yes', $VMName, "echo $b64 | base64 -d | bash") -Quiet
Write-Host $r.Output

Write-Host ''
Write-Host "VirtoMir dev host is ready." -ForegroundColor Green
Write-Host "  ssh $VMName            (IP $ip, user $UserName, key ~/.ssh/virtomir_dev_ed25519)" -ForegroundColor Green
Write-Host "  Hyper-V console:      vmconnect localhost $VMName" -ForegroundColor Green
