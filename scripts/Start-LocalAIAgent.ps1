[CmdletBinding()]
param(
    [string]$LocalStackEndpoint = 'http://localhost:4566',
    [string]$ApiBaseURL = 'http://localhost:8081',
    # Empty means: derive the region from the local queue URL. This keeps the
    # isolated worker aligned with the control plane even when Cognito/local
    # infrastructure was bootstrapped in a region other than us-east-1.
    [string]$AWSRegion = '',
    # This script is for local development, where .env.ai.local is the
    # deliberate source of truth. Opt in only when an explicit caller needs
    # process environment variables to take precedence.
    [switch]$PreferProcessEnvironment,
    [switch]$ProviderSmoke,
    [switch]$Doctor,
    [switch]$SyncWorkspaces,
    # Service mode is intentionally opt-in: it keeps the local worker online
    # after an unexpected process failure, while normal development runs still
    # return their exit code directly to the terminal.
    [switch]$KeepAlive,
    [ValidateRange(1, 300)]
    [int]$RestartDelaySeconds = 5
)

$ErrorActionPreference = 'Stop'
$endpoint = [Uri]$LocalStackEndpoint
if ($endpoint.Scheme -ne 'http' -or $endpoint.Host -notin @('localhost', '127.0.0.1', '::1')) {
    throw 'LocalStackEndpoint must be an HTTP loopback endpoint.'
}

$backendRoot = Split-Path -Parent $PSScriptRoot

function Resolve-LocalAWSRegion([string]$RequestedRegion, [string]$BackendRoot) {
    if (-not [string]::IsNullOrWhiteSpace($RequestedRegion)) {
        return $RequestedRegion.Trim()
    }
    $dashboardEnvironment = Join-Path (Split-Path -Parent $BackendRoot) 'dashboard-ts/.env.local'
    if (Test-Path -LiteralPath $dashboardEnvironment -PathType Leaf) {
        $regionLine = Get-Content -LiteralPath $dashboardEnvironment | Where-Object { $_ -match '^\s*COGNITO_REGION\s*=\s*(.+?)\s*$' } | Select-Object -First 1
        if ($regionLine -match '^\s*COGNITO_REGION\s*=\s*(.+?)\s*$') {
            $region = $matches[1].Trim().Trim('"').Trim("'")
            if ($region -match '^[a-z]{2}-[a-z]+-\d+$') { return $region }
        }
    }
    return 'us-east-1'
}

function Import-LocalAgentEnvironment([string]$Path, [bool]$PreferProcessEnvironment) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return }
    Get-Content -LiteralPath $Path | ForEach-Object {
        $line = $_.Trim()
        if (-not $line -or $line.StartsWith('#')) { return }
        if ($line -notmatch '^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$') {
            throw "Invalid local agent environment entry in $Path"
        }
        $name = $matches[1]
        $value = $matches[2].Trim()
        if ($value.Length -ge 2 -and (($value.StartsWith('"') -and $value.EndsWith('"')) -or ($value.StartsWith("'") -and $value.EndsWith("'")))) {
            $value = $value.Substring(1, $value.Length - 2)
        }
        # Local development must not accidentally inherit a stale User-level
        # secret. Deployment does not invoke this script and reads its explicit
        # environment directly; callers who need that behavior locally can opt
        # in with -PreferProcessEnvironment.
        if (-not $PreferProcessEnvironment -or -not [Environment]::GetEnvironmentVariable($name, 'Process')) {
            [Environment]::SetEnvironmentVariable($name, $value, 'Process')
        }
    }
}

function Test-StagehandConfigured {
    $raw = [Environment]::GetEnvironmentVariable('ITBEM_AI_WORKSPACES_JSON', 'Process')
    if ([string]::IsNullOrWhiteSpace($raw)) { return $false }
    try {
        $workspaces = $raw | ConvertFrom-Json -ErrorAction Stop
        foreach ($property in $workspaces.PSObject.Properties) {
            if ($null -ne $property.Value.qa_semantic_command -and @($property.Value.qa_semantic_command).Count -gt 0) {
                return $true
            }
        }
    }
    catch {
        # The Go worker remains the authoritative validator for the workspace
        # registry. Do not mask its useful error with a launcher-only parser.
        return $false
    }
    return $false
}

