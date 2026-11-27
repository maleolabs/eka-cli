<#
install.ps1 — Install the EKA CLI (Windows)

Usage:
  irm https://github.com/maleolabs/eka-cli/releases/latest/download/install.ps1 | iex

  # Specific version or custom directory (Invoke-Expression takes no
  # script parameters — interpolate them into the script text):
  $s = irm https://github.com/maleolabs/eka-cli/releases/latest/download/install.ps1
  iex "$s -Version v0.2.0"
  iex "$s -To 'C:\tools\bin'"
  iex "$s -NoCompletion"

Downloads the latest (or specified) EKA release binary for the current OS
and architecture from the GitHub Release asset registry, verifies its
checksum against the release's SHA256SUMS.txt, and installs it to
$env:LOCALAPPDATA\Programs\eka (or a custom path via -To).

After the binary is installed, PowerShell shell completion is
registered in the current user's PowerShell profile:
  - interactive session: prompts Yes/No (default Yes);
  - non-interactive runs (CI, piped invocation): registers by default;
  - -Completion powershell forces registration, -NoCompletion skips.
Completion setup is best-effort and never fails the install.

Trust model: verification is FAIL-CLOSED. The release workflow always
publishes SHA256SUMS.txt alongside the binaries; a checksum file that
cannot be fetched — or a checksum mismatch — aborts the install. No
binary is ever installed unverified.

Supported platforms:
  - Windows (amd64, arm64)

Requires PowerShell 5.1+ (built into Windows) or PowerShell 7+.
The Invoke-RestMethod calls are restricted to https.
#>

[CmdletBinding()]
param(
    [string]$Version = "latest",
    [string]$To = "",
    [string]$Completion = "",
    [switch]$NoCompletion
)

$ErrorActionPreference = "Stop"

# ── Config ─────────────────────────────────────────────────────────
$Repo = "maleolabs/eka-cli"
$DefaultInstallDir = Join-Path $env:LOCALAPPDATA "Programs\eka"
if ($To -eq "") {
    $InstallDir = $DefaultInstallDir
} else {
    $InstallDir = $To
}

# ── Helpers ────────────────────────────────────────────────────────
function Write-Step {
    param([string]$Name, [string]$Detail = "")
    if ($Detail) {
        Write-Host ("  [OK] {0,-40} ({1})" -f $Name, $Detail)
    } else {
        Write-Host ("  [OK] {0,-40}" -f $Name)
    }
}

function Stop-Step {
    param([string]$Message)
    Write-Host ("  [FAIL] " + $Message) -ForegroundColor Red
    exit 1
}

# ── Header ─────────────────────────────────────────────────────────
Write-Host ""
Write-Host "EKA CLI Installer"
Write-Host "─────────────────"
Write-Host ""

# ── Detect platform ────────────────────────────────────────────────
$OS = "windows"
$Arch = $env:PROCESSOR_ARCHITECTURE
switch -Regex ($Arch) {
    "AMD64" { $Arch = "amd64" }
    "ARM64" { $Arch = "arm64" }
    default { Stop-Step "Unsupported architecture '$Arch'. Only amd64 and arm64 are supported." }
}

Write-Step ("Platform: {0}/{1}" -f $OS, $Arch)

$Binary = "eka-$OS-$Arch.exe"

# ── Resolve download URL ───────────────────────────────────────────
if ($Version -eq "latest") {
    $BaseUrl = "https://github.com/$Repo/releases/latest/download"
} else {
    $BaseUrl = "https://github.com/$Repo/releases/download/$Version"
}

$DownloadUrl = "$BaseUrl/$Binary"
$ChecksumUrl = "$BaseUrl/SHA256SUMS.txt"

