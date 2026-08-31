[CmdletBinding()]
param(
    [int]$Port = 8081,
    [string]$DatabaseName = 'events_ai_local',
    [string]$DatabaseHost = 'localhost',
    [string]$DatabasePort = '5432',
    [string]$DatabaseUser = 'postgres',
    [string]$RedisHost = 'localhost:6379',
    # Prefer the inherited process setting so a local database password never
    # needs to appear in a command line or PowerShell history. Passing this
    # parameter remains supported for explicit, one-off development setups.
    [string]$DatabasePassword = '',
	# Empty keeps the repository compose service probe. Set this only for an
	# explicitly isolated local PostgreSQL container (for example a dedicated
	# integration database on another port); it never changes that container.
	[string]$DatabaseProbeContainer = '',
    # Windows hosts may run the disposable container in a WSL Docker Engine
    # while Docker Desktop is unavailable. This changes only the read-only
    # probe transport; the API still connects through the loopback port.
    [switch]$DatabaseProbeInWSL,
    [string]$WSLDistribution = 'Ubuntu',
    [Alias('LocalStackEndpoint')]
    [string]$AwsEmulatorEndpoint = 'http://localhost:4566',
    # Deliberately opt-in and local-only. The API ignores this setting outside
    # ENV=local, and authentication still comes from Cognito before a role is
    # granted. Prefer passing it from an ignored local environment file.
    [string]$BootstrapRootEmails = '',
    # Strict validation is the default. Use this only when the host clock is
    # known to be skewed relative to Cognito; it is bounded and local-only.
    [ValidateRange(0, 86400)]
    [int]$JwtClockSkewSeconds = 0,
    # Optional, cost-free identity fixture for isolated qualification. The API
    # accepts these only with ENV=local and loopback URLs; production continues
    # to derive its issuer and JWKS endpoint from Cognito.
    [string]$OIDCIssuerURL = '',
    [string]$OIDCJWKSURL = '',
    [string]$OIDCAudience = 'local-itbem',
    # Opt in to the same five-lane topology used by the Linux systemd
    # deployment. The legacy combined queue remains the default so an existing
    # single-worker local setup is never silently stranded.
    [switch]$RoleLanes
)

$ErrorActionPreference = 'Stop'

function Test-LoopbackHostName([string]$HostName) {
    if ($HostName.Trim().ToLowerInvariant() -eq 'localhost') { return $true }
    $address = $null
    if (-not [Net.IPAddress]::TryParse($HostName.Trim('[', ']'), [ref]$address)) { return $false }
    return [Net.IPAddress]::IsLoopback($address)
}

if ([string]::IsNullOrWhiteSpace($DatabasePassword)) {
    $DatabasePassword = [Environment]::GetEnvironmentVariable('ITBEM_LOCAL_DB_PASSWORD', 'Process')
}
if ([string]::IsNullOrWhiteSpace($DatabasePassword)) {
    # Keep the repository's original disposable-compose default working. An
    # isolated database with a different credential should use the process
    # variable above, so that credential never appears in shell history.
    $DatabasePassword = 'postgres'
}

$endpointUri = [Uri]$AwsEmulatorEndpoint
if ($endpointUri.Scheme -ne 'http' -or -not (Test-LoopbackHostName $endpointUri.Host)) {
    throw 'AwsEmulatorEndpoint must be an HTTP loopback endpoint.'
}

$hasOIDCIssuer = -not [string]::IsNullOrWhiteSpace($OIDCIssuerURL)
$hasOIDCJWKS = -not [string]::IsNullOrWhiteSpace($OIDCJWKSURL)
if ($hasOIDCIssuer -ne $hasOIDCJWKS) {
    throw 'OIDCIssuerURL and OIDCJWKSURL must be configured together.'
}
if ($hasOIDCIssuer) {
    foreach ($candidate in @($OIDCIssuerURL, $OIDCJWKSURL)) {
        $candidateUri = [Uri]$candidate
        if ($candidateUri.Scheme -notin @('http', 'https') -or -not (Test-LoopbackHostName $candidateUri.Host) -or -not [string]::IsNullOrWhiteSpace($candidateUri.UserInfo) -or -not [string]::IsNullOrWhiteSpace($candidateUri.Query) -or -not [string]::IsNullOrWhiteSpace($candidateUri.Fragment)) {
            throw 'Local OIDC endpoints must be absolute loopback HTTP(S) URLs without credentials, query, or fragment.'
        }
    }
    if ([string]::IsNullOrWhiteSpace($OIDCAudience)) {
        throw 'OIDCAudience is required when local OIDC endpoints are configured.'
    }
    $redisUri = [Uri]("tcp://$RedisHost")
    if (-not (Test-LoopbackHostName $DatabaseHost) -or -not (Test-LoopbackHostName $redisUri.Host) -or $redisUri.Port -lt 1) {
        throw 'Local OIDC qualification requires loopback PostgreSQL and Valkey endpoints.'
    }
}

