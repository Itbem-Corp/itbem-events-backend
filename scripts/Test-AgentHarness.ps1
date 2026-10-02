[CmdletBinding()]
param(
    # Kept only to fail old invocations closed with an actionable message.
    [switch]$LiveMiniMax,
    [string]$LiveProvider = '',
    [switch]$ScoreSemantics,
    [switch]$AllowSemanticFailures,
    [string]$ScoreReportPath = '',
    [string]$PriorReport = '',
    [string]$Role = 'all',
    [string]$CredentialSource = ''
)

$ErrorActionPreference = 'Stop'
$liveProviderRequested = -not [string]::IsNullOrWhiteSpace($LiveProvider)
if ($LiveMiniMax -or $liveProviderRequested) {
    throw 'Paid live-provider harness mode is retired because it called providers directly. No credentials were loaded and no request was sent. Use the offline simulated test suite, or evaluate a real authorized agent run through the central gateway.'
}
if ($PriorReport -or $Role -ne 'all' -or $CredentialSource) {
    throw 'PriorReport, Role, and CredentialSource were live-harness options and are no longer supported. Replay a saved report with -ScoreReportPath.'
}
$repoRoot = Split-Path $PSScriptRoot -Parent
$pwshCommand = (Get-Command pwsh -ErrorAction SilentlyContinue).Source
if (-not $pwshCommand) { $pwshCommand = (Get-Command powershell -ErrorAction SilentlyContinue).Source }
if (-not $pwshCommand) { throw 'PowerShell executable unavailable; semantic scoring cannot be invoked.' }
$scorerPath = Join-Path $repoRoot 'scripts/Score-HarnessSemantics.ps1'
if (($ScoreSemantics -or $ScoreReportPath) -and -not (Test-Path -LiteralPath $scorerPath -PathType Leaf)) {
    throw "Semantic scorer unavailable: $scorerPath"
}
if ($ScoreSemantics -and -not $ScoreReportPath) {
    throw 'Live semantic scoring has been retired. Supply -ScoreReportPath to replay an existing report.'
}
if ($AllowSemanticFailures -and -not ($ScoreSemantics -or $ScoreReportPath)) {
    throw '-AllowSemanticFailures requires semantic scoring.'
}
$goCandidate = Join-Path (Split-Path $repoRoot -Parent) '.local/toolchains/go1.25.12/bin/go.exe'
$goCommand = if (Test-Path -LiteralPath $goCandidate) { $goCandidate } else { (Get-Command go).Source }
$reportDirectory = Join-Path $repoRoot ('.local/harness-evals/' + (Get-Date -Format 'yyyyMMdd-HHmmss-fff'))
$null = New-Item -ItemType Directory -Path $reportDirectory
$providerEnvironmentNames = @(
    'ITBEM_AI_PROVIDER','ITBEM_AI_GATEWAY_URL','ITBEM_AGENT_LIVE_PROVIDER',
    'MINIMAX_API_KEY','MINIMAX_MODEL','MINIMAX_API_BASE_URL',
    'OPENAI_API_KEY','OPENAI_MODEL','OPENAI_API_BASE_URL',
    'DEEPSEEK_API_KEY','DEEPSEEK_MODEL','DEEPSEEK_API_BASE_URL',
    'OPENROUTER_API_KEY','OPENROUTER_MODEL','OPENROUTER_API_BASE_URL',
    'ANTHROPIC_API_KEY','ANTHROPIC_MODEL','ANTHROPIC_API_BASE_URL',
    'OPENCODE_GO_API_KEY','OPENCODE_GO_MODEL','OPENCODE_GO_API_BASE_URL',
    'ITBEM_AGENT_LIVE_REPORT','ITBEM_AGENT_LIVE_PRIOR_REPORT'
)
$trackedNames = @('PATH','GOTOOLCHAIN','GOPROXY','ITBEM_AGENT_LIVE_EVAL') + $providerEnvironmentNames
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
    foreach ($name in $providerEnvironmentNames) { [Environment]::SetEnvironmentVariable($name,'','Process') }
    $offlinePath = Join-Path $reportDirectory 'offline.jsonl'
    $null = New-Item -ItemType File -Path $offlinePath
    $harnessPackages = @('internal/automationagent','controllers/delivery','controllers/automation','services/automationcost','services/deliveryworkflow','internal/runtimeroute')
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
        # Exit zero alone cannot prove execution: require one terminal pass
        # and at least one executed test for every requested package. A
        # truncated, empty, skipped-only or unexpected stream fails closed.
        if ($offlineRows.Count -eq 0) { throw 'Empty harness test stream.' }
        $moduleLine = @(Get-Content -LiteralPath (Join-Path $repoRoot 'go.mod') | Where-Object { $_ -match '^module\s+\S+\s*$' })
        if ($moduleLine.Count -ne 1) { throw 'Cannot identify harness module.' }
        $moduleName = ($moduleLine[0] -replace '^module\s+', '').Trim()
        $expectedPackages = @($harnessPackages | ForEach-Object { $moduleName + '/' + $_ })
        foreach ($row in $offlineRows) {
            if ($row.Package -notin $expectedPackages -or $row.Action -notin @('start','run','pause','cont','pass','fail','skip','output','bench')) {
                throw 'Unexpected harness test event or package.'
            }
        }
        foreach ($package in $expectedPackages) {
            $terminalRows = @($offlineRows | Where-Object { $_.Package -eq $package -and -not $_.Test -and $_.Action -in @('pass','fail','skip') })
            $executedTests = @($offlineRows | Where-Object { $_.Package -eq $package -and $_.Test -and $_.Action -eq 'pass' })
            if ($terminalRows.Count -ne 1 -or $terminalRows[0].Action -ne 'pass' -or $executedTests.Count -eq 0) {
                throw "Missing successful execution evidence for $package."
            }
        }
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
    Write-Host "Evaluation artifacts: $reportDirectory"
    if ($testExit -ne 0) { throw "Harness evaluation failed (exit $testExit); inspect the saved evidence." }
    if ($semanticExit -ne 0 -and -not $AllowSemanticFailures) { throw "Semantic evaluation failed (exit $semanticExit); inspect the saved evidence." }
} finally {
    foreach ($name in $trackedNames) { [Environment]::SetEnvironmentVariable($name,$previous[$name],'Process') }
    Pop-Location
}
