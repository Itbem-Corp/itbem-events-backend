$ErrorActionPreference = 'Stop'
$root = Join-Path ([IO.Path]::GetTempPath()) ('harness-costs-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $root | Out-Null
try {
    function Write-Fixture([string]$name, [string]$text) {
        [IO.File]::WriteAllText((Join-Path $root $name), $text, [Text.UTF8Encoding]::new($false))
    }
    Write-Fixture 'run.json' '{"calls":[{"role":"qa","estimated_api_equivalent_microusd":0}],"reserved_upper_bound_microusd":0}'
    $report = & "$PSScriptRoot/Report-HarnessCosts.ps1" -HarnessRoot $root
    if ($report -notmatch '\$0\.000000' -or $report -match 'Unknown \(') { throw 'Recorded zero was lost.' }
    Write-Fixture 'missing.json' '{"calls":[{"role":"qa"}]}'
    Write-Fixture 'broken.json' '{"calls":['
    Write-Fixture 'other.json' '{"not_a_run":true}'
    $report = & "$PSScriptRoot/Report-HarnessCosts.ps1" -HarnessRoot $root
    if ($report -notmatch '4 JSON files scanned; 2 runs parsed; 1 malformed files excluded; 1 non-run files excluded' -or $report -notmatch 'known subtotal \(1/2 recorded\)') { throw 'Incomplete coverage was hidden.' }
    Write-Fixture 'run.json' '{"calls":[{"role":"qa"}]}'
    $report = & "$PSScriptRoot/Report-HarnessCosts.ps1" -HarnessRoot $root
    if ($report -notmatch 'Unknown \(0/2 recorded\)' -or $report -match '\$0\.000000') { throw 'Missing amounts became zero.' }
    foreach ($value in @('-1', '1.5', 'true', '9223372036854775808')) {
        Write-Fixture 'run.json' ('{"calls":[{"estimated_api_equivalent_microusd":' + $value + '}]}')
        $rejected = $false
        try { $null = & "$PSScriptRoot/Report-HarnessCosts.ps1" -HarnessRoot $root } catch { $rejected = $_.Exception.Message -match 'nonnegative Int64 integer' }
        if (-not $rejected) { throw "Invalid cost accepted: $value" }
    }
    Write-Output 'Harness cost coverage and unknown-value checks passed.'
    $global:LASTEXITCODE = 0
} finally {
    # This exact directory was created above under the system temporary directory.
    $resolved = [IO.Path]::GetFullPath($root)
    $temporaryRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not $resolved.StartsWith($temporaryRoot, [StringComparison]::OrdinalIgnoreCase)) { throw 'Temporary cleanup escaped its root.' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
