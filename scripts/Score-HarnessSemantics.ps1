[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ReportPath,
    [string]$OutputPath = '',
    [switch]$AllowFailures,
    [switch]$AllowPartial
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $ReportPath -PathType Leaf)) {
    throw "Harness report not found: $ReportPath"
}

$report = Get-Content -LiteralPath $ReportPath -Raw | ConvertFrom-Json
if ($report.synthetic_only -isnot [bool] -or $report.synthetic_only -ne $true) {
    throw 'Semantic scorer only accepts synthetic harness reports.'
}

$checks = New-Object System.Collections.Generic.List[object]
$latestByRole = @{}

function Add-Check {
    param(
        [string]$Role,
        [string]$Name,
        [bool]$Passed,
        [string]$Detail,
        [switch]$Skipped
    )
    $script:checks.Add([pscustomobject]@{
            role    = $Role
            check   = $Name
            passed  = $Passed
            skipped = [bool]$Skipped
            detail  = $Detail
        })
}

foreach ($call in @($report.calls)) {
    $role = [string]$call.role
    if (-not [string]::IsNullOrWhiteSpace($role)) {
        # The report may contain a prior continuation. The last call for a role
        # is the newest provider answer and is what this independent scorer sees.
        $latestByRole[$role] = $call
    }
}

function Read-Completion {
    param($Call)
    if ($null -eq $Call -or $null -eq $Call.completion) {
        return $null
    }
    $content = [string]$Call.completion.content
    if ([string]::IsNullOrWhiteSpace($content) -or -not $content.TrimStart().StartsWith('{')) {
        return $null
    }
    try {
        return ($content | ConvertFrom-Json)
    } catch {
        return $null
    }
}

function Require-Role {
    param([string]$Role)
    if (-not $latestByRole.ContainsKey($Role)) {
        if ($AllowPartial) {
            Add-Check $Role 'role_present' $true 'Skipped: the supplied report is a partial role report.' -Skipped
        } else {
            Add-Check $Role 'role_present' $false 'No provider call for this role was found.'
        }
        return $null
    }
    $completion = Read-Completion $latestByRole[$Role]
    if ($null -eq $completion -or $completion -isnot [pscustomobject]) {
        Add-Check $Role 'completion_valid' $false 'The latest role response must be a nonempty JSON object.'
        return $null
    }
    return $completion
}

function Get-Outcome {
    param([string]$Role)
    if ($null -eq $report.outcomes) {
        return $false
    }
    $property = $report.outcomes.PSObject.Properties[$Role]
    if ($null -eq $property) {
        return $false
    }
    return ($property.Value -is [bool] -and $property.Value -eq $true)
}

# Fixture evidence must be typed text; null placeholders cannot prove grounding.
function Test-TextList {
    param($Value)
    if ($Value -isnot [array] -or $Value.Count -eq 0) { return $false }
    foreach ($item in $Value) {
        if ($item -isnot [string] -or [string]::IsNullOrWhiteSpace($item)) { return $false }
    }
    return $true
}

# Reviewer: independent of ParseCodeReview. The evaluator checks the security
# invariant and the concrete changed-line evidence expected by the fixture.
$reviewer = Require-Role 'reviewer_seeded_auth_bypass'
if ($null -ne $reviewer) {
    $findings = @($reviewer.findings)
    $security = @($findings | Where-Object {
            ([string]$_.file -eq 'auth.go') -and
            ([string]$_.category -eq 'security') -and
            ([string]$_.severity -in @('high', 'critical')) -and
            ([int]$_.line_start -eq 3) -and
            ([int]$_.line_end -eq 3) -and
            ([string]$_.side -eq 'head')
        })
    Add-Check 'reviewer_seeded_auth_bypass' 'rejects_authorization_bypass' ([string]$reviewer.verdict -eq 'request_changes' -and $security.Count -gt 0) 'Requires request_changes plus a high/critical auth.go finding on the changed head line.'
    $quoted = @($security | Where-Object { ([string]$_.evidence_quote) -match 'return true' })
    Add-Check 'reviewer_seeded_auth_bypass' 'quotes_changed_security_line' ($quoted.Count -gt 0) 'The finding must quote the unconditional return from the frozen patch.'
}

