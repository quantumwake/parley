# PowerShell twin of scripts/parley for Windows hosts without Git Bash.
# Same resolution: cached build at this version, else build from vendored
# source with Go, else download the release asset with gh.
$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$Data = if ($env:CLAUDE_PLUGIN_DATA) { $env:CLAUDE_PLUGIN_DATA } else { Join-Path $HOME ".statefs-ai" }
$Bin = Join-Path $Data "bin\parley.exe"
$Ver = (Get-Content (Join-Path $Root "cmd\parley\VERSION")).Trim()
if (Test-Path $Bin) {
  $Have = ((& $Bin version 2>$null) -split ' ')[1]
  if ($Have -and ([version]$Have -ge [version]$Ver)) { & $Bin @args; exit $LASTEXITCODE }
}
New-Item -ItemType Directory -Force -Path (Split-Path $Bin) | Out-Null
if (Get-Command go -ErrorAction SilentlyContinue) {
  Push-Location $Root; try { $env:GOFLAGS = "-mod=vendor"; go build -o $Bin ./cmd/parley } finally { Pop-Location }
  & $Bin @args; exit $LASTEXITCODE
}
if (Get-Command gh -ErrorAction SilentlyContinue) {
  $Arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }
  gh release download "v$Ver" -R quantumwake/parley -p "parley_windows_$Arch.exe" -O $Bin --clobber
  & $Bin @args; exit $LASTEXITCODE
}
Write-Error "parley: no binary; install Go (https://go.dev) or gh (https://cli.github.com) and retry"
exit 4
