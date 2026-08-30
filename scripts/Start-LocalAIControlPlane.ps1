[CmdletBinding()]
param(
    [int]$Port = 8081,
    [string]$DatabaseName = 'events_ai_local',
    [string]$DatabaseHost = 'localhost',
    [string]$DatabasePort = '5432',
    [string]$DatabaseUser = 'postgres',
    # Prefer the inherited process setting so a local database password never
    # needs to appear in a command line or PowerShell history. Passing this
    # parameter remains supported for explicit, one-off development setups.
    [string]$DatabasePassword = '',
	# Empty keeps the repository compose service probe. Set this only for an
	# explicitly isolated local PostgreSQL container (for example a dedicated
	# integration database on another port); it never changes that container.
	[string]$DatabaseProbeContainer = '',
    [string]$LocalStackEndpoint = 'http://localhost:4566',
    # Deliberately opt-in and local-only. The API ignores this setting outside
    # ENV=local, and authentication still comes from Cognito before a role is
    # granted. Prefer passing it from an ignored local environment file.
    [string]$BootstrapRootEmails = '',
    # Strict validation is the default. Use this only when the host clock is
    # known to be skewed relative to Cognito; it is bounded and local-only.
    [ValidateRange(0, 86400)]
    [int]$JwtClockSkewSeconds = 0
)

$ErrorActionPreference = 'Stop'

if ([string]::IsNullOrWhiteSpace($DatabasePassword)) {
    $DatabasePassword = [Environment]::GetEnvironmentVariable('ITBEM_LOCAL_DB_PASSWORD', 'Process')
}
if ([string]::IsNullOrWhiteSpace($DatabasePassword)) {
    # Keep the repository's original disposable-compose default working. An
    # isolated database with a different credential should use the process
    # variable above, so that credential never appears in shell history.
    $DatabasePassword = 'postgres'
}

$endpointUri = [Uri]$LocalStackEndpoint
if ($endpointUri.Scheme -ne 'http' -or $endpointUri.Host -notin @('localhost', '127.0.0.1', '::1')) {
    throw 'LocalStackEndpoint must be an HTTP loopback endpoint.'
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

function Test-LocalPostgreSQL([string]$ComposeFile, [string]$DatabaseUser, [string]$DatabaseName, [string]$DatabaseProbeContainer) {
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
        & aws s3api head-bucket --endpoint-url $LocalStackEndpoint --bucket $Bucket 2>$null
        $headExitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorAction
    }
    if ($headExitCode -ne 0) {
        & aws s3api create-bucket --endpoint-url $LocalStackEndpoint --bucket $Bucket | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Could not create local bucket $Bucket" }
    }
}

function Ensure-LocalBucketCors([string]$Bucket) {
    # The dashboard reads short-lived private URLs directly from LocalStack in
    # local development. Mirror the browser contract deliberately instead of
    # disabling CORS or widening it to arbitrary origins.
    $corsConfiguration = Join-Path $PSScriptRoot 'localstack-bucket-cors.json'
    if (-not (Test-Path -LiteralPath $corsConfiguration -PathType Leaf)) {
        throw "Local CORS policy was not found: $corsConfiguration"
    }
    & aws s3api put-bucket-cors --endpoint-url $LocalStackEndpoint --bucket $Bucket --cors-configuration "file://$corsConfiguration" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Could not configure local browser access for bucket $Bucket" }
}

function Ensure-LocalQueue([string]$QueueName) {
    # Like buckets, a queue may not exist during first bootstrap. Treat that
    # result as data, then create it, instead of letting PowerShell abort.
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $queueURL = ((@(& aws sqs get-queue-url --endpoint-url $LocalStackEndpoint --queue-name $QueueName --query QueueUrl --output text 2>$null) -join [Environment]::NewLine)).Trim()
        $queueExitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorAction
    }
    if ($queueExitCode -eq 0 -and -not [string]::IsNullOrWhiteSpace($queueURL)) {
        return $queueURL
    }
    $queueURL = ((@(& aws sqs create-queue --endpoint-url $LocalStackEndpoint --queue-name $QueueName --query QueueUrl --output text) -join [Environment]::NewLine)).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($queueURL)) {
        throw "Could not create or resolve local queue $QueueName"
    }
    return $queueURL
}

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$workspaceRoot = Split-Path -Parent $repositoryRoot
$composeFile = Join-Path $repositoryRoot 'docker-compose.yml'
if (-not (Test-Path -LiteralPath $composeFile -PathType Leaf)) {
    throw "Local Docker Compose file was not found: $composeFile"
}
Test-LocalPostgreSQL $composeFile $DatabaseUser $DatabaseName $DatabaseProbeContainer
$dashboardSettings = Read-EnvironmentFile (Join-Path $workspaceRoot 'dashboard-ts/.env.local')
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
$cognitoRegion = Require-Value $dashboardSettings 'COGNITO_REGION'
$userPoolID = Require-Value $dashboardSettings 'COGNITO_USER_POOL_ID'
$eventiappClientID = Require-Value $dashboardSettings 'COGNITO_EVENTIAPP_CLIENT_ID'
$itbemClientID = Require-Value $dashboardSettings 'COGNITO_ITBEM_CLIENT_ID'
$cafettonHouseClientID = Require-Value $dashboardSettings 'COGNITO_CAFETTONHOUSE_CLIENT_ID'

