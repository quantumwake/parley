# statefs.ai parley installer for Windows (PowerShell).
#   irm https://raw.githubusercontent.com/quantumwake/parley/main/install.ps1 | iex
# Private repo: set $env:GITHUB_TOKEN first.
$ErrorActionPreference = "Stop"
$Repo = "quantumwake/parley"
$Arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }
$Headers = @{}; if ($env:GITHUB_TOKEN) { $Headers["Authorization"] = "token $env:GITHUB_TOKEN" }
$Ver = if ($env:PARLEY_VERSION) { $env:PARLEY_VERSION } else { (Invoke-RestMethod -Headers $Headers "https://api.github.com/repos/$Repo/releases/latest").tag_name }
$Rel = Invoke-RestMethod -Headers $Headers "https://api.github.com/repos/$Repo/releases/tags/$Ver"
$Asset = $Rel.assets | Where-Object { $_.name -eq "parley_windows_$Arch.exe" }
if (-not $Asset) { throw "no asset for windows/$Arch in $Ver" }
$Dir = if ($env:PARLEY_DIR) { $env:PARLEY_DIR } else { Join-Path $HOME ".statefs-ai\bin" }
New-Item -ItemType Directory -Force -Path $Dir | Out-Null
$Headers["Accept"] = "application/octet-stream"
Invoke-WebRequest -Headers $Headers $Asset.url -OutFile (Join-Path $Dir "parley.exe")
Write-Host "installed parley $Ver -> $Dir\parley.exe"
if (-not ($env:PATH -split ";" | Where-Object { $_ -eq $Dir })) { Write-Host "add to PATH: $Dir" }
if (Get-Command claude -ErrorAction SilentlyContinue) { claude plugin marketplace add $Repo | Out-Null; claude plugin install parley@parley --scope user | Out-Null; Write-Host "Claude Code plugin parley@parley installed" }
Write-Host "next: parley enroll '<enrollment url from your tenant admin>'"
