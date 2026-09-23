[CmdletBinding()]
param(
    [string]$LocalStackEndpoint = 'http://localhost:4566',
    [string]$ApiBaseURL = 'http://127.0.0.1:18080',
    [string]$BackendContainerName = 'eventiapp-local-backend-1',
    [switch]$Doctor,
    [switch]$KeepAlive,
    [string]$SandboxAttestationPath,
    [ValidateRange(1, 300)]
    [int]$RestartDelaySeconds = 5
)

$ErrorActionPreference = 'Stop'
$backendRoot = Split-Path -Parent $PSScriptRoot
$environmentPath = Join-Path $backendRoot '.env.ai.local'
if (-not (Test-Path -LiteralPath $environmentPath -PathType Leaf)) {
    throw "Local agent environment file was not found: $environmentPath"
}

$workspaceLine = Get-Content -LiteralPath $environmentPath |
    Where-Object { $_ -match '^\s*ITBEM_AI_WORKSPACES_JSON\s*=' } |
    Select-Object -Last 1
if ([string]::IsNullOrWhiteSpace($workspaceLine)) {
    throw 'ITBEM_AI_WORKSPACES_JSON is required for the Docker sandbox launcher.'
}
$workspaceJSON = ($workspaceLine -replace '^\s*ITBEM_AI_WORKSPACES_JSON\s*=\s*', '').Trim().Trim('"').Trim("'")
$workspaces = $workspaceJSON | ConvertFrom-Json -ErrorAction Stop

# Keep the worker's derived sandbox registry authoritative while still
# preventing stale User/Process provider credentials from winning over the
# local file. Only the provider settings are copied; secrets never get logged.
$localAgentValues = @{}
Get-Content -LiteralPath $environmentPath | ForEach-Object {
    if ($_ -match '^\s*(ITBEM_AI_PROVIDER|MINIMAX_MODEL|MINIMAX_API_KEY|OPENAI_API_KEY|ANTHROPIC_API_KEY)\s*=\s*(.*)$') {
        $value = $matches[2].Trim().Trim('"').Trim("'")
        $localAgentValues[$matches[1]] = $value
    }
}
foreach ($name in $localAgentValues.Keys) {
    [Environment]::SetEnvironmentVariable($name, $localAgentValues[$name], 'Process')
}

if (-not [string]::IsNullOrWhiteSpace($SandboxAttestationPath)) {
    if (-not (Test-Path -LiteralPath $SandboxAttestationPath -PathType Leaf)) {
        throw "Sandbox attestation file was not found: $SandboxAttestationPath"
    }
    $attestationRaw = Get-Content -LiteralPath $SandboxAttestationPath -Raw
    if ($attestationRaw.Length -gt 2048) {
        throw 'Sandbox attestation file exceeds the bounded heartbeat input size.'
    }
    $attestation = $attestationRaw | ConvertFrom-Json -ErrorAction Stop
    if ($attestation.runtime -ne 'firecracker' -or $attestation.transport -ne 'virtio_vsock' -or [string]::IsNullOrWhiteSpace([string]$attestation.evidence_scope) -or $attestation.guest_command_verified -ne $true) {
        throw 'Sandbox attestation must prove Firecracker + virtio-vsock + an explicit evidence scope + a guest command.'
    }
    # Preserve the exact bounded JSON; the worker normalizes it again before
    # serializing the heartbeat. This opt-in path never grants execution rights.
    $env:ITBEM_AI_SANDBOX_ATTESTATION_JSON = ($attestation | ConvertTo-Json -Compress)
} else {
    # Never inherit an attestation from a previous launcher invocation.
    Remove-Item Env:ITBEM_AI_SANDBOX_ATTESTATION_JSON -ErrorAction SilentlyContinue
}

# These immutable local images are already present on this machine and are
# digest-pinned. A missing image fails closed in the worker doctor; the wrapper
# never pulls or builds an image implicitly.
$profiles = @{
    'itbem-events-backend' = @{ image = 'golang@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437'; digest = 'sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437' }
    'eventiapp-dashboard' = @{ image = 'node@sha256:8a34c4ab3ea2c5cd194f07e317b2a8f09461d3c8b05c4e34c8ccd56d56024c4d'; digest = 'sha256:8a34c4ab3ea2c5cd194f07e317b2a8f09461d3c8b05c4e34c8ccd56d56024c4d' }
}
foreach ($property in $workspaces.PSObject.Properties) {
    if (-not $profiles.ContainsKey($property.Name)) {
        throw "No digest-pinned local Docker profile is defined for workspace '$($property.Name)'."
    }
    $profile = $profiles[$property.Name]
    $property.Value | Add-Member -NotePropertyName sandbox_runtime -NotePropertyValue 'docker' -Force
    $property.Value | Add-Member -NotePropertyName sandbox_image -NotePropertyValue $profile.image -Force
    $property.Value | Add-Member -NotePropertyName sandbox_image_digest -NotePropertyValue $profile.digest -Force
    $property.Value | Add-Member -NotePropertyName sandbox_network -NotePropertyValue 'none' -Force
    $property.Value | Add-Member -NotePropertyName require_sandbox -NotePropertyValue $true -Force
}
$env:ITBEM_AI_WORKSPACES_JSON = $workspaces | ConvertTo-Json -Depth 20 -Compress

# The running local backend owns the callback secret. Resolve it in-process on
# every launch so a stale inherited value can never silently authenticate the
# wrong local backend. It never appears in a command line, log or generated
# configuration file.
$secret = (& docker exec $BackendContainerName sh -lc 'printf %s "$AUTOMATION_CALLBACK_SECRET"').Trim()
if (-not [string]::IsNullOrWhiteSpace($secret)) {
    $env:AUTOMATION_CALLBACK_SECRET = $secret
} elseif ([string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable('AUTOMATION_CALLBACK_SECRET', 'Process'))) {
    throw "Could not resolve the callback secret from local container '$BackendContainerName'."
}
$launcher = Join-Path $PSScriptRoot 'Start-LocalAIAgent.ps1'
# The local .env.ai.local file is the source of truth for provider credentials.
# Do not inherit a stale User/Process MINIMAX_API_KEY into the worker; the
# callback secret is resolved explicitly above from the running backend.
$arguments = @('-NoProfile', '-File', $launcher, '-LocalStackEndpoint', $LocalStackEndpoint, '-ApiBaseURL', $ApiBaseURL, '-PreferProcessEnvironment', '-RestartDelaySeconds', "$RestartDelaySeconds")
if ($Doctor) { $arguments += '-Doctor' }
if ($KeepAlive) { $arguments += '-KeepAlive' }
& pwsh @arguments
exit $LASTEXITCODE