function Test-SupportedStagehandNode([string]$Version) {
    if ($Version -notmatch '^v?(\d+)\.(\d+)\.(\d+)$') { return $false }
    $major = [int]$matches[1]
    $minor = [int]$matches[2]
    return $major -gt 22 -or ($major -eq 22 -and $minor -ge 12) -or ($major -eq 20 -and $minor -ge 19)
}

function Ensure-StagehandNodeRuntime {
    if (-not (Test-StagehandConfigured)) { return }
    $configured = [Environment]::GetEnvironmentVariable('ITBEM_STAGEHAND_NODE_EXECUTABLE', 'Process')
    if (-not [string]::IsNullOrWhiteSpace($configured)) {
        if (-not (Test-Path -LiteralPath $configured -PathType Leaf)) {
            throw 'ITBEM_STAGEHAND_NODE_EXECUTABLE does not point to a local executable.'
        }
        return
    }
    $globalVersion = (& node --version 2>$null | Select-Object -First 1).Trim()
    if (Test-SupportedStagehandNode $globalVersion) { return }

    # Stagehand v3 needs Node >=20.19. On machines with an older global Node,
    # obtain an isolated Node 24 runtime through npm's cache instead of
    # changing Windows-wide tooling. This runs only for a workspace that has
    # explicitly configured the pinned Stagehand runner.
    $runtime = (& npx --yes --package=node@24 node -p 'process.execPath' 2>$null | Select-Object -First 1).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($runtime) -or -not (Test-Path -LiteralPath $runtime -PathType Leaf)) {
        throw 'Stagehand requires Node ^20.19.0 or >=22.12.0. Install a compatible runtime or set ITBEM_STAGEHAND_NODE_EXECUTABLE.'
    }
    [Environment]::SetEnvironmentVariable('ITBEM_STAGEHAND_NODE_EXECUTABLE', $runtime, 'Process')
    Write-Host 'Using an isolated Node 24 runtime for the local Stagehand worker.'
}

function Enter-LocalAgentWorkerLock {
    # A process-wide, session-local mutex makes the launcher the owner of the
    # single local consumer. It covers both one-shot and KeepAlive modes, so a
    # second terminal cannot accidentally double-consume the same SQS queue.
    # Diagnostics and workspace sync intentionally do not take this lock.
    $createdNew = $false
    $mutex = $null
    try {
        $mutex = [System.Threading.Mutex]::new($true, 'Local\ITBEM.LocalAIAgent.Worker', [ref]$createdNew)
    }
    catch [System.Threading.AbandonedMutexException] {
        # The previous launcher was terminated abruptly. Taking ownership is
        # safe because the OS has released that process's mutex ownership.
        $createdNew = $true
    }

    if (-not $createdNew) {
        if ($null -ne $mutex) { $mutex.Dispose() }
        throw 'Another local ITBEM AI worker is already running in this Windows session. Stop it first instead of starting a second queue consumer.'
    }
    return $mutex
}

Import-LocalAgentEnvironment (Join-Path $backendRoot '.env.ai.local') $PreferProcessEnvironment
if ($Doctor) {
    Set-Location $backendRoot
    go run ./cmd/itbem-ai-agent --doctor
    exit $LASTEXITCODE
}
if ($SyncWorkspaces) {
    Set-Location $backendRoot
    go run ./cmd/itbem-ai-agent --sync-workspaces
    exit $LASTEXITCODE
}
$provider = [Environment]::GetEnvironmentVariable('ITBEM_AI_PROVIDER', 'Process')
if ([string]::IsNullOrWhiteSpace($provider)) { $provider = 'minimax' }
$provider = $provider.Trim().ToLowerInvariant()
$secretName = @{ minimax = 'MINIMAX_API_KEY'; openai = 'OPENAI_API_KEY'; anthropic = 'ANTHROPIC_API_KEY' }[$provider]
if (-not $secretName) { throw 'ITBEM_AI_PROVIDER must be minimax, openai, or anthropic.' }

foreach ($name in @($secretName, 'ITBEM_AI_WORKSPACES_JSON', 'ITBEM_AI_CONCURRENCY')) {
    if (-not [Environment]::GetEnvironmentVariable($name, 'Process')) {
        $userValue = [Environment]::GetEnvironmentVariable($name, 'User')
        if ($userValue) { [Environment]::SetEnvironmentVariable($name, $userValue, 'Process') }
    }
}
if (-not [Environment]::GetEnvironmentVariable($secretName, 'Process')) {
    throw "$secretName must be set in .env.ai.local, the current process, or Windows User environment. Never commit local secrets."
}

