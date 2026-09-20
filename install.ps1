# statefs.ai parley installer for Windows (PowerShell).
#   irm https://raw.githubusercontent.com/quantumwake/parley/main/install.ps1 | iex
# Install and enroll in one step: $env:PARLEY_ENROLL_URL = '<enrollment url from app.statefs.ai>' first.
$ErrorActionPreference = "Stop"
$Repo = "quantumwake/parley"
$Arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }
$Asset = "parley_windows_$Arch.exe"
$Dir = if ($env:PARLEY_DIR) { $env:PARLEY_DIR } else { Join-Path $HOME ".statefs-ai\bin" }
New-Item -ItemType Directory -Force -Path $Dir | Out-Null
$Ver = $env:PARLEY_VERSION
if ($Ver) {
  Invoke-WebRequest "https://github.com/$Repo/releases/download/$Ver/$Asset" -OutFile (Join-Path $Dir "parley.exe")
} else {
  Invoke-WebRequest "https://github.com/$Repo/releases/latest/download/$Asset" -OutFile (Join-Path $Dir "parley.exe")
  $Ver = "latest"
}
$cache = Join-Path $HOME ".claude\plugins\data\parley-parley\bin"
if (Test-Path (Join-Path $HOME ".claude\plugins\data\parley-parley")) {
  New-Item -ItemType Directory -Force -Path $cache | Out-Null
  Copy-Item (Join-Path $Dir "parley.exe") (Join-Path $cache "parley.exe") -Force
}
Write-Host "installed parley $Ver -> $Dir\parley.exe"
if (-not ($env:PATH -split ";" | Where-Object { $_ -eq $Dir })) { Write-Host "add to PATH: $Dir" }
& (Join-Path $Dir "parley.exe") setup auto
if ($env:PARLEY_ENROLL_URL) {
  Write-Host "enrolling this machine..."
  & (Join-Path $Dir "parley.exe") enroll $env:PARLEY_ENROLL_URL
  Write-Host "done: new agent sessions on this machine are recorded (restart any that are open)"
} else {
  Write-Host "next: sign in at https://app.statefs.ai, add this machine, and run the command it shows (or: parley enroll '<enrollment url>')"
}
