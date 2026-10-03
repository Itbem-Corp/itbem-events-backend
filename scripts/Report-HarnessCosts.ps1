[CmdletBinding()]
param(
    [string]$HarnessRoot = '',
    [string]$OutputPath
)

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($HarnessRoot)) {
    $HarnessRoot = Join-Path $PSScriptRoot '..\.local\harness-evals'
}
$calls = @()
$runs = @()
$scanned = 0
$malformed = 0
$nonRuns = 0

function Get-Int64Property($object, [string]$name) {
    if ($null -eq $object -or -not ($object.PSObject.Properties.Name -contains $name)) {
        return $null
    }
    $value = $object.$name
    if ($null -eq $value -or [string]::IsNullOrWhiteSpace([string]$value)) {
        return $null
    }
    $parsed = [int64]0
    if ([string]$value -notmatch '^\d+$' -or -not [int64]::TryParse([string]$value, [ref]$parsed)) {
        throw "Cost field '$name' must be a nonnegative Int64 integer."
    }
    return $parsed
}

foreach ($file in Get-ChildItem -LiteralPath $HarnessRoot -Recurse -Filter '*.json' -File) {
    $scanned++
    try {
        $report = Get-Content -LiteralPath $file.FullName -Raw | ConvertFrom-Json
    } catch {
        $malformed++
        continue
    }

    if ($null -eq $report -or -not ($report.PSObject.Properties.Name -contains 'calls')) { $nonRuns++; continue }
    if ($null -eq $report.calls -or $report.calls -isnot [array]) { throw 'Run calls must be an explicit JSON array.' }
    $runName = [IO.Path]::GetFileNameWithoutExtension($file.Name)
    $reserved = Get-Int64Property $report 'reserved_upper_bound_microusd'
    $runCalls = @($report.calls)
    $runs += [pscustomobject]@{
        Run = $runName
        File = $file.FullName
        Calls = $runCalls.Count
        ReservedMicrousd = $reserved
        Model = [string]$report.model
        SyntheticOnly = [bool]$report.synthetic_only
    }

    foreach ($call in $runCalls) {
        $provider = 'unknown'
        $model = [string]$report.model
        $completion = [string]$call.completion
        if ($completion -match 'provider=([^;\s]+)') { $provider = $Matches[1] }
        if ($completion -match 'model=([^\s}]+)') { $model = $Matches[1] }
        $calls += [pscustomobject]@{
            Run = $runName
            Role = [string]$call.role
            Provider = $provider
            Model = $model
            EstimatedMicrousd = Get-Int64Property $call 'estimated_api_equivalent_microusd'
            Error = [string]$call.error
        }
    }
}

function Format-Usd([int64]$microusd) {
    return ('${0:N6}' -f ($microusd / 1000000.0))
}

function Format-CostTotal([object[]]$items, [string]$property) {
    $known = @($items | Where-Object { $null -ne $_.$property })
    if ($items.Count -eq 0) { return 'Unknown (no records)' }
    if ($known.Count -eq 0) { return "Unknown (0/$($items.Count) recorded)" }
    $sum = [decimal]0
    foreach ($item in $known) { $sum += [decimal]$item.$property }
    $amount = ('${0:N6}' -f ($sum / 1000000))
    if ($known.Count -ne $items.Count) { return "$amount known subtotal ($($known.Count)/$($items.Count) recorded)" }
    return $amount
}

$lines = [Collections.Generic.List[string]]::new()
$lines.Add('# Harness cost reconciliation')
$lines.Add('')
$lines.Add("Generated: $(Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK')")
$lines.Add("Source: $HarnessRoot")
$lines.Add("Coverage: $scanned JSON files scanned; $($runs.Count) runs parsed; $malformed malformed files excluded; $nonRuns non-run files excluded.")
$lines.Add('Totals cover parsed runs only. Missing amounts are unknown; partial totals show only recorded values. Malformed files prevent complete source coverage.')
$lines.Add('')
$lines.Add('| Scope | Calls | Estimated observed | Reserved upper bound |')
$lines.Add('| --- | ---: | ---: | ---: |')
$lines.Add("| All parsed provider runs | $($calls.Count) | $(Format-CostTotal $calls 'EstimatedMicrousd') | $(Format-CostTotal $runs 'ReservedMicrousd') |")
$lines.Add('')
$lines.Add('## By provider/model')
$lines.Add('')
$lines.Add('| Provider | Model | Calls | Estimated observed |')
$lines.Add('| --- | --- | ---: | ---: |')
foreach ($group in ($calls | Group-Object Provider,Model | Sort-Object Name)) {
    $first = $group.Group[0]
    $lines.Add("| $($first.Provider) | $($first.Model) | $($group.Count) | $(Format-CostTotal $group.Group 'EstimatedMicrousd') |")
}
$lines.Add('')
$lines.Add('## By role')
$lines.Add('')
$lines.Add('| Role | Calls | Estimated observed | Errors |')
$lines.Add('| --- | ---: | ---: | ---: |')
foreach ($group in ($calls | Group-Object Role | Sort-Object Name)) {
    $errors = @($group.Group | Where-Object { -not [string]::IsNullOrWhiteSpace($_.Error) }).Count
    $lines.Add("| $($group.Name) | $($group.Count) | $(Format-CostTotal $group.Group 'EstimatedMicrousd') | $errors |")
}
$lines.Add('')
$lines.Add('Reserved upper bounds are admission guards, not invoices. Estimated observed values come from the harness ledger and remain synthetic unless the run explicitly records a real provider response.')

$output = $lines -join [Environment]::NewLine
if ($OutputPath) {
    $output | Set-Content -LiteralPath $OutputPath -Encoding utf8
}
Write-Output $output
