[CmdletBinding()]
param(
    [switch]$LiveMiniMax,
    [ValidateSet('openai','anthropic')]
    [string]$LiveProvider = '',
    [switch]$ScoreSemantics,
    [switch]$AllowSemanticFailures,
    [string]$ScoreReportPath = '',
    [string]$PriorReport = '',
    [ValidateSet('all','implementer_executable_acceptance','delivery_grounded_summary','reviewer_seeded_auth_bypass','product_bounded_options','qa_observed_failure','planner_missing_context')]
    [string]$Role = 'all',
    [ValidateSet('project','environment')]
    [string]$CredentialSource = 'project'
)

$ErrorActionPreference = 'Stop'
$liveProviderRequested = -not [string]::IsNullOrWhiteSpace($LiveProvider)
if ($LiveMiniMax -and $liveProviderRequested) { throw '-LiveMiniMax and -LiveProvider are mutually exclusive.' }
$repoRoot = Split-Path $PSScriptRoot -Parent
$pwshCommand = (Get-Command pwsh -ErrorAction SilentlyContinue).Source
if (-not $pwshCommand) { $pwshCommand = (Get-Command powershell -ErrorAction SilentlyContinue).Source }
if (-not $pwshCommand) { throw 'PowerShell executable unavailable; semantic scoring cannot be invoked.' }
$scorerPath = Join-Path $repoRoot 'scripts/Score-HarnessSemantics.ps1'
if (($ScoreSemantics -or $ScoreReportPath) -and -not (Test-Path -LiteralPath $scorerPath -PathType Leaf)) {
    throw "Semantic scorer unavailable: $scorerPath"
}
if ($ScoreReportPath -and ($LiveMiniMax -or $liveProviderRequested -or $PriorReport -or $Role -ne 'all' -or $CredentialSource -ne 'project')) {
    throw '-ScoreReportPath is a replay-only mode; do not combine it with a live evaluation selector.'
}
if ($ScoreSemantics -and -not ($LiveMiniMax -or $liveProviderRequested) -and -not $ScoreReportPath) {
    throw '-ScoreSemantics requires a live provider selector or -ScoreReportPath.'
}
if ($AllowSemanticFailures -and -not ($ScoreSemantics -or $ScoreReportPath)) {
    throw '-AllowSemanticFailures requires semantic scoring.'
}
$goCandidate = Join-Path (Split-Path $repoRoot -Parent) '.local/toolchains/go1.25.12/bin/go.exe'
$goCommand = if (Test-Path -LiteralPath $goCandidate) { $goCandidate } else { (Get-Command go).Source }
$reportDirectory = Join-Path $repoRoot ('.local/harness-evals/' + (Get-Date -Format 'yyyyMMdd-HHmmss-fff'))
$null = New-Item -ItemType Directory -Path $reportDirectory
$trackedNames = @('PATH','GOTOOLCHAIN','GOPROXY','ITBEM_AI_PROVIDER','ITBEM_AGENT_LIVE_PROVIDER','MINIMAX_API_KEY','OPENAI_API_KEY','ANTHROPIC_API_KEY','ITBEM_AGENT_LIVE_EVAL','ITBEM_AGENT_LIVE_REPORT','ITBEM_AGENT_LIVE_PRIOR_REPORT')
$previous = @{}
foreach ($name in $trackedNames) { $previous[$name] = [Environment]::GetEnvironmentVariable($name,'Process') }

