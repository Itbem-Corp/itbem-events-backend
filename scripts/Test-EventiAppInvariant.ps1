[CmdletBinding()]
param(
    [string]$PlatformPath = ''
)

$ErrorActionPreference = 'Stop'
$backendRoot = Split-Path $PSScriptRoot -Parent
if (-not $PlatformPath) {
    $PlatformPath = Join-Path (Split-Path $backendRoot -Parent) 'eventiapp-platform'
}

$resolvedPlatform = (Resolve-Path -LiteralPath $PlatformPath -ErrorAction Stop).Path
$gitDirectory = Join-Path $resolvedPlatform '.git'
if (-not (Test-Path -LiteralPath $gitDirectory)) {
    throw "EventiApp platform is not a Git checkout: $resolvedPlatform"
}

$statusLines = @(git -C $resolvedPlatform status --short)
if ($LASTEXITCODE -ne 0) {
    throw "Could not inspect EventiApp platform status: $resolvedPlatform"
}
if ($statusLines.Count -gt 0) {
    Write-Error "EventiApp platform changed; refusing to treat this run as isolated: $resolvedPlatform"
    $statusLines | ForEach-Object { Write-Error $_ }
    exit 2
}

Write-Host "EventiApp invariant: CLEAN ($resolvedPlatform)"
