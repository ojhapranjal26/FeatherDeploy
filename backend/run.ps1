# Run the DeployPaaS backend server
# Usage: .\run.ps1 [options]
#   -Dev   : use development defaults (auto-sets JWT_SECRET, DB_PATH, ADDR, ORIGIN)
#   -Build : build before running
#   -Help  : show help

param(
  [switch]$Dev,
  [switch]$Build,
  [switch]$Help
)

if ($Help) {
  Write-Host "Usage: .\run.ps1 [-Dev] [-Build] [-Help]"
  Write-Host "  -Dev   : run with development defaults"
  Write-Host "  -Build : rebuild binary before running"
  exit 0
}

# Ensure 64-bit GCC (MSYS2 MinGW64) is on the PATH for CGO
$mingwBin = "C:\msys64\mingw64\bin"
if ((Test-Path $mingwBin) -and ($env:PATH -notlike "*$mingwBin*")) {
  $env:PATH = "$mingwBin;" + $env:PATH
  Write-Host "Added $mingwBin to PATH for CGO (go-sqlite3)"
}

# Load .env if it exists
$envFile = Join-Path $PSScriptRoot ".env"
if (Test-Path $envFile) {
  Get-Content $envFile | ForEach-Object {
    $line = $_.Trim()
    if ($line -and -not $line.StartsWith('#') -and $line -match '^([^=]+)=(.*)$') {
      $key   = $Matches[1].Trim()
      $value = $Matches[2].Trim()
      if (-not [System.Environment]::GetEnvironmentVariable($key)) {
        [System.Environment]::SetEnvironmentVariable($key, $value, 'Process')
      }
    }
  }
}

# Dev defaults
if ($Dev) {
  if (-not $env:DB_PATH)     { $env:DB_PATH     = "./deploy-dev.db" }
  if (-not $env:JWT_SECRET)  { $env:JWT_SECRET  = "dev-secret-do-not-use-in-production" }
  if (-not $env:ADDR)        { $env:ADDR        = ":8080" }
  if (-not $env:ORIGIN)      { $env:ORIGIN      = "http://localhost:5173,http://localhost:5174" }
  Write-Host "Running in DEV mode (DB: $env:DB_PATH, Addr: $env:ADDR)"
}

# Build
$binary = Join-Path $PSScriptRoot "server.exe"
if ($Build -or -not (Test-Path $binary)) {
  Write-Host "Building backend..."
  Push-Location $PSScriptRoot
  go build -o server.exe ./cmd/server/
  $buildResult = $LASTEXITCODE
  Pop-Location
  if ($buildResult -ne 0) {
    Write-Error "Build failed. Fix errors and retry."
    exit 1
  }
  Write-Host "Build successful."
}

# Run
Write-Host "Starting server on $env:ADDR ..."
& $binary