function Read-EnvironmentFile([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Dashboard environment file was not found: $Path"
    }

    $values = @{}
    Get-Content -LiteralPath $Path | ForEach-Object {
        if ($_ -match '^\s*([^#=\s]+)=(.*)$') {
            $values[$matches[1]] = $matches[2].Trim().Trim('"')
        }
    }
    return $values
}

function Require-Value([hashtable]$Values, [string]$Name) {
    if ([string]::IsNullOrWhiteSpace($Values[$Name])) {
        throw "Required local dashboard setting is missing: $Name"
    }
    return $Values[$Name]
}

function Test-LocalPostgreSQL([string]$ComposeFile, [string]$DatabaseUser, [string]$DatabaseName, [string]$DatabaseProbeContainer, [bool]$DatabaseProbeInWSL, [string]$WSLDistribution) {
    # Docker's health check only proves that the postmaster process is alive.
    # The control plane needs a real catalog query before it starts, otherwise a
    # corrupted local cluster becomes an opaque "connection" error in the UI.
    # This is intentionally read-only: it never creates, migrates, repairs or
    # deletes a database.
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
		if ([string]::IsNullOrWhiteSpace($DatabaseProbeContainer)) {
			$queryOutput = @(& docker compose -f $ComposeFile exec -T postgres psql -U $DatabaseUser -d $DatabaseName -v ON_ERROR_STOP=1 -Atqc 'SELECT 1' 2>&1)
		} elseif ($DatabaseProbeInWSL) {
			$queryOutput = @(& wsl.exe -d $WSLDistribution -- docker exec $DatabaseProbeContainer psql -U $DatabaseUser -d $DatabaseName -v ON_ERROR_STOP=1 -Atqc 'SELECT 1' 2>&1)
		} else {
			$queryOutput = @(& docker exec $DatabaseProbeContainer psql -U $DatabaseUser -d $DatabaseName -v ON_ERROR_STOP=1 -Atqc 'SELECT 1' 2>&1)
		}
        $queryExitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorAction
    }

    if ($queryExitCode -eq 0 -and (($queryOutput -join [Environment]::NewLine).Trim() -eq '1')) {
        return
    }

    $diagnostic = ($queryOutput -join [Environment]::NewLine)
    if ($diagnostic -match 'critical system index|PANIC:') {
        throw "Local PostgreSQL failed a read-only health query for '$DatabaseName' because its system catalog is corrupted. No data was changed. Stop here, preserve the local volume, and follow docs/LOCAL_DATABASE_RECOVERY.md before any reset."
    }
    if ($diagnostic -match 'database .* does not exist') {
        throw "The isolated local database '$DatabaseName' does not exist. Create it through the documented local bootstrap, then run this command again. No data was changed."
    }
    throw "Local PostgreSQL did not pass a read-only SELECT 1 query for '$DatabaseName'. The API was not started. Inspect 'docker compose logs postgres' and docs/LOCAL_DATABASE_RECOVERY.md; no data was changed."
}

function Ensure-LocalBucket([string]$Bucket) {
    # A missing bucket is the normal first-run condition. PowerShell 5.1 turns
    # an expected non-zero native exit into a terminating NativeCommandError
    # under the script-wide Stop preference, so capture that exit deliberately.
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        & aws s3api head-bucket --endpoint-url $AwsEmulatorEndpoint --bucket $Bucket 2>$null
        $headExitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorAction
    }
    if ($headExitCode -ne 0) {
        & aws s3api create-bucket --endpoint-url $AwsEmulatorEndpoint --bucket $Bucket | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Could not create local bucket $Bucket" }
    }
}

function Ensure-LocalBucketCors([string]$Bucket) {
    # The dashboard reads short-lived private URLs directly from the emulator in
    # local development. Mirror the browser contract deliberately instead of
    # disabling CORS or widening it to arbitrary origins.
    $corsConfiguration = Join-Path $PSScriptRoot 'localstack-bucket-cors.json'
    if (-not (Test-Path -LiteralPath $corsConfiguration -PathType Leaf)) {
        throw "Local CORS policy was not found: $corsConfiguration"
    }
    & aws s3api put-bucket-cors --endpoint-url $AwsEmulatorEndpoint --bucket $Bucket --cors-configuration "file://$corsConfiguration" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Could not configure local browser access for bucket $Bucket" }
}