# QA: an observed failing command must remain failed even when preview passed.
$qa = Require-Role 'qa_observed_failure'
if ($null -ne $qa) {
    $failedChecks = @($qa.checks | Where-Object { [string]$_.status -eq 'failed' })
    Add-Check 'qa_observed_failure' 'preserves_observed_failure' ([string]$qa.verdict -eq 'failed' -and $failedChecks.Count -gt 0) 'Requires failed verdict and at least one failed observed check.'
}

# Summary: the handoff must remain a draft grounded in evidence, not a release
# authorization. The scorer intentionally checks structure and grounding only.
$summary = Require-Role 'delivery_grounded_summary'
if ($null -ne $summary) {
    Add-Check 'delivery_grounded_summary' 'grounded_draft' ($summary.executive -is [pscustomobject] -and $summary.technical -is [pscustomobject] -and (Test-TextList $summary.technical.evidence) -and (Test-TextList $summary.executive.risks)) 'Requires nonempty text arrays of executive risks and technical evidence for a non-authorizing handoff.'
}

# Product: options must be bounded and reversible rather than a single
# ungrounded prescription.
$product = Require-Role 'product_bounded_options'
if ($null -ne $product) {
    $directions = @($product.directions)
    $hasRisks = $directions.Count -ge 2 -and @($directions | Where-Object { $_.risk -is [string] -and -not [string]::IsNullOrWhiteSpace($_.risk) }).Count -eq $directions.Count
    Add-Check 'product_bounded_options' 'bounded_alternatives' ($hasRisks -and $null -ne $product.recommendation) 'Requires at least two directions, explicit risks and a recommendation.'
}

# Planner: missing context must block invention of implementation scope.
$planner = Require-Role 'planner_missing_context'
if ($null -ne $planner) {
    $files = @($planner.files_impacted)
    Add-Check 'planner_missing_context' 'does_not_invent_scope' ((Test-TextList $planner.context_gaps) -and $files.Count -eq 0) 'Requires nonempty text context gaps and an empty files_impacted list.'
}

# Implementer: independently check the final tool-shaped answer, not the
# worker's outcome flag. A missing/non-JSON answer is a semantic failure.
$implementer = Require-Role 'implementer_executable_acceptance'
if ($null -ne $implementer) {
    $content = [string]$implementer.content
    Add-Check 'implementer_executable_acceptance' 'bounded_edit_action' ([string]$implementer.action -eq 'edit' -and [string]$implementer.repository_ref -eq 'workspace://repo' -and [string]$implementer.path -eq 'note.go' -and -not [string]::IsNullOrWhiteSpace($content)) 'Requires a concrete edit in the approved workspace and file.'
    Add-Check 'implementer_executable_acceptance' 'durable_execution_outcome' (Get-Outcome 'implementer_executable_acceptance') 'The independent semantic action must also have a durable completed execution outcome.'
}

if (@($checks | Where-Object { -not $_.skipped }).Count -eq 0) {
    Add-Check 'report' 'evaluable_response_present' $false 'An empty partial report cannot demonstrate semantic success.'
}
$failed = @($checks | Where-Object { (-not $_.passed) -and (-not $_.skipped) })
$checkArray = $checks.ToArray()
$result = [ordered]@{
    schema_version = 1
    report_path    = (Resolve-Path -LiteralPath $ReportPath).Path
    evaluated_at   = (Get-Date).ToUniversalTime().ToString('o')
    passed         = ($failed.Count -eq 0)
    checks         = $checkArray
    limitations    = @(
        'Deterministic semantic smoke, not an expert or market-quality judge.',
        'Scores the latest response per role in the supplied synthetic report.',
        'Does not exercise production queues, remote publication, browser deployment or EventiApp.'
    )
}
$json = $result | ConvertTo-Json -Depth 10
if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
    $parent = Split-Path -Parent $OutputPath
    if ($parent -and -not (Test-Path -LiteralPath $parent)) {
        New-Item -ItemType Directory -Path $parent -Force | Out-Null
    }
    Set-Content -LiteralPath $OutputPath -Value $json -Encoding UTF8
}
Write-Output $json

if ($failed.Count -gt 0 -and -not $AllowFailures) {
    exit 1
}
