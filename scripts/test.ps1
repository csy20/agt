# Native PowerShell development and test entry for agt.
# This file was not executed on the Linux host that added it.
# The Makefile demo uses /tmp, background processes, curl, and kill.
# That demo is not a Windows test path. Use this script instead.
[CmdletBinding()]
param(
    [ValidateSet("unit", "integration")]
    [string]$Mode = "unit"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

function Clear-AgtTestEnv {
    foreach ($name in @(
            "AGT_INTEGRATION",
            "AGT_DATABASE_URL",
            "AGT_IMAGE_PYTHON",
            "AGT_IMAGE_GO",
            "AGT_IMAGE_NODE",
            "AGT_SANDBOX_IMAGE"
        )) {
        Remove-Item "Env:$name" -ErrorAction SilentlyContinue
    }
}

$unitFile = Join-Path $root "scripts/unit-packages.txt"
$packages = @(
    Get-Content $unitFile |
        ForEach-Object { $_.Trim() } |
        Where-Object { $_ -and -not $_.StartsWith("#") }
)
if ($packages.Count -eq 0) {
    Write-Error "scripts/unit-packages.txt has no packages"
    exit 1
}

if ($Mode -eq "unit") {
    Clear-AgtTestEnv
    & go test @packages
    exit $LASTEXITCODE
}

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Error "integration mode requires docker; refusing to skip"
    exit 1
}

& docker compose -f deploy/compose.yaml up -d --wait
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& docker build -t agt-sandbox:python -f deploy/python.Dockerfile deploy
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& docker build -t agt-sandbox:go -f deploy/go.Dockerfile deploy
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& docker build -t agt-sandbox:node -f deploy/node.Dockerfile deploy
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$env:AGT_INTEGRATION = "1"
$env:AGT_DATABASE_URL = "postgres://agt:agt@127.0.0.1:54329/agt?sslmode=disable"
$env:AGT_IMAGE_PYTHON = "agt-sandbox:python"
$env:AGT_IMAGE_GO = "agt-sandbox:go"
$env:AGT_IMAGE_NODE = "agt-sandbox:node"
$env:AGT_SANDBOX_IMAGE = "agt-sandbox:python"
$env:AGT_LISTEN = "127.0.0.1:8787"
& go test -timeout 20m ./...
exit $LASTEXITCODE
