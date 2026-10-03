[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('itbem-semantic-regression-' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $testRoot
$scorer = Join-Path $PSScriptRoot 'Score-HarnessSemantics.ps1'
$shellPath = (Get-Command pwsh -ErrorAction SilentlyContinue).Source
if (-not $shellPath) { $shellPath = (Get-Command powershell).Source }
$caseCount = 0
$answers = [ordered]@{
    reviewer_seeded_auth_bypass = @{ verdict = 'request_changes'; findings = @(@{ file = 'auth.go'; category = 'security'; severity = 'high'; line_start = 3; line_end = 3; side = 'head'; evidence_quote = 'return true' }) }
    qa_observed_failure = @{ verdict = 'failed'; checks = @(@{ status = 'failed' }) }
    delivery_grounded_summary = @{ executive = @{ risks = @('Pending human review') }; technical = @{ evidence = @('Recorded test') } }
    product_bounded_options = @{ directions = @(@{ risk = 'Risk A' }, @{ risk = 'Risk B' }); recommendation = @{ direction = 'A' } }
    planner_missing_context = @{ context_gaps = @('Repository missing'); files_impacted = @() }
    implementer_executable_acceptance = @{ action = 'edit'; repository_ref = 'workspace://repo'; path = 'note.go'; content = 'package note' }
}
function New-Report {
    $calls = @()
    foreach ($role in $answers.Keys) { $calls += @{ role = $role; completion = @{ content = ($answers[$role] | ConvertTo-Json -Depth 10 -Compress) } } }
    return @{ synthetic_only = $true; calls = $calls; outcomes = @{ implementer_executable_acceptance = $true } }
}
function Assert-Score([string]$Name, $Report, [bool]$ExpectedPass, [string[]]$Options = @()) {
    $inputPath = Join-Path $testRoot ($Name + '.json')
    $outputPath = Join-Path $testRoot ($Name + '-score.json')
    $Report | ConvertTo-Json -Depth 15 | Set-Content -LiteralPath $inputPath -Encoding UTF8
    $null = & $shellPath -NoProfile -File $scorer -ReportPath $inputPath -OutputPath $outputPath @Options 2>&1
    $exitCode = $LASTEXITCODE
    if (-not (Test-Path -LiteralPath $outputPath)) { throw "Scorer did not retain evidence for $Name." }
    $score = Get-Content -LiteralPath $outputPath -Raw | ConvertFrom-Json
    if ($score.passed -ne $ExpectedPass) { throw "Incorrect semantic verdict for $Name." }
    $expectedExit = if ($ExpectedPass -or $Options -contains '-AllowFailures') { 0 } else { 1 }
    if ($exitCode -ne $expectedExit) { throw "Incorrect scorer exit for $Name." }
    $script:caseCount++
}
try {
    Assert-Score 'valid' (New-Report) $true
    foreach ($marker in @('true', 1, $false, $null)) {
        $report = New-Report
        $report.synthetic_only = $marker
        $inputPath = Join-Path $testRoot ('invalid-marker-' + [guid]::NewGuid().ToString('N') + '.json')
        $outputPath = $inputPath + '-score.json'
        $report | ConvertTo-Json -Depth 15 | Set-Content -LiteralPath $inputPath -Encoding UTF8
        $null = & $shellPath -NoProfile -File $scorer -ReportPath $inputPath -OutputPath $outputPath 2>&1
        if ($LASTEXITCODE -eq 0 -or (Test-Path -LiteralPath $outputPath)) { throw 'An invalid synthetic marker was scored.' }
        $caseCount++
    }
    foreach ($role in $answers.Keys) {
        $arrayAnswer = '[' + ($answers[$role] | ConvertTo-Json -Depth 10 -Compress) + ']'
        foreach ($content in @('', 'not-json', 'null', '[]', 'true', '"text"', $arrayAnswer)) {
            $report = New-Report
            ($report.calls | Where-Object role -eq $role).completion.content = $content
            Assert-Score ($role + '-' + [guid]::NewGuid().ToString('N')) $report $false
        }
    }
    $report = New-Report
    $report.calls += @{ role = 'qa_observed_failure'; completion = @{ content = 'not-json' } }
    Assert-Score 'latest-invalid' $report $false
    foreach ($value in @('false','true',1,$false,$null)) {
        $report = New-Report
        $report.outcomes.implementer_executable_acceptance = $value
        Assert-Score ('outcome-' + [guid]::NewGuid().ToString('N')) $report $false
    }
    $report = New-Report
    $report.calls = @($report.calls | Where-Object role -eq 'qa_observed_failure')
    Assert-Score 'partial-valid' $report $true @('-AllowPartial')
    $report.calls[0].completion.content = 'not-json'
    Assert-Score 'partial-invalid' $report $false @('-AllowPartial')
    Assert-Score 'allowed-failure' $report $false @('-AllowPartial','-AllowFailures')
    $report.calls = @()
    Assert-Score 'empty-partial' $report $false @('-AllowPartial')
    "Semantic regressions passed: $caseCount cases with synthetic reports."
} finally {
    $resolvedRoot = [IO.Path]::GetFullPath($testRoot)
    $resolvedTemp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    if (-not $resolvedRoot.StartsWith($resolvedTemp, [StringComparison]::OrdinalIgnoreCase)) { throw 'Refusing cleanup outside the temporary directory.' }
    Remove-Item -LiteralPath $resolvedRoot -Recurse -Force
}

# GitHub Actions propagates LASTEXITCODE from native commands after a script
# returns. Negative scorer cases deliberately set it to 1; reaching here means
# every expected verdict and exit code was verified successfully.
$global:LASTEXITCODE = 0
