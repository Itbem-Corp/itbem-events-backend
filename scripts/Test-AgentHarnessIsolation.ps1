[CmdletBinding()]
param([string]$RealGoPath = '')

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
    $runnerPath = Join-Path $scriptRoot 'Invoke-IsolationProbe.ps1'
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
    $latestReport = Get-ChildItem -LiteralPath (Join-Path $backendRoot '.local/harness-evals') -Directory -ErrorAction SilentlyContinue | Sort-Object Name -Descending | Select-Object -First 1
    $summary = $null
    if ($latestReport) {
        $summaryPath = Join-Path $latestReport.FullName 'offline-summary.json'
        if (Test-Path -LiteralPath $summaryPath) { $summary = Get-Content -LiteralPath $summaryPath -Raw | ConvertFrom-Json }
    }
    [pscustomobject]@{ ExitCode = $process.ExitCode; Output = $stdout; Error = $stderr; Summary = $summary; Restored = (Test-Path -LiteralPath ($CapturePath + '.restored')) }
}

try {
    $null = New-Item -ItemType Directory -Path $scriptRoot -Force
    $null = New-Item -ItemType Directory -Path $fakeBin -Force
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'Test-AgentHarness.ps1') -Destination (Join-Path $scriptRoot 'Test-AgentHarness.ps1')

    # Check the runner's process after success and failure as well as the Go
    # child. Credential values in this isolated process are synthetic.
    $probeSource = @'
$ErrorActionPreference = 'Stop'
$tracked = @('PATH','GOTOOLCHAIN','GOPROXY','ITBEM_AGENT_LIVE_EVAL',
    'ITBEM_AI_PROVIDER','ITBEM_AI_GATEWAY_URL','ITBEM_AGENT_LIVE_PROVIDER',
    'MINIMAX_API_KEY','MINIMAX_MODEL','MINIMAX_API_BASE_URL',
    'OPENAI_API_KEY','OPENAI_MODEL','OPENAI_API_BASE_URL',
    'DEEPSEEK_API_KEY','DEEPSEEK_MODEL','DEEPSEEK_API_BASE_URL',
    'OPENROUTER_API_KEY','OPENROUTER_MODEL','OPENROUTER_API_BASE_URL',
    'ANTHROPIC_API_KEY','ANTHROPIC_MODEL','ANTHROPIC_API_BASE_URL',
    'OPENCODE_GO_API_KEY','OPENCODE_GO_MODEL','OPENCODE_GO_API_BASE_URL',
    'ITBEM_AGENT_LIVE_REPORT','ITBEM_AGENT_LIVE_PRIOR_REPORT')
