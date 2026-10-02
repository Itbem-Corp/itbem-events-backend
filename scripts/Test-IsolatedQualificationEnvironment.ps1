$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'IsolatedQualificationEnvironment.ps1')
$canaryNames = @('ITBEM_GITHUB_APP_PRIVATE_KEY','ITBEM_GITHUB_SOURCE_APP_PRIVATE_KEY_FILE','AI_PROVIDER_CREDENTIALS_LOCAL_FILE','AI_PROVIDER_CREDENTIALS_SECRET_ID','MINIMAX_API_KEY','ITBEM_AI_GATEWAY_TOKEN','AWS_SESSION_TOKEN','AWS_PROFILE','AWS_WEB_IDENTITY_TOKEN_FILE','AWS_CONTAINER_CREDENTIALS_FULL_URI')
foreach ($name in $canaryNames) { [Environment]::SetEnvironmentVariable($name, 'synthetic-isolation-canary', 'Process') }
$env:AWS_ACCESS_KEY_ID = 'synthetic-access-canary'
$env:AWS_SECRET_ACCESS_KEY = 'synthetic-secret-canary'
$env:AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY = 'synthetic-signing-canary'
$env:QUALIFICATION_UNRELATED_SETTING = 'preserved'
Initialize-IsolatedQualificationEnvironment
foreach ($name in $canaryNames) {
    if ([Environment]::GetEnvironmentVariable($name, 'Process')) { throw "Qualification inherited a remote authority setting: $name" }
}
if ($env:AWS_ACCESS_KEY_ID -ne 'test' -or $env:AWS_SECRET_ACCESS_KEY -ne 'test' -or $env:AWS_EC2_METADATA_DISABLED -ne 'true') { throw 'Qualification did not select disposable AWS identity.' }
if ($env:AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY -eq 'synthetic-signing-canary' -or $env:AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY.Length -lt 32) { throw 'Qualification did not create process-local signing material.' }
$firstKey = $env:AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY
Initialize-IsolatedQualificationEnvironment
if ($firstKey -eq $env:AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY) { throw 'Separate fixture initialization reused its signing authority.' }
if ($env:QUALIFICATION_UNRELATED_SETTING -ne 'preserved') { throw 'Unrelated process setting changed.' }
'Isolated qualification rejects inherited remote authority and uses fresh process-local signing material.'
