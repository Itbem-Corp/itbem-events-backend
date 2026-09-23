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

function Get-Int64Property($object, [string]$name) {
    if ($null -eq $object -or -not ($object.PSObject.Properties.Name -contains $name)) {
        return [int64]0
    }
    $value = $object.$name
    if ($null -eq $value -or [string]::IsNullOrWhiteSpace([string]$value)) {
        return [int64]0
    }
    return [int64]$value
}

foreach ($file in Get-ChildItem -LiteralPath $HarnessRoot -Recurse -Filter '*.json' -File) {
    try {
        $report = Get-Content -LiteralPath $file.FullName -Raw | ConvertFrom-Json
    } catch {
        continue
    }

    if (-not ($report.PSObject.Properties.Name -contains 'calls')) { continue }
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

$lines = [Collections.Generic.List[string]]::new()
$lines.Add('# Harness cost reconciliation')
$lines.Add('')
$lines.Add("Generated: $(Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK')")
$lines.Add("Source: $HarnessRoot")
$lines.Add('')
$lines.Add('| Scope | Calls | Estimated observed | Reserved upper bound |')
$lines.Add('| --- | ---: | ---: | ---: |')
$lines.Add("| All parsed provider runs | $($calls.Count) | $(Format-Usd (($calls | Measure-Object EstimatedMicrousd -Sum).Sum)) | $(Format-Usd (($runs | Measure-Object ReservedMicrousd -Sum).Sum)) |")
$lines.Add('')
$lines.Add('## By provider/model')
$lines.Add('')
$lines.Add('| Provider | Model | Calls | Estimated observed |')
$lines.Add('| --- | --- | ---: | ---: |')
foreach ($group in ($calls | Group-Object Provider,Model | Sort-Object Name)) {
    $first = $group.Group[0]
    $sum = [int64](($group.Group | Measure-Object EstimatedMicrousd -Sum).Sum)
    $lines.Add("| $($first.Provider) | $($first.Model) | $($group.Count) | $(Format-Usd $sum) |")
}
$lines.Add('')
$lines.Add('## By role')
$lines.Add('')
$lines.Add('| Role | Calls | Estimated observed | Errors |')
$lines.Add('| --- | ---: | ---: | ---: |')
foreach ($group in ($calls | Group-Object Role | Sort-Object Name)) {
    $sum = [int64](($group.Group | Measure-Object EstimatedMicrousd -Sum).Sum)
    $errors = @($group.Group | Where-Object { -not [string]::IsNullOrWhiteSpace($_.Error) }).Count
    $lines.Add("| $($group.Name) | $($group.Count) | $(Format-Usd $sum) | $errors |")
}
$lines.Add('')
$lines.Add('Reserved upper bounds are admission guards, not invoices. Estimated observed values come from the harness ledger and remain synthetic unless the run explicitly records a real provider response.')

$output = $lines -join [Environment]::NewLine
if ($OutputPath) {
    $output | Set-Content -LiteralPath $OutputPath -Encoding utf8
}
Write-Output $output