if ($ProviderSmoke) {
    Write-Host "Running one explicit provider smoke check with '$provider'."
    $env:ITBEM_AI_ALLOW_PROVIDER_SMOKE = '1'
    Set-Location $backendRoot
    go run ./cmd/itbem-ai-agent --provider-smoke
    exit $LASTEXITCODE
}

Ensure-StagehandNodeRuntime

$env:AWS_ACCESS_KEY_ID = 'test'
$env:AWS_SECRET_ACCESS_KEY = 'test'
# The control plane and worker must use the same region. Resolve it before the
# queue lookup instead of silently creating a homonymous queue elsewhere.
$AWSRegion = Resolve-LocalAWSRegion $AWSRegion $backendRoot
$env:AWS_REGION = $AWSRegion
$env:AWS_DEFAULT_REGION = $AWSRegion
$queueLookup = & aws sqs get-queue-url --endpoint-url $LocalStackEndpoint --queue-name 'itbem-ai-local' --query QueueUrl --output text
$queueURL = ([string]$queueLookup).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($queueURL)) {
    throw 'The local ITBEM automation queue is unavailable. Start the local control plane first.'
}

$queueRegion = ''
try {
    $queueHost = ([Uri]$queueURL).Host
    if ($queueHost -match '^sqs\.([a-z0-9-]+)\.') {
        $queueRegion = $matches[1]
    }
}
catch {
    throw 'The local ITBEM automation queue returned an invalid URL.'
}
if ($queueRegion -and $AWSRegion -ne $queueRegion) {
    throw "Local automation queue region '$queueRegion' does not match control-plane region '$AWSRegion'. Restart the local AI control plane."
}

$env:ITBEM_AI_QUEUE_URL = $queueURL
$env:ITBEM_AI_SQS_ENDPOINT = $LocalStackEndpoint
$env:ITBEM_AI_S3_ENDPOINT = $LocalStackEndpoint
$env:ITBEM_AI_INPUT_BUCKET = 'itbem-ai-inputs-local'
$env:ITBEM_AI_OUTPUT_BUCKET = 'itbem-ai-outputs-local'
$env:ITBEM_API_BASE_URL = $ApiBaseURL.TrimEnd('/')
$env:AUTOMATION_CALLBACK_SECRET = 'local-automation-callback-secret'

$workerLock = Enter-LocalAgentWorkerLock
$exitCode = 0
try {
    Write-Host "Starting isolated ITBEM Go AI agent with provider '$provider'."
    Set-Location $backendRoot
    if (-not $KeepAlive) {
        go run ./cmd/itbem-ai-agent
        $exitCode = $LASTEXITCODE
    }
    else {
        # A continuous local worker must prove its static configuration before it is
        # allowed to enter a restart loop. This doctor is credential-free and makes no
        # provider, AWS or GitHub request, so it cannot create a noisy crash/restart
        # cycle around a broken workspace registry or missing runtime contract.
        Write-Host 'Verifying local agent readiness before entering service mode.'
        go run ./cmd/itbem-ai-agent --doctor
        if ($LASTEXITCODE -ne 0) {
            throw 'Local agent readiness check failed. Fix the reported configuration before starting service mode.'
        }

        $consecutiveFailures = 0
        while ($true) {
            go run ./cmd/itbem-ai-agent
            $agentExitCode = $LASTEXITCODE
            if ($agentExitCode -eq 0) {
                # A clean exit is normally a deliberate Ctrl+C/SIGTERM. Do not turn it
                # into a hidden background process; the operator can start it again.
                break
            }

            $consecutiveFailures++
            # Bound the restart delay so a transient endpoint outage recovers without
            # either hot-looping or leaving local automation offline for too long.
            $delay = [Math]::Min(60, $RestartDelaySeconds * [Math]::Pow(2, [Math]::Min($consecutiveFailures - 1, 4)))
            Write-Warning "ITBEM AI agent exited with code $agentExitCode. Restarting in $([int]$delay)s (failure $consecutiveFailures)."
            Start-Sleep -Seconds ([int]$delay)
        }
    }
}
finally {
    $workerLock.ReleaseMutex()
    $workerLock.Dispose()
}

exit $exitCode
