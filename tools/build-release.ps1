<#
.SYNOPSIS
    Builds the Todnet release artifacts: the app exe, a portable zip, and the
    Inno Setup installer.

.DESCRIPTION
    Produces, in dist\:

        Todnet.exe                        the app itself (embedded UI)
        Todnet-<version>-windows-x64.zip  portable build for people who do
                                          not want an installer
        Todnet-<version>-setup.exe        Windows installer (Program Files,
                                          or per-user without UAC)
        SHA256SUMS.txt                    checksums for the two above

    Requirement: Inno Setup 6.3+ (https://jrsoftware.org/isdl.php). The script
    looks for ISCC.exe in the usual per-user and machine-wide locations, and
    on PATH.

.EXAMPLE
    .\tools\build-release.ps1
    .\tools\build-release.ps1 -Version 1.2.0
#>
[CmdletBinding()]
param(
    # Release version, without a leading "v". Defaults to the version in main.go.
    [string]$Version = "",

    # Path to the Go toolchain. Defaults to "go" on PATH, then the
    # conventional SDK location used on this project's build machines.
    [string]$Go = ""
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$dist = Join-Path $root "dist"
$iss = Join-Path $root "installer\Todnet.iss"

if (-not $Version) {
    $mainGo = Get-Content (Join-Path $root "main.go") -Raw
    $match = [regex]::Match($mainGo, 'var version = "([^"]+)"')
    if (-not $match.Success) {
        throw "Could not read the default version from main.go; pass -Version."
    }
    $Version = $match.Groups[1].Value
}
$Version = $Version.TrimStart("v")

# Inno Setup's version fields are numeric; refuse a tag like "1.2.3-beta"
# rather than silently shipping a file with no version metadata.
if ($Version -notmatch '^\d+(\.\d+){1,3}$') {
    throw "Version '$Version' is not numeric (expected 1.0.0 or 1.0.0.0)."
}

if (-not $Go) {
    $cmd = Get-Command go -ErrorAction SilentlyContinue
    if ($cmd) {
        $Go = $cmd.Source
    } elseif (Test-Path "C:\tmp\gosdk\go\bin\go.exe") {
        $Go = "C:\tmp\gosdk\go\bin\go.exe"
    } else {
        throw "Go 1.27+ is required but 'go' was not found. Install it from https://go.dev/dl/ or pass -Go <path>."
    }
}

$isccCandidates = @(
    (Join-Path $env:LOCALAPPDATA "Programs\Inno Setup 6\ISCC.exe"),
    "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
    "$env:ProgramFiles\Inno Setup 6\ISCC.exe"
)
$iscc = $isccCandidates | Where-Object { $_ -and (Test-Path $_) } | Select-Object -First 1
if (-not $iscc) {
    $onPath = Get-Command iscc -ErrorAction SilentlyContinue
    if ($onPath) { $iscc = $onPath.Source }
}
if (-not $iscc) {
    throw "ISCC.exe (Inno Setup 6.3+) was not found. Install it from https://jrsoftware.org/isdl.php"
}

New-Item -ItemType Directory -Force -Path $dist | Out-Null

Write-Host "Todnet $Version" -ForegroundColor Cyan
Write-Host "  go:   $Go"
Write-Host "  iscc: $iscc"

# --- the app -----------------------------------------------------------------
$exe = Join-Path $dist "Todnet.exe"
$env:CGO_ENABLED = "0"
& $Go build -trimpath -ldflags "-H windowsgui -s -w -X main.version=$Version" -o $exe .
if ($LASTEXITCODE -ne 0) { throw "go build failed." }
# Replacing an exe that is running leaves the previous one as "Todnet.exe~".
$backup = "$exe~"
if (Test-Path $backup) { Remove-Item $backup -Force -ErrorAction SilentlyContinue }

# --- portable zip ------------------------------------------------------------
# A running Todnet keeps its own image open, and Compress-Archive opens the
# source with a share mode that conflicts with that. Copy through a stream
# with FILE_SHARE_READ | FILE_SHARE_WRITE instead, so a release can be built
# while the app is still running.
$zip = Join-Path $dist "Todnet-$Version-windows-x64.zip"
if (Test-Path $zip) { Remove-Item $zip -Force }
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$source = [IO.File]::Open($exe, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::ReadWrite)
try {
    $archive = [IO.Compression.ZipFile]::Open($zip, [IO.Compression.ZipArchiveMode]::Create)
    try {
        $entry = $archive.CreateEntry("Todnet.exe", [IO.Compression.CompressionLevel]::Optimal)
        $target = $entry.Open()
        try { $source.CopyTo($target) } finally { $target.Dispose() }
    } finally {
        $archive.Dispose()
    }
} finally {
    $source.Dispose()
}

# --- installer ---------------------------------------------------------------
# A scanner (Defender, typically) sometimes holds the setup exe for a second
# after ISCC creates it and reports "EndUpdateResource failed". Retrying is
# reliable and keeps a release from failing on a transient lock.
$isccAttempts = 3
for ($attempt = 1; $attempt -le $isccAttempts; $attempt++) {
    & $iscc "/DAppVersion=$Version" $iss
    if ($LASTEXITCODE -eq 0) { break }
    if ($attempt -eq $isccAttempts) { throw "ISCC failed after $isccAttempts attempts." }
    Write-Warning "ISCC failed (attempt $attempt of $isccAttempts); retrying after a scanner lock."
    Start-Sleep -Seconds 3
}
$setup = Join-Path $dist "Todnet-$Version-setup.exe"
if (-not (Test-Path $setup)) { throw "Expected installer was not produced: $setup" }

# --- checksums ---------------------------------------------------------------
$sums = Join-Path $dist "SHA256SUMS.txt"
Get-FileHash $setup, $zip -Algorithm SHA256 |
    ForEach-Object { "$($_.Hash.ToLower())  $(Split-Path $_.Path -Leaf)" } |
    Set-Content $sums -Encoding ascii

Write-Host ""
Write-Host "Done:" -ForegroundColor Green
Get-ChildItem $setup, $zip, $sums | ForEach-Object {
    "{0,-45} {1,12:N0} bytes" -f $_.Name, $_.Length | Write-Host
}