# ── Create temp directory ──────────────────────────────────────────
$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("eka-install-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $TmpDir -Force | Out-Null

try {
    # ── Download binary ────────────────────────────────────────────
    try {
        Invoke-WebRequest -Uri $DownloadUrl -OutFile (Join-Path $TmpDir $Binary) -UseBasicParsing
    } catch {
        Stop-Step "Download failed: $DownloadUrl"
    }
    Write-Step "Download $Binary"

    # ── Verify checksum (fail-closed) ──────────────────────────────
    try {
        Invoke-WebRequest -Uri $ChecksumUrl -OutFile (Join-Path $TmpDir "SHA256SUMS.txt") -UseBasicParsing
    } catch {
        Stop-Step "Checksum file not available - refusing to install $Binary unverified (fail-closed). URL: $ChecksumUrl"
    }

    # Extract expected hash. The file may contain "binaries/eka-windows-amd64.exe"
    # or just "eka-windows-amd64.exe".
    $ExpectedHash = $null
    foreach ($Line in Get-Content (Join-Path $TmpDir "SHA256SUMS.txt")) {
        if ($Line -match [regex]::Escape($Binary)) {
            $ExpectedHash = ($Line -split "\s+")[0]
            break
        }
    }
    if (-not $ExpectedHash) {
        Stop-Step "No checksum entry for $Binary in SHA256SUMS.txt - refusing to install (fail-closed)."
    }

    $ActualHash = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $TmpDir $Binary)).Hash.ToLowerInvariant()
    if ($ExpectedHash.ToLowerInvariant() -ne $ActualHash) {
        Stop-Step "Checksum mismatch - downloaded binary may be corrupted or tampered. Expected: $ExpectedHash Actual: $ActualHash"
    }
    Write-Step "Verify checksum"

    # ── Install ────────────────────────────────────────────────────
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    Move-Item -Path (Join-Path $TmpDir $Binary) -Destination (Join-Path $InstallDir "eka.exe") -Force
    Write-Step "Install to $(Join-Path $InstallDir 'eka.exe')"

    # ── Shell completion (best-effort, never fails the install) ────
    $ekaExe = Join-Path $InstallDir "eka.exe"
    if ($NoCompletion) {
        $CompletionShell = "none"
    } elseif ($Completion) {
        if ($Completion -ne "powershell") {
            Stop-Step "-Completion only supports 'powershell' on Windows (got '$Completion')"
        }
        $CompletionShell = $Completion
    } elseif ([Environment]::UserInteractive -and -not $env:CI) {
        # Interactive console: ask. Default is Yes (Enter).
        Write-Host ""
        Write-Host "  Install shell completion for PowerShell? (Tab completion for eka)"
        $ans = Read-Host "  [1] Yes (recommended)  [2] No  (Enter = 1)"
        if ($ans -eq "2") { $CompletionShell = "none" } else { $CompletionShell = "powershell" }
    } else {
        # Non-interactive (CI, piped invocation): register by default.
        $CompletionShell = "powershell"
    }

    if ($CompletionShell -eq "powershell") {
        $ProfilePath = $PROFILE.CurrentUserAllHosts
        $ProfileDir = Split-Path -Parent $ProfilePath
        if (-not (Test-Path $ProfileDir)) {
            New-Item -ItemType Directory -Path $ProfileDir -Force | Out-Null
        }
        if (-not (Test-Path $ProfilePath)) {
            New-Item -ItemType File -Path $ProfilePath -Force | Out-Null
        }
        $CompletionLine = "Invoke-Expression (& '$ekaExe' completion powershell | Out-String)"
        if (Select-String -Path $ProfilePath -SimpleMatch "eka.exe" -Quiet) {
            Write-Step "PowerShell completion already registered in $ProfilePath"
        } else {
            Add-Content -Path $ProfilePath -Value ("`n# eka shell completion`n" + $CompletionLine)
            Write-Step "PowerShell completion registered in $ProfilePath"
        }
    } else {
        Write-Step "Shell completion skipped"
    }

    # ── Summary ────────────────────────────────────────────────────
    Write-Host ""
    Write-Host "─────────────────"
    Write-Host "EKA CLI installed successfully!"
    Write-Host ""
    Write-Host "  Binary: $(Join-Path $InstallDir 'eka.exe')"
    Write-Host ""
    # %PATH% stays literal: setx expands it at the cmd prompt, which
    # preserves the existing PATH when the user runs the command.
    Write-Host ("  Add it to your PATH to use 'eka' from any terminal:")
    Write-Host ("  setx PATH `"{0};%PATH%`"" -f $InstallDir)
    Write-Host ""
    Write-Host "Run 'eka --help' to get started."
    Write-Host "Run 'eka init <name>' to create a new EKA repository."
}
finally {
    Remove-Item -Path $TmpDir -Recurse -Force -ErrorAction SilentlyContinue
}