Push-Location $repoRoot
try {
    if ($ScoreReportPath) {
        $resolvedReport = (Resolve-Path -LiteralPath $ScoreReportPath -ErrorAction Stop).Path
        $replayDirectory = Join-Path $repoRoot ('.local/harness-evals/' + (Get-Date -Format 'yyyyMMdd-HHmmss-fff') + '-semantic-replay')
        $null = New-Item -ItemType Directory -Path $replayDirectory
        $scorePath = Join-Path $replayDirectory 'semantic-score.json'
        $scoreArgs = @('-NoProfile', '-File', $scorerPath, '-ReportPath', $resolvedReport, '-OutputPath', $scorePath)
        if ($AllowSemanticFailures) { $scoreArgs += '-AllowFailures' }
        & $pwshCommand @scoreArgs 2>&1 | Tee-Object -FilePath (Join-Path $replayDirectory 'semantic-score.log')
        $semanticExit = $LASTEXITCODE
        Write-Host "Semantic replay artifacts: $replayDirectory"
        if ($semanticExit -ne 0 -and -not $AllowSemanticFailures) {
            throw "Semantic replay failed (exit $semanticExit); inspect the saved evidence."
        }
        return
    }
    $env:PATH = (Split-Path $goCommand -Parent) + ';' + $env:PATH
    $env:GOTOOLCHAIN = 'local'
    $env:GOPROXY = 'off'
    $env:ITBEM_AGENT_LIVE_EVAL = '0'
    $env:ITBEM_AGENT_LIVE_PRIOR_REPORT = ''
    if ($LiveMiniMax -or $liveProviderRequested) {
        # Explicit opt-in. Load only this credential, never the production queue,
        # workspace registry, callback tokens or arbitrary values from the file.
        $selectedProvider = if ($LiveMiniMax) { 'minimax' } else { $LiveProvider }
        $secretName = @{ minimax = 'MINIMAX_API_KEY'; openai = 'OPENAI_API_KEY'; anthropic = 'ANTHROPIC_API_KEY' }[$selectedProvider]
        $modelName = @{ minimax = 'MINIMAX_MODEL'; openai = 'OPENAI_MODEL'; anthropic = 'ANTHROPIC_MODEL' }[$selectedProvider]
        $keyFile = Join-Path $repoRoot '.env.ai.local'
        if ($CredentialSource -eq 'project') {
            if (-not (Test-Path -LiteralPath $keyFile)) { throw 'Project credential file unavailable; use -CredentialSource environment only if intended.' }
            $keyLine = Get-Content -LiteralPath $keyFile | Where-Object { $_ -match ("^\s*" + [regex]::Escape($secretName) + "\s*=") } | Select-Object -Last 1
            if (-not $keyLine) { throw "Project $selectedProvider credential unavailable." }
            [Environment]::SetEnvironmentVariable($secretName, ($keyLine -replace ("^\s*" + [regex]::Escape($secretName) + "\s*=\s*"),'').Trim().Trim('"').Trim("'"), 'Process')
        }
        $selectedSecret = [Environment]::GetEnvironmentVariable($secretName, 'Process')
        if (-not $selectedSecret) { throw "$secretName is unavailable; no evaluation was sent." }
        $env:ITBEM_AI_PROVIDER = $selectedProvider
        $env:ITBEM_AGENT_LIVE_PROVIDER = if ($selectedProvider -eq 'minimax') { '' } else { $selectedProvider }
        $env:ITBEM_AGENT_LIVE_EVAL = '1'
        $env:ITBEM_AGENT_LIVE_REPORT = Join-Path $reportDirectory 'live.json'
        if ($PriorReport) { $env:ITBEM_AGENT_LIVE_PRIOR_REPORT = (Resolve-Path -LiteralPath $PriorReport).Path }
        Write-Host "Paid synthetic $selectedProvider evaluation: at most 12 calls and 1 USD API-equivalent admission reserve, including a supplied prior report."
        $testName = if ($selectedProvider -eq 'minimax') { 'TestHarnessLiveMiniMax' } else { 'TestHarnessLiveConfiguredProvider' }
        $pattern = if ($Role -eq 'all') { '^' + $testName + '$' } else { '^' + $testName + '$/' + $Role + '$' }
        & $goCommand test ./internal/automationagent -run $pattern -count=1 -v -timeout 12m 2>&1 | Tee-Object -FilePath (Join-Path $reportDirectory 'live.log')
        $testExit = $LASTEXITCODE
        if ($ScoreSemantics -and (Test-Path -LiteralPath $env:ITBEM_AGENT_LIVE_REPORT -PathType Leaf)) {
            $scorePath = Join-Path $reportDirectory 'semantic-score.json'
            $scoreArgs = @('-NoProfile', '-File', $scorerPath, '-ReportPath', $env:ITBEM_AGENT_LIVE_REPORT, '-OutputPath', $scorePath)
            if ($AllowSemanticFailures) { $scoreArgs += '-AllowFailures' }
            & $pwshCommand @scoreArgs 2>&1 | Tee-Object -FilePath (Join-Path $reportDirectory 'semantic-score.log')
            $semanticExit = $LASTEXITCODE
            Write-Host "Semantic score artifact: $scorePath"
        } elseif ($ScoreSemantics) {
            throw 'Live harness did not produce a report; semantic scoring was not run.'
        } else {
            $semanticExit = 0
        }
    } else {
        $offlinePath = Join-Path $reportDirectory 'offline.jsonl'
        & $goCommand test ./internal/automationagent ./controllers/delivery ./controllers/automation ./services/automationcost ./services/deliveryworkflow ./internal/runtimeroute -shuffle=49207 -count=3 -timeout 180s -json 2>&1 | Tee-Object -FilePath $offlinePath
        $testExit = $LASTEXITCODE
        $semanticExit = 0
        # Keep the operator-facing result aligned with the auditable JSONL. The
        # test stream deliberately contains `cont`/`pause` records for
        # re-runnable cases; those are not failures and must not be conflated
        # with a green package result. Never make a malformed log look green.
        try {
            $offlineRows = @(Get-Content -LiteralPath $offlinePath -ErrorAction Stop | ForEach-Object {
                if (-not $_.Trim()) { return }
                $_ | ConvertFrom-Json -ErrorAction Stop
            })
            $offlineSummary = [ordered]@{
                pass = @($offlineRows | Where-Object Action -eq 'pass').Count
                fail = @($offlineRows | Where-Object Action -eq 'fail').Count
                skip = @($offlineRows | Where-Object Action -eq 'skip').Count
                pause = @($offlineRows | Where-Object Action -eq 'pause').Count
                continuation = @($offlineRows | Where-Object Action -eq 'cont').Count
            }
            $summaryJSON = $offlineSummary | ConvertTo-Json -Compress
            Write-Host "Offline harness summary: $summaryJSON"
            if ($offlineSummary.fail -gt 0) { $testExit = 1 }
        } catch {
            Write-Error "Offline harness output could not be summarized safely: $($_.Exception.Message)"
            $testExit = 1
        }
    }
    Write-Host "Evaluation artifacts: $reportDirectory"
    if ($testExit -ne 0) { throw "Harness evaluation failed (exit $testExit); inspect the saved evidence." }
    if ($semanticExit -ne 0 -and -not $AllowSemanticFailures) { throw "Semantic evaluation failed (exit $semanticExit); inspect the saved evidence." }
} finally {
    foreach ($name in $trackedNames) { [Environment]::SetEnvironmentVariable($name,$previous[$name],'Process') }
    Pop-Location
}