$before = @{}
foreach ($name in $tracked) { $before[$name] = [Environment]::GetEnvironmentVariable($name,'Process') }
$failed = $false
try { & (Join-Path $PSScriptRoot 'Test-AgentHarness.ps1') @args } catch { Write-Host $_; $failed = $true }
foreach ($name in $tracked) {
    if ([Environment]::GetEnvironmentVariable($name,'Process') -cne $before[$name]) {
        throw "Harness did not restore its process environment: $name"
    }
}
[IO.File]::WriteAllText(($env:ITBEM_AGENT_HARNESS_CAPTURE + '.restored'), 'restored')
if ($failed) { exit 1 }
'@
    [IO.File]::WriteAllText((Join-Path $scriptRoot 'Invoke-IsolationProbe.ps1'), $probeSource)

    $fixtureModule = 'harness-fixture'
    Set-Content -LiteralPath (Join-Path $backendRoot 'go.mod') -Value @(('module ' + $fixtureModule), 'go 1.25.0')
    $runnerSource = Get-Content -LiteralPath (Join-Path $scriptRoot 'Test-AgentHarness.ps1') -Raw
    $packageDeclaration = [regex]::Match($runnerSource, '\$testPackages\s*=\s*@\((?<packages>[^)]*)\)')
    $fixturePackages = @([regex]::Matches($packageDeclaration.Groups['packages'].Value, "'([^']+)'") | ForEach-Object { $_.Groups[1].Value -replace '^\./', '' })
    if (-not $packageDeclaration.Success -or $fixturePackages.Count -eq 0) { throw 'Cannot read the canonical harness package list.' }
    $goLines = @('@echo off')
    for ($index = 0; $index -lt $allProbeNames.Count; $index++) {
        $redirect = if ($index -eq 0) { '>' } else { '>>' }
        $name = $allProbeNames[$index]
        $goLines += $redirect + '"%ITBEM_AGENT_HARNESS_CAPTURE%" echo ' + $name + '=%' + $name + '%'
    }
    $goLines += 'if "%ITBEM_AGENT_HARNESS_STREAM%"=="missing-package" echo {"Action":"pass"}'
    $goLines += 'if "%ITBEM_AGENT_HARNESS_STREAM%"=="missing-package" exit /b 0'
    $goLines += 'if "%ITBEM_AGENT_HARNESS_STREAM%"=="empty" exit /b 0'
    foreach ($package in $fixturePackages) {
        $goLines += 'if not "%ITBEM_AGENT_HARNESS_STREAM%"=="no-tests" echo {"Action":"pass","Package":"events-stocks/' + $package + '","Test":"TestSynthetic"}'
        if ($package -eq 'internal/runtimeroute') {
            $goLines += 'if "%ITBEM_AGENT_HARNESS_STREAM%"=="truncated" exit /b 0'
        }
        if ($package -eq 'internal/runtimeroute') {
            $goLines += 'if "%ITBEM_AGENT_HARNESS_STREAM%"=="skipped-package" echo {"Action":"skip","Package":"events-stocks/internal/runtimeroute"}'
            $goLines += 'if "%ITBEM_AGENT_HARNESS_STREAM%"=="failed-package" echo {"Action":"fail","Package":"events-stocks/internal/runtimeroute"}'
            $goLines += 'if not "%ITBEM_AGENT_HARNESS_STREAM%"=="skipped-package" if not "%ITBEM_AGENT_HARNESS_STREAM%"=="failed-package" echo {"Action":"pass","Package":"events-stocks/internal/runtimeroute"}'
        } else {
            $goLines += 'echo {"Action":"pass","Package":"events-stocks/' + $package + '"}'
        }
    }
    $goLines += 'if "%ITBEM_AGENT_HARNESS_STREAM%"=="duplicate" echo {"Action":"pass","Package":"events-stocks/internal/runtimeroute"}'
    $goLines += 'if "%ITBEM_AGENT_HARNESS_STREAM%"=="failed" echo {"Action":"fail","Package":"events-stocks/internal/runtimeroute","Test":"TestSyntheticFailure"}'
    $goLines += 'if "%ITBEM_AGENT_HARNESS_STREAM%"=="malformed" echo invalid-json'
    $goLines += 'if "%ITBEM_AGENT_HARNESS_STREAM%"=="skipped" echo {"Action":"skip","Package":"events-stocks/internal/runtimeroute","Test":"TestSyntheticIntegration"}'
    $goLines += 'if "%ITBEM_AGENT_HARNESS_STREAM%"=="skipped" echo {"Action":"skip","Package":"events-stocks/internal/runtimeroute","Test":"TestSyntheticIntegration"}'
    $goLines += 'if "%ITBEM_AGENT_HARNESS_STREAM%"=="unknown-action" echo {"Action":"green","Package":"events-stocks/internal/runtimeroute"}'
    $goLines += 'exit /b 0'
    $goLines = @($goLines | ForEach-Object { $_.Replace('events-stocks/', $fixtureModule + '/') })
    [IO.File]::WriteAllLines((Join-Path $fakeBin 'go.cmd'), [string[]]$goLines, [Text.Encoding]::ASCII)

    $isolatedCapture = Join-Path $testRoot 'offline-child-environment.txt'
    $offline = Invoke-HarnessChild -HarnessArguments @() -CapturePath $isolatedCapture
    if ($offline.ExitCode -ne 0 -or -not $offline.Restored -or -not (Test-Path -LiteralPath $isolatedCapture -PathType Leaf)) {
        throw 'Offline runner did not complete through the fake Go command and restore its environment.'
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
    $strictPass = Invoke-HarnessChild -HarnessArguments @('-RequireNoSkips') -CapturePath (Join-Path $testRoot 'strict-pass.txt')
    if ($strictPass.ExitCode -ne 0 -or -not $strictPass.Restored -or -not $strictPass.Summary.passed -or $strictPass.Summary.test_executions_passed -ne $fixturePackages.Count -or $strictPass.Summary.distinct_tests_passed -ne $fixturePackages.Count) {
        throw 'Strict gate did not approve complete no-skip evidence with separate test counters.'
    }

    $previousStream = [Environment]::GetEnvironmentVariable('ITBEM_AGENT_HARNESS_STREAM','Process')
    try {
        $env:ITBEM_AGENT_HARNESS_STREAM = 'skipped'
        $skipped = Invoke-HarnessChild -HarnessArguments @() -CapturePath (Join-Path $testRoot 'skipped.txt')
        if ($skipped.ExitCode -ne 0 -or -not $skipped.Restored -or -not $skipped.Summary.passed -or @($skipped.Summary.skipped_tests).Count -ne 1 -or $skipped.Summary.skip -ne 2) {
            throw 'Default offline policy did not preserve distinct skipped-test evidence.'
        }
        $strict = Invoke-HarnessChild -HarnessArguments @('-RequireNoSkips') -CapturePath (Join-Path $testRoot 'strict.txt')
        if ($strict.ExitCode -eq 0 -or -not $strict.Restored -or $strict.Summary.passed -or -not $strict.Summary.require_no_skips -or $strict.Summary.skipped_tests[0].Test -ne 'TestSyntheticIntegration') {
            throw 'Strict no-skip gate did not reject omitted integration evidence.'
        }
        foreach ($stream in @('empty','truncated','no-tests','duplicate','failed','malformed','missing-package','unknown-action','skipped-package','failed-package')) {
            $env:ITBEM_AGENT_HARNESS_STREAM = $stream
            $incomplete = Invoke-HarnessChild -HarnessArguments @() -CapturePath (Join-Path $testRoot ($stream + '.txt'))
            if ($incomplete.ExitCode -eq 0 -or -not $incomplete.Restored -or (($incomplete.Output + $incomplete.Error) -notmatch 'Offline harness output could not be summarized safely|Harness evaluation failed')) {
                throw "Incomplete $stream test evidence did not fail closed."
            }
            if ($stream -in @('truncated','no-tests','duplicate') -and @($incomplete.Summary.package_issues).Count -eq 0) {
                throw "Incomplete $stream evidence did not retain package diagnostics."
            }
            if ($stream -eq 'failed' -and (@($incomplete.Summary.failed_tests).Count -ne 1 -or $incomplete.Summary.failed_tests[0].Test -ne 'TestSyntheticFailure')) {
                throw 'Failed-test summary did not retain the failing test identity.'
            }
            if ($incomplete.Summary -and $incomplete.Summary.passed) {
                throw 'An invalid evidence stream retained a successful summary verdict.'
            }
        }
    } finally {
        if ($null -eq $previousStream) {
            Remove-Item -LiteralPath Env:ITBEM_AGENT_HARNESS_STREAM -ErrorAction SilentlyContinue
        } else {
            [Environment]::SetEnvironmentVariable('ITBEM_AGENT_HARNESS_STREAM',$previousStream,'Process')
        }
    }

    if ($RealGoPath) {
        $resolvedGo = (Resolve-Path -LiteralPath $RealGoPath -ErrorAction Stop).Path
        foreach ($package in $fixturePackages) {
            $packageRoot = Join-Path $backendRoot $package
            $null = New-Item -ItemType Directory -Path $packageRoot -Force
            Set-Content -LiteralPath (Join-Path $packageRoot 'fixture_test.go') -Value @('package fixture', 'import "testing"', 'func TestRealGoEvidence(t *testing.T) {}')
        }
        [IO.File]::WriteAllLines((Join-Path $fakeBin 'go.cmd'), [string[]]@('@echo off', ('"' + $resolvedGo + '" %*'), 'exit /b %errorlevel%'), [Text.Encoding]::ASCII)
        $realCapture = Join-Path $testRoot 'real-go-environment.txt'
        $real = Invoke-HarnessChild -HarnessArguments @() -CapturePath $realCapture
        if ($real.ExitCode -ne 0) { throw ('Real Go JSON compatibility failed: ' + $real.Output + $real.Error) }
    }

    foreach ($legacyArguments in @(@('-LiveMiniMax'), @('-LiveProvider','openai'))) {
        $legacyCapture = Join-Path $testRoot ([guid]::NewGuid().ToString('N') + '.txt')
        $legacy = Invoke-HarnessChild -HarnessArguments $legacyArguments -CapturePath $legacyCapture
        if ($legacy.ExitCode -eq 0 -or (($legacy.Output + $legacy.Error) -notmatch 'retired') -or (Test-Path -LiteralPath $legacyCapture)) {
            throw 'A retired live selector reached the Go test process.'
        }
    }

    'Offline environment scrubbed; retired live selectors blocked; ten invalid streams rejected; strict skip policy verified.'
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