function Ensure-LocalQueue([string]$QueueName) {
    # Like buckets, a queue may not exist during first bootstrap. Treat that
    # result as data, then create it, instead of letting PowerShell abort.
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $queueURL = ((@(& aws sqs get-queue-url --endpoint-url $AwsEmulatorEndpoint --queue-name $QueueName --query QueueUrl --output text 2>$null) -join [Environment]::NewLine)).Trim()
        $queueExitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorAction
    }
    if ($queueExitCode -eq 0 -and -not [string]::IsNullOrWhiteSpace($queueURL)) {
        return $queueURL
    }
    $queueURL = ((@(& aws sqs create-queue --endpoint-url $AwsEmulatorEndpoint --queue-name $QueueName --query QueueUrl --output text) -join [Environment]::NewLine)).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($queueURL)) {
        throw "Could not create or resolve local queue $QueueName"
    }
    return $queueURL
}

function Get-LocalQueueArn([string]$QueueURL) {
    $queueArn = ((@(& aws sqs get-queue-attributes --endpoint-url $AwsEmulatorEndpoint --queue-url $QueueURL --attribute-names QueueArn --query Attributes.QueueArn --output text) -join [Environment]::NewLine)).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($queueArn)) {
        throw 'Could not resolve a local ITBEM automation queue ARN.'
    }
    return $queueArn
}

function Set-LocalQueueRedrive([string]$QueueURL, [string]$DeadLetterQueueArn) {
    $redrivePolicy = @{ deadLetterTargetArn = $DeadLetterQueueArn; maxReceiveCount = '5' } | ConvertTo-Json -Compress
    $redriveRequest = @{ QueueUrl = $QueueURL; Attributes = @{ VisibilityTimeout = '120'; MessageRetentionPeriod = '1209600'; RedrivePolicy = $redrivePolicy } } | ConvertTo-Json -Depth 4 -Compress
    $redriveRequestFile = [System.IO.Path]::GetTempFileName()
    try {
        [System.IO.File]::WriteAllText($redriveRequestFile, $redriveRequest, (New-Object System.Text.UTF8Encoding($false)))
        & aws sqs set-queue-attributes --endpoint-url $AwsEmulatorEndpoint --cli-input-json "file://$redriveRequestFile" | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Could not configure a local automation queue dead-letter policy.' }
    }
    finally {
        Remove-Item -LiteralPath $redriveRequestFile -Force -ErrorAction SilentlyContinue
    }
}

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$workspaceRoot = Split-Path -Parent $repositoryRoot
$composeFile = Join-Path $repositoryRoot 'docker-compose.yml'
if (-not (Test-Path -LiteralPath $composeFile -PathType Leaf)) {
    throw "Local Docker Compose file was not found: $composeFile"
}
Test-LocalPostgreSQL $composeFile $DatabaseUser $DatabaseName $DatabaseProbeContainer $DatabaseProbeInWSL.IsPresent $WSLDistribution
# The control plane owns remote context synchronization and publication grants,
# while the local agent owns model execution. Keep their development setup
# aligned without loading provider credentials into the API process. The three
# GitHub App values mint a short-lived installation token, while the workspace
# registry is non-secret local topology required to validate and freeze
# `workspace://` references. The provider key never crosses this boundary.
$agentSettingsPath = Join-Path $repositoryRoot '.env.ai.local'
if (Test-Path -LiteralPath $agentSettingsPath -PathType Leaf) {
    $agentSettings = Read-EnvironmentFile $agentSettingsPath
    foreach ($name in @('ITBEM_GITHUB_APP_ID', 'ITBEM_GITHUB_INSTALLATION_ID', 'ITBEM_GITHUB_INSTALLATION_IDS', 'ITBEM_GITHUB_APP_PRIVATE_KEY', 'ITBEM_GITHUB_APP_PRIVATE_KEY_FILE', 'ITBEM_GITHUB_API_BASE_URL', 'GITHUB_REVIEW_WEBHOOK_SECRET', 'GITHUB_REVIEW_REPOSITORIES', 'ITBEM_AI_WORKSPACES_JSON')) {
        if (-not [string]::IsNullOrWhiteSpace($agentSettings[$name])) {
            Set-Item -Path "Env:$name" -Value $agentSettings[$name]
        }
    }
	# Cost admission needs the worker's provider/model identity but never its
	# API key. Keep this explicit so a budget cannot silently price one model
	# while the worker executes another.
	$budgetProvider = $agentSettings['ITBEM_AI_PROVIDER']
	if ([string]::IsNullOrWhiteSpace($budgetProvider)) { $budgetProvider = 'minimax' }
	$budgetModel = switch ($budgetProvider.Trim().ToLowerInvariant()) {
		'minimax' { $agentSettings['MINIMAX_MODEL'] }
		'openai' { $agentSettings['OPENAI_MODEL'] }
		'anthropic' { $agentSettings['ANTHROPIC_MODEL'] }
		default { '' }
	}
	if ([string]::IsNullOrWhiteSpace($budgetModel) -and $budgetProvider.Trim().ToLowerInvariant() -eq 'minimax') { $budgetModel = 'MiniMax-M3' }
	$env:AUTOMATION_BUDGET_PROVIDER = $budgetProvider.Trim()
	$env:AUTOMATION_BUDGET_MODEL = $budgetModel.Trim()
}
if ($hasOIDCIssuer) {
    # These values satisfy the shared Config shape only. The local-only token
    # middleware uses the validated loopback issuer/JWKS pair instead and no
    # Cognito call is made for authentication.
    $cognitoRegion = 'us-east-1'
    $userPoolID = 'local-qualification'
    $eventiappClientID = ''
    $itbemClientID = ''
    $cafettonHouseClientID = ''
} else {
    $dashboardSettings = Read-EnvironmentFile (Join-Path $workspaceRoot 'dashboard-ts/.env.local')
    $cognitoRegion = Require-Value $dashboardSettings 'COGNITO_REGION'
    $userPoolID = Require-Value $dashboardSettings 'COGNITO_USER_POOL_ID'
    $eventiappClientID = Require-Value $dashboardSettings 'COGNITO_EVENTIAPP_CLIENT_ID'
    $itbemClientID = Require-Value $dashboardSettings 'COGNITO_ITBEM_CLIENT_ID'
    $cafettonHouseClientID = Require-Value $dashboardSettings 'COGNITO_CAFETTONHOUSE_CLIENT_ID'
}

