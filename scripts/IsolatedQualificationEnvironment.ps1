function Initialize-IsolatedQualificationEnvironment {
    # Operate on explicit names only; never inspect or serialize inherited
    # credential values. Synthetic OIDC must not gain a real publication App
    # or model credential through the normal developer launch configuration.
    $remoteNames = @(
        'ITBEM_GITHUB_APP_ID','ITBEM_GITHUB_INSTALLATION_ID','ITBEM_GITHUB_INSTALLATION_IDS',
        'ITBEM_GITHUB_APP_PRIVATE_KEY','ITBEM_GITHUB_APP_PRIVATE_KEY_FILE','ITBEM_GITHUB_API_BASE_URL',
        'ITBEM_GITHUB_SOURCE_APP_ID','ITBEM_GITHUB_SOURCE_INSTALLATION_ID','ITBEM_GITHUB_SOURCE_INSTALLATION_IDS',
        'ITBEM_GITHUB_SOURCE_APP_PRIVATE_KEY','ITBEM_GITHUB_SOURCE_APP_PRIVATE_KEY_FILE','ITBEM_GITHUB_SOURCE_API_BASE_URL',
        'GITHUB_REVIEW_WEBHOOK_SECRET','GITHUB_REVIEW_REPOSITORIES','ITBEM_AI_WORKSPACES_JSON',
        'AI_PROVIDER_CREDENTIALS_LOCAL_FILE','AI_PROVIDER_CREDENTIALS_SECRET_ID',
        'MINIMAX_API_KEY','DEEPSEEK_API_KEY','OPENAI_API_KEY','OPENROUTER_API_KEY','ANTHROPIC_API_KEY','OPENCODE_GO_API_KEY',
        'ITBEM_AI_GATEWAY_URL','ITBEM_AI_GATEWAY_TOKEN',
        'AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY','AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY_PREVIOUS','AUTOMATION_CALLBACK_SECRET',
        'AWS_SESSION_TOKEN','AWS_PROFILE','AWS_DEFAULT_PROFILE','AWS_WEB_IDENTITY_TOKEN_FILE','AWS_ROLE_ARN',
        'AWS_CONTAINER_CREDENTIALS_RELATIVE_URI','AWS_CONTAINER_CREDENTIALS_FULL_URI'
    )
    foreach ($name in $remoteNames) { [Environment]::SetEnvironmentVariable($name, $null, 'Process') }
    $env:AWS_ACCESS_KEY_ID = 'test'
    $env:AWS_SECRET_ACCESS_KEY = 'test'
    $env:AWS_EC2_METADATA_DISABLED = 'true'
    $bytes = New-Object byte[] 48
    $generator = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $generator.GetBytes($bytes) } finally { $generator.Dispose() }
    $env:AUTOMATION_ATTEMPT_POLICY_SIGNING_KEY = [Convert]::ToBase64String($bytes)
}
