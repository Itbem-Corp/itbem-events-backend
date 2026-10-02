[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('itbem-agent-harness-isolation-' + [guid]::NewGuid().ToString('N'))
$backendRoot = Join-Path $testRoot 'workspace/backend'
$scriptRoot = Join-Path $backendRoot 'scripts'
$fakeBin = Join-Path $testRoot 'fake-bin'
$providerNames = @(
    'ITBEM_AI_PROVIDER','ITBEM_AI_GATEWAY_URL','ITBEM_AGENT_LIVE_PROVIDER',
    'MINIMAX_API_KEY','MINIMAX_MODEL','MINIMAX_API_BASE_URL',
    'OPENAI_API_KEY','OPENAI_MODEL','OPENAI_API_BASE_URL',
    'DEEPSEEK_API_KEY','DEEPSEEK_MODEL','DEEPSEEK_API_BASE_URL',
    'OPENROUTER_API_KEY','OPENROUTER_MODEL','OPENROUTER_API_BASE_URL',
    'ANTHROPIC_API_KEY','ANTHROPIC_MODEL','ANTHROPIC_API_BASE_URL',
    'OPENCODE_GO_API_KEY','OPENCODE_GO_MODEL','OPENCODE_GO_API_BASE_URL',
    'ITBEM_AGENT_LIVE_REPORT','ITBEM_AGENT_LIVE_PRIOR_REPORT'
)
$allProbeNames = @('ITBEM_AGENT_LIVE_EVAL') + $providerNames

function Invoke-HarnessChild([string[]]$HarnessArguments, [string]$CapturePath) {
    $shellPath = [Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
    $runnerPath = Join-Path $scriptRoot 'Test-AgentHarness.ps1'
    $argumentText = '-NoProfile -ExecutionPolicy Bypass -File "' + $runnerPath + '"'
    foreach ($argument in $HarnessArguments) { $argumentText += ' "' + $argument.Replace('"','\"') + '"' }

    $startInfo = New-Object Diagnostics.ProcessStartInfo
    $startInfo.FileName = $shellPath
    $startInfo.Arguments = $argumentText
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true

    # Remove inherited values without inspecting them. Synthetic sentinels let
    # the fake Go command prove the runner clears each name before test launch.
    foreach ($name in $allProbeNames) { [void]$startInfo.EnvironmentVariables.Remove($name) }
    foreach ($name in $providerNames) { $startInfo.EnvironmentVariables[$name] = 'HARNESS_ISOLATION_SENTINEL' }
    $startInfo.EnvironmentVariables['ITBEM_AGENT_LIVE_EVAL'] = '1'
    $startInfo.EnvironmentVariables['ITBEM_AGENT_HARNESS_CAPTURE'] = $CapturePath
    $startInfo.EnvironmentVariables['PATH'] = $fakeBin + ';' + $PSHOME + ';' + $env:SystemRoot + '\System32'

    $process = [Diagnostics.Process]::Start($startInfo)
    $stdout = $process.StandardOutput.ReadToEnd()
    $stderr = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    [pscustomobject]@{ ExitCode = $process.ExitCode; Output = $stdout; Error = $stderr }
}

try {
    $null = New-Item -ItemType Directory -Path $scriptRoot -Force
    $null = New-Item -ItemType Directory -Path $fakeBin -Force
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'Test-AgentHarness.ps1') -Destination (Join-Path $scriptRoot 'Test-AgentHarness.ps1')
    Set-Content -LiteralPath (Join-Path $backendRoot 'go.mod') -Value 'module harness-fixture'

    $goLines = @('@echo off')
    for ($index = 0; $index -lt $allProbeNames.Count; $index++) {
        $redirect = if ($index -eq 0) { '>' } else { '>>' }
        $name = $allProbeNames[$index]
        $goLines += $redirect + '"%ITBEM_AGENT_HARNESS_CAPTURE%" echo ' + $name + '=%' + $name + '%'
    }
    $captureLines = @($goLines)
    $fixturePackages = @('internal/automationagent','controllers/delivery','controllers/automation','services/automationcost','services/deliveryworkflow','internal/runtimeroute')
    foreach ($package in $fixturePackages) {
        $goLines += 'echo {"Action":"pass","Package":"harness-fixture/' + $package + '","Test":"TestExecuted"}'
        $goLines += 'echo {"Action":"pass","Package":"harness-fixture/' + $package + '"}'
    }
    $successfulLines = @($goLines)
    $goLines += 'exit /b 0'
    [IO.File]::WriteAllLines((Join-Path $fakeBin 'go.cmd'), [string[]]$goLines, [Text.Encoding]::ASCII)

    $isolatedCapture = Join-Path $testRoot 'offline-child-environment.txt'
    $offline = Invoke-HarnessChild -HarnessArguments @() -CapturePath $isolatedCapture
    if ($offline.ExitCode -ne 0 -or -not (Test-Path -LiteralPath $isolatedCapture -PathType Leaf)) {
        throw 'Offline runner did not complete through the fake Go command.'
    }
    $capturedLines = @(Get-Content -LiteralPath $isolatedCapture)
    foreach ($name in $allProbeNames) {
        $expectedValue = if ($name -eq 'ITBEM_AGENT_LIVE_EVAL') { '0' } else { '' }
        if ($capturedLines -notcontains ($name + '=' + $expectedValue)) {
            throw 'Offline child environment was not scrubbed for a provider/live setting.'
        }
    }
    if ($capturedLines -match 'HARNESS_ISOLATION_SENTINEL') {
        throw 'Offline child received a synthetic provider sentinel.'
    }

    $invalidStreams = [ordered]@{
        empty = @()
        missingPackage = @('echo {"Action":"pass"}')
        truncated = @($successfulLines | Select-Object -SkipLast 1)
        noExecutedTests = @($successfulLines | Where-Object { $_ -notmatch 'TestExecuted' })
        failedTest = @($successfulLines) + @('echo {"Action":"fail","Package":"harness-fixture/internal/automationagent","Test":"TestFailure"}')
        duplicateTerminal = @($successfulLines) + @('echo {"Action":"pass","Package":"harness-fixture/internal/automationagent"}')
        malformed = @('echo malformed-json')
    }
    foreach ($streamName in $invalidStreams.Keys) {
        $invalidStream = $invalidStreams[$streamName]
        $invalidLines = @($captureLines) + @($invalidStream) + @('exit /b 0')
        [IO.File]::WriteAllLines((Join-Path $fakeBin 'go.cmd'), [string[]]$invalidLines, [Text.Encoding]::ASCII)
        $invalidCapture = Join-Path $testRoot ([guid]::NewGuid().ToString('N') + '.txt')
        $invalid = Invoke-HarnessChild -HarnessArguments @() -CapturePath $invalidCapture
        if ($invalid.ExitCode -eq 0) { throw "Invalid test stream '$streamName' was accepted despite exit zero." }
    }

    foreach ($legacyArguments in @(@('-LiveMiniMax'), @('-LiveProvider','openai'))) {
        $legacyCapture = Join-Path $testRoot ([guid]::NewGuid().ToString('N') + '.txt')
        $legacy = Invoke-HarnessChild -HarnessArguments $legacyArguments -CapturePath $legacyCapture
        if ($legacy.ExitCode -eq 0 -or (($legacy.Output + $legacy.Error) -notmatch 'retired') -or (Test-Path -LiteralPath $legacyCapture)) {
            throw 'A retired live selector reached the Go test process.'
        }
    }

    'Offline child environment scrubbed; incomplete test evidence rejected; retired live selectors failed before Go launch.'
} finally {
    if (Test-Path -LiteralPath $testRoot -PathType Container) {
        $resolvedRoot = [IO.Path]::GetFullPath((Resolve-Path -LiteralPath $testRoot).Path)
        $resolvedTemp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
        if (-not $resolvedRoot.StartsWith($resolvedTemp, [StringComparison]::OrdinalIgnoreCase)) {
            throw 'Refusing to remove a harness test directory outside the system temporary directory.'
        }
        Remove-Item -LiteralPath $resolvedRoot -Recurse -Force
    }
}