# LocalStack only. These disposable credentials never leave the local endpoint.
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
$automationDLQArn = ((@(& aws sqs get-queue-attributes --endpoint-url $LocalStackEndpoint --queue-url $automationDLQURL --attribute-names QueueArn --query Attributes.QueueArn --output text) -join [Environment]::NewLine)).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($automationDLQArn)) {
    throw 'Could not resolve the local ITBEM automation dead-letter queue ARN.'
}
$automationQueueURL = Ensure-LocalQueue 'itbem-ai-local'
$redrivePolicy = @{ deadLetterTargetArn = $automationDLQArn; maxReceiveCount = '5' } | ConvertTo-Json -Compress
$redriveRequest = @{ QueueUrl = $automationQueueURL; Attributes = @{ VisibilityTimeout = '120'; MessageRetentionPeriod = '1209600'; RedrivePolicy = $redrivePolicy } } | ConvertTo-Json -Depth 4 -Compress
$redriveRequestFile = Join-Path ([System.IO.Path]::GetTempPath()) 'itbem-ai-local-redrive.json'
try {
    [System.IO.File]::WriteAllText($redriveRequestFile, $redriveRequest, (New-Object System.Text.UTF8Encoding($false)))
    & aws sqs set-queue-attributes --endpoint-url $LocalStackEndpoint --cli-input-json "file://$redriveRequestFile" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not configure the local automation queue dead-letter policy.' }
}
finally {
    Remove-Item -LiteralPath $redriveRequestFile -Force -ErrorAction SilentlyContinue
}

# LocalStack requires disposable test credentials, but Cognito must continue
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
$env:COGNITO_ALLOWED_CLIENT_IDS = "$eventiappClientID=eventiapp,$itbemClientID=itbem,$cafettonHouseClientID=cafettonhouse"
$env:COGNITO_TENANT_CLIENT_MAP = $env:COGNITO_ALLOWED_CLIENT_IDS
$env:TENANT_HOST_MAP = 'localhost=itbem'
$env:AWS_BUCKET_NAME = $inputBucket
$env:TENANT_BUCKET_MAP = "itbem=$inputBucket"
$env:S3_REGION = 'us-east-1'
$env:S3_ENDPOINT = $LocalStackEndpoint
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
$env:REDIS_HOST = 'localhost:6379'
$env:REDIS_DB = '0'
$env:REDIS_TLS = 'false'
$env:GOOGLE_CLIENT_ID = 'local-not-configured'
$env:GOOGLE_CLIENT_SECRET = 'local-not-configured'
$env:CORS_ALLOW_ORIGINS = 'http://dashboard.itbem.localhost:3000,http://dashboard.itbem.localhost:3001'
$env:SQS_AUTOMATION_QUEUE_URL = $automationQueueURL
$env:SQS_AUTOMATION_DEAD_LETTER_QUEUE_URL = $automationDLQURL
$env:AUTOMATION_INPUT_BUCKET = $inputBucket
$env:AUTOMATION_OUTPUT_BUCKET = $outputBucket
$env:SQS_ENDPOINT = $LocalStackEndpoint
$env:AUTOMATION_CALLBACK_SECRET = 'local-automation-callback-secret'

Write-Host "Starting isolated AI control plane on port $Port (database: $DatabaseName)."
Push-Location $repositoryRoot
try {
    go run ./cmd/api
}
finally {
    Pop-Location
}