# Local emulator only. These disposable credentials never leave loopback.
$awsCredentialEnvironment = @{}
foreach ($name in @('AWS_ACCESS_KEY_ID', 'AWS_SECRET_ACCESS_KEY', 'AWS_SESSION_TOKEN', 'AWS_REGION', 'AWS_DEFAULT_REGION')) {
    $awsCredentialEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$env:AWS_ACCESS_KEY_ID = 'test'
$env:AWS_SECRET_ACCESS_KEY = 'test'
$env:AWS_REGION = $cognitoRegion
$env:AWS_DEFAULT_REGION = $cognitoRegion
$inputBucket = 'itbem-ai-inputs-local'
$outputBucket = 'itbem-ai-outputs-local'
Ensure-LocalBucket $inputBucket
Ensure-LocalBucket $outputBucket
Ensure-LocalBucketCors $inputBucket
Ensure-LocalBucketCors $outputBucket
$automationDLQURL = Ensure-LocalQueue 'itbem-ai-local-dlq'
$automationDLQArn = Get-LocalQueueArn $automationDLQURL
$automationQueueURL = Ensure-LocalQueue 'itbem-ai-local'
Set-LocalQueueRedrive $automationQueueURL $automationDLQArn

$automationRoleDLQURL = ''
$automationRoleLaneQueuesJSON = ''
if ($RoleLanes) {
    $automationRoleDLQURL = Ensure-LocalQueue 'itbem-ai-local-role-dlq'
    $automationRoleDLQArn = Get-LocalQueueArn $automationRoleDLQURL
    $automationRoleLaneQueues = [ordered]@{}
    foreach ($lane in @('orchestration', 'engineering', 'review', 'qa', 'release')) {
        $laneQueueURL = Ensure-LocalQueue "itbem-ai-local-$lane"
        Set-LocalQueueRedrive $laneQueueURL $automationRoleDLQArn
        $automationRoleLaneQueues[$lane] = $laneQueueURL
    }
    $automationRoleLaneQueuesJSON = $automationRoleLaneQueues | ConvertTo-Json -Compress
}

# The local emulator requires disposable test credentials, but Cognito must continue
# using the caller's normal AWS credential chain. Restore it before the API
# process starts so user synchronization and invitation management are real.
foreach ($name in $awsCredentialEnvironment.Keys) {
    $value = $awsCredentialEnvironment[$name]
    if ($null -eq $value) {
        Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue
    } else {
        Set-Item -Path "Env:$name" -Value $value
    }
}

# The API loads all configuration from this process. It never writes credentials
# to .env files, and it uses a fresh database name by default to avoid touching
# the existing local events database.
$env:ENV = 'local'
$env:ALLOW_LOCAL_USER_SYNC_FALLBACK = 'true'
$env:LOCAL_BOOTSTRAP_ROOT_EMAILS = $BootstrapRootEmails.Trim()
$env:JWT_CLOCK_SKEW_SECONDS = "$JwtClockSkewSeconds"
$env:SOURCE_REVISION = 'local-ai-control-plane'
$env:PORT = "$Port"
$env:AWS_REGION = $cognitoRegion
$env:COGNITO_AWS_REGION = $cognitoRegion
$env:COGNITO_USER_POOL_ID = $userPoolID
if ($hasOIDCIssuer) {
    $env:OIDC_ISSUER_URL = $OIDCIssuerURL.TrimEnd('/')
    $env:OIDC_JWKS_URL = $OIDCJWKSURL
    $env:COGNITO_ALLOWED_CLIENT_IDS = $OIDCAudience.Trim()
    $env:COGNITO_TENANT_CLIENT_MAP = "$($OIDCAudience.Trim())=itbem"
} else {
    Remove-Item -Path Env:OIDC_ISSUER_URL -ErrorAction SilentlyContinue
    Remove-Item -Path Env:OIDC_JWKS_URL -ErrorAction SilentlyContinue
    $env:COGNITO_ALLOWED_CLIENT_IDS = "$eventiappClientID=eventiapp,$itbemClientID=itbem,$cafettonHouseClientID=cafettonhouse"
    $env:COGNITO_TENANT_CLIENT_MAP = $env:COGNITO_ALLOWED_CLIENT_IDS
}
$env:TENANT_HOST_MAP = 'localhost=itbem'
$env:AWS_BUCKET_NAME = $inputBucket
$env:TENANT_BUCKET_MAP = "itbem=$inputBucket"
$env:S3_REGION = 'us-east-1'
$env:S3_ENDPOINT = $AwsEmulatorEndpoint
$env:S3_USE_PATH_STYLE = 'true'
$env:S3_CLIENT_ID = 'test'
$env:S3_CLIENT_SECRET = 'test'
$env:DB_HOST = $DatabaseHost
$env:DB_USER = $DatabaseUser
$env:DB_PASSWORD = $DatabasePassword
$env:DB_NAME = $DatabaseName
$env:DB_PORT = $DatabasePort
$env:DB_TIMEZONE = 'America/Mexico_City'
$env:DB_LOG_LEVEL = 'warn'
$env:REDIS_HOST = $RedisHost
$env:REDIS_DB = '0'
$env:REDIS_TLS = 'false'
$env:GOOGLE_CLIENT_ID = 'local-not-configured'
$env:GOOGLE_CLIENT_SECRET = 'local-not-configured'
$env:CORS_ALLOW_ORIGINS = 'http://dashboard.itbem.localhost:3000,http://dashboard.itbem.localhost:3001'
$env:SQS_AUTOMATION_QUEUE_URL = $automationQueueURL
$env:SQS_AUTOMATION_DEAD_LETTER_QUEUE_URL = $automationDLQURL
if ($RoleLanes) {
    $env:SQS_AUTOMATION_QUEUE_LANES_JSON = $automationRoleLaneQueuesJSON
    $env:SQS_AUTOMATION_ROLE_DEAD_LETTER_QUEUE_URL = $automationRoleDLQURL
} else {
    Remove-Item -Path Env:SQS_AUTOMATION_QUEUE_LANES_JSON -ErrorAction SilentlyContinue
    Remove-Item -Path Env:SQS_AUTOMATION_ROLE_DEAD_LETTER_QUEUE_URL -ErrorAction SilentlyContinue
}
$env:AUTOMATION_INPUT_BUCKET = $inputBucket
$env:AUTOMATION_OUTPUT_BUCKET = $outputBucket
$env:SQS_ENDPOINT = $AwsEmulatorEndpoint
$env:AUTOMATION_CALLBACK_SECRET = 'local-automation-callback-secret'

Write-Host "Starting isolated AI control plane on port $Port (database: $DatabaseName)."
Push-Location $repositoryRoot
try {
    go run ./cmd/api
}
finally {
    Pop-Location
}
