# build.ps1 — Builds the FeatherDeploy Linux binary from Windows using WSL2.
# Requires WSL2 with build dependencies installed (handled by build.sh).
# The resulting binary is placed in dist\featherdeploy (Linux ELF executable).

$ErrorActionPreference = "Stop"

Write-Host "==> Running build via WSL2..." -ForegroundColor Cyan
wsl bash ./build.sh

if ($LASTEXITCODE -ne 0) {
    Write-Error "Build failed (exit code $LASTEXITCODE)"
    exit $LASTEXITCODE
}

Write-Host ""
Write-Host "Done! Binary at: dist\featherdeploy" -ForegroundColor Green
